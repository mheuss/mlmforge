package networkengine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/config"
	"github.com/mlmforge/mlmforge/internal/period"
)

// RankDriver runs per-period rank evaluation: it sizes and builds the prior
// axis, fetches history, evaluates, and persists. The caller wires the
// EngineClient (LoadPlan + tree) before using the driver.
type RankDriver struct {
	client   *EngineClient
	store    QualificationHistoryStore
	plan     *config.CompensationPlan
	provider PeriodInputProvider
	seq      *period.Sequence
}

// NewRankDriver builds the period sequence from the plan. A missing start_date
// is a loud error (design BR6).
func NewRankDriver(
	client *EngineClient, store QualificationHistoryStore,
	plan *config.CompensationPlan, provider PeriodInputProvider,
) (*RankDriver, error) {
	if client == nil || store == nil || plan == nil || provider == nil {
		return nil, fmt.Errorf("rank driver: client, store, plan, and provider are all required")
	}
	if plan.Period.StartDate == nil || *plan.Period.StartDate == "" {
		return nil, fmt.Errorf("rank driver: plan period start_date is required")
	}
	seq, err := period.NewSequence(plan.Period.Length, *plan.Period.StartDate)
	if err != nil {
		return nil, fmt.Errorf("rank driver: %w", err)
	}
	return &RankDriver{client: client, store: store, plan: plan, provider: provider, seq: seq}, nil
}

// guardAfterStart rejects evaluation of a period before the plan begins (BR9).
func (d *RankDriver) guardAfterStart(asOf time.Time) error {
	if d.seq.IsBeforeStart(asOf) {
		return fmt.Errorf("rank driver: %s is before the plan start period", asOf.Format("2006-01-02"))
	}
	return nil
}

// EvaluatePeriod evaluates and persists the period containing asOf. It builds
// the strictly-prior axis (sized by MaxHistoryDepth), fetches that history,
// and evaluates with persistence. The axis is empty only when the plan has no
// time gate (depth 0), so HEU-446's TimeGateWithoutHistory guard never trips.
func (d *RankDriver) EvaluatePeriod(ctx context.Context, asOf time.Time) (*EvaluationResultDTO, error) {
	if err := d.guardAfterStart(asOf); err != nil {
		return nil, err
	}
	periodID := d.seq.Label(asOf)
	inputs, err := d.provider.InputsFor(ctx, periodID)
	if err != nil {
		return nil, fmt.Errorf("rank driver: inputs for %s: %w", periodID, err)
	}
	// Normalize nil to empty: the Rust EvaluationInputs.distributors/volume_sources
	// fields lack serde(default) and have no omitempty, so a JSON null fails to
	// deserialize at the worker. Empty {} / [] are required.
	if inputs.Distributors == nil {
		inputs.Distributors = map[string]DistributorPrimitivesDTO{}
	}
	if inputs.VolumeSources == nil {
		inputs.VolumeSources = []VolumeSourceDTO{}
	}
	depth := config.MaxHistoryDepth(d.plan)
	axis := d.seq.PriorLabels(asOf, depth) // DESC; nil when depth == 0
	ids, err := distributorIDs(inputs.Distributors)
	if err != nil {
		return nil, err
	}
	_, hist, err := BuildHistoryWindow(ctx, d.store, ids, axis)
	if err != nil {
		return nil, fmt.Errorf("rank driver: build history for %s: %w", periodID, err)
	}
	req := EvaluateRanksRequest{
		Distributors:  inputs.Distributors,
		VolumeSources: inputs.VolumeSources,
		HistoryWindow: axis,
		History:       hist,
	}
	result, err := d.client.EvaluateRanks(ctx, req, WithPersistence(periodID, d.store))
	if err != nil {
		return result, fmt.Errorf("rank driver: evaluate %s: %w", periodID, err)
	}
	return result, nil
}

// distributorIDs parses the request's distributor map keys into sorted UUIDs.
// A non-UUID key is a loud error, named, before any engine call.
func distributorIDs(m map[string]DistributorPrimitivesDTO) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(m))
	for k := range m {
		id, err := uuid.Parse(k)
		if err != nil {
			return nil, fmt.Errorf("rank driver: invalid distributor id %q: %w", k, err)
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids, nil
}
