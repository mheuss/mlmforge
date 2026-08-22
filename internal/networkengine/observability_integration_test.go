package networkengine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/mlmforge/mlmforge/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// e2eLogExporter is an in-memory sdklog.Exporter that captures the OTel log
// records the observer emits, so the end-to-end test can assert on them.
type e2eLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *e2eLogExporter) Export(_ context.Context, recs []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.records = append(e.records, recs...)
	return nil
}

func (e *e2eLogExporter) Shutdown(context.Context) error   { return nil }
func (e *e2eLogExporter) ForceFlush(context.Context) error { return nil }

func (e *e2eLogExporter) snapshot() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdklog.Record(nil), e.records...)
}

// unilevelPlanBadCompression is a valid unilevel plan whose compression is
// SkipBelowRank with no rank_threshold. That is a misconfiguration the engine
// warns about (target network_engine::commission::walk) while still returning a
// normal result — a debug-safe way to trigger a real signal. Raw JSON rather
// than a map[string]any, which avoids re-marshaling. That used to be required:
// re-marshaling sorts keys, and the sorted order broke the adjacent-tagged
// structure config. HEU-648 fixed that, so it is now just the cheaper path
// (docs/development/network-engine.md).
const unilevelPlanBadCompression = `{"name":"Integration Test Plan","version":1,"structures":[{"type":"unilevel","config":{"name":"Test","level_commission":{"broad_commission_percent":0.40,"volume_to_dollar_multiplier":null,"commissionable_depth":3,"rate_table":{"member":{"1":0.05,"2":0.05,"3":0.05}}},"compression":{"enabled":true,"mode":"skip_below_rank","rank_threshold":null}}}],"period":{"length":"month","start_date":"2026-03-01","payout_lag_days":14},"volume":{"inhibit_signup_volume":false,"base_currency":"USD","volume_to_dollar_multiplier":1.0,"deduct_qualifying_volume":false},"ranks":[{"name":"member","ordinal":1,"qualification":{"structures":[],"required_products":[]},"qualified_structures":["Test"],"demotion_policy":"promotion_only"}],"rank_tracking":{"track_achieved_rank":false},"rank_features":{"constraints_enabled":false,"overrides_enabled":false},"commission_eligibility":{"min_personal_volume":0.0,"require_order_in_period":false,"eligible_statuses":[],"active_leg_tiers":[]},"bonuses":{"matching":null,"sponsor":null,"fast_start":null,"rank_advancement":null,"leadership_development":null,"infinity":null,"lifestyle":null,"pool":null,"matrix_completion":null,"position":null,"board_cycling":null},"payout":{"base_currency":"USD","minimum_amount":50.0,"split_payouts_enabled":true,"methods":[{"type":"bank_transfer","fee":2.50}]},"caps":{"per_distributor_per_period":null,"company_payout_cap_percent":0.42,"cap_enforcement":"pro_rata","clawback_on_refund":false},"placement":{"donated_placement":null,"holding_tank":null,"binary_placement":null}}`

// TestObservabilityEndToEnd proves the whole Rust->Go->OTel path with a real
// worker: an engine warn during a commission calculation is demuxed from the
// response, converted by the observer into an OTel log record, and correlated by
// trace id to the Go span that drove the request. This is forward infrastructure
// (nothing in production passes WithSignalHandler yet); the test proves the path.
func TestObservabilityEndToEnd(t *testing.T) {
	binaryPath := findWorkerBinary(t) // skips when the worker binary isn't built

	// In-memory OTel capture. A simple (synchronous) processor makes the emitted
	// record observable the moment HandleSignal runs, without waiting on a batch.
	exporter := &e2eLogExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	observer := observability.NewObserver(lp)

	// The intended production wiring: pass the observer's handler to the client.
	client, err := NewEngineClient(context.Background(), binaryPath, WithSignalHandler(observer.HandleSignal))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Stop() })

	ctx := context.Background()
	const (
		structure = "Test"
		root      = "00000000-0000-0000-0000-000000000001"
		mid       = "00000000-0000-0000-0000-000000000002"
		leaf      = "00000000-0000-0000-0000-000000000003"
	)

	require.NoError(t, client.LoadPlan(ctx, json.RawMessage(unilevelPlanBadCompression)))
	require.NoError(t, client.CreateTree(ctx, structure, "unilevel"))
	require.NoError(t, client.AddRoot(ctx, structure, root, 100))
	require.NoError(t, client.AddNode(ctx, structure, mid, root, root, 200))
	require.NoError(t, client.AddNode(ctx, structure, leaf, mid, mid, 300))

	// Drive the calculation inside a span so the request carries trace context
	// and the warn signal comes back correlated to it.
	traceID, err := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("b7ad6b7169203331")
	require.NoError(t, err)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	tracedCtx := trace.ContextWithSpanContext(ctx, sc)

	earnings, err := client.CalculateUnilevel(tracedCtx, CalculateUnilevelRequest{
		StructureName: structure,
		Snapshots: map[string]DistributorSnapshotDTO{
			root: {Rank: "member", PersonalVolume: 100, Status: "active", HasOrderInPeriod: true},
			mid:  {Rank: "member", PersonalVolume: 100, Status: "active", HasOrderInPeriod: true},
			leaf: {Rank: "member", PersonalVolume: 100, Status: "active", HasOrderInPeriod: true},
		},
		Volume: []VolumeSourceDTO{{SourceID: leaf, CVAmount: 100}},
	})

	// (a) The response was demuxed correctly despite the interleaved signal.
	require.NoError(t, err)
	require.NotEmpty(t, earnings, "calculation should still return earnings")

	// Flush the synchronous processor and inspect the captured records.
	require.NoError(t, lp.ForceFlush(ctx))
	records := exporter.snapshot()
	require.NotEmpty(t, records, "the engine warn should have produced a log record")

	var found bool
	for i := range records {
		rec := &records[i]
		if rec.Severity() != otellog.SeverityWarn ||
			!strings.Contains(rec.Body().AsString(), "SkipBelowRank compression") {
			continue
		}
		found = true

		// (c) Cross-language correlation: the Go span's ids are stamped natively.
		assert.Equal(t, traceID.String(), rec.TraceID().String(),
			"log record should carry the Go span's trace id")
		assert.Equal(t, spanID.String(), rec.SpanID().String(),
			"log record should carry the Go span's span id")

		// The engine module path survives the bridge as the target attribute.
		attrs := make(map[string]otellog.Value)
		rec.WalkAttributes(func(kv otellog.KeyValue) bool {
			attrs[kv.Key] = kv.Value
			return true
		})
		assert.Equal(t, "network_engine::commission::walk", attrs["target"].AsString(),
			"target attribute should be the engine module path")
	}
	require.True(t, found, "expected a Warn record about SkipBelowRank compression")
}
