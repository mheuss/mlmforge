package networkengine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mlmforge/mlmforge/internal/config"
)

func TestDistributorIDs(t *testing.T) {
	t.Run("two valid UUIDs parse to sorted slice", func(t *testing.T) {
		// The interior-segment key (ffff in segment 3) sorts between the two
		// trailing-byte keys, confirming full-string lexical order.
		m := map[string]DistributorPrimitivesDTO{
			"00000000-0000-0000-0000-000000000002": {},
			"00000000-ffff-0000-0000-000000000000": {},
			"00000000-0000-0000-0000-000000000001": {},
		}
		ids, err := distributorIDs(m)
		require.NoError(t, err)
		require.Len(t, ids, 3)
		assert.True(t, ids[0].String() < ids[1].String(), "expected ascending order by string")
		assert.True(t, ids[1].String() < ids[2].String(), "expected ascending order by string")
		assert.Equal(t, "00000000-0000-0000-0000-000000000001", ids[0].String())
		assert.Equal(t, "00000000-0000-0000-0000-000000000002", ids[1].String())
		assert.Equal(t, "00000000-ffff-0000-0000-000000000000", ids[2].String())
	})

	t.Run("bad UUID key returns error naming the key", func(t *testing.T) {
		m := map[string]DistributorPrimitivesDTO{
			"not-a-uuid": {},
		}
		_, err := distributorIDs(m)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not-a-uuid")
	})

	t.Run("empty map returns empty slice and no error", func(t *testing.T) {
		m := map[string]DistributorPrimitivesDTO{}
		ids, err := distributorIDs(m)
		require.NoError(t, err)
		assert.Empty(t, ids)
	})
}

// monthlyWindowPlan builds a minimal monthly plan for rank driver tests.
func monthlyWindowPlan(startDate string, windowPeriods uint8) *config.CompensationPlan {
	return &config.CompensationPlan{
		Period: config.PeriodConfig{Length: "month", StartDate: &startDate},
		Ranks: []config.RankDefinition{{
			Name:    "Director",
			Ordinal: 3,
			Qualification: config.RankQualification{
				Window: &config.RankQualificationWindow{
					ThresholdRank: "Director", QualifyingPeriods: 1, WindowPeriods: windowPeriods,
				},
			},
		}},
	}
}

func TestNewRankDriver_RequiresStartDate(t *testing.T) {
	plan := monthlyWindowPlan("2026-01-01", 6)
	plan.Period.StartDate = nil

	client := newEngineClientWithTransport(&mockTransport{response: json.RawMessage(`null`)})
	store := NewMemoryQualificationHistoryStore()
	provider := NewMemoryPeriodInputProvider()

	_, err := NewRankDriver(client, store, plan, provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start_date")
}

func TestNewRankDriver_Succeeds(t *testing.T) {
	client := newEngineClientWithTransport(&mockTransport{response: json.RawMessage(`null`)})
	store := NewMemoryQualificationHistoryStore()
	provider := NewMemoryPeriodInputProvider()

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-01-01", 6), provider)
	require.NoError(t, err)
	assert.NotNil(t, driver)
}

func TestRankDriver_GuardAfterStart(t *testing.T) {
	// Plan starts 2026-03-01; evaluating 2026-02-15 is before the plan start.
	client := newEngineClientWithTransport(&mockTransport{response: json.RawMessage(`null`)})
	store := NewMemoryQualificationHistoryStore()
	provider := NewMemoryPeriodInputProvider()

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-03-01", 6), provider)
	require.NoError(t, err)

	asOf := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	_, err = driver.EvaluatePeriod(context.Background(), asOf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before the plan start")
}

func TestNewRankDriver_NilCollaboratorsError(t *testing.T) {
	client := newEngineClientWithTransport(&mockTransport{response: json.RawMessage(`null`)})
	store := NewMemoryQualificationHistoryStore()
	plan := monthlyWindowPlan("2026-01-01", 6)
	provider := NewMemoryPeriodInputProvider()

	_, err := NewRankDriver(nil, store, plan, provider)
	require.Error(t, err)

	_, err = NewRankDriver(client, nil, plan, provider)
	require.Error(t, err)

	_, err = NewRankDriver(client, store, nil, provider)
	require.Error(t, err)

	_, err = NewRankDriver(client, store, plan, nil)
	require.Error(t, err)
}

// monthlyNoGatePlan builds a minimal monthly plan whose only rank has no time
// gate, so MaxHistoryDepth is 0 and the driver sends no history (design BR7).
func monthlyNoGatePlan(startDate string) *config.CompensationPlan {
	return &config.CompensationPlan{
		Period: config.PeriodConfig{Length: "month", StartDate: &startDate},
		Ranks:  []config.RankDefinition{{Name: "Member", Ordinal: 1}},
	}
}

func TestRankDriver_EvaluatePeriod_BuildsAxisAndPersists(t *testing.T) {
	ctx := context.Background()
	userA := "00000000-0000-0000-0000-000000000001"
	uidA := mustParseUUID(t, userA)

	store := NewMemoryQualificationHistoryStore()
	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{{UserID: uidA, Rank: strPtr("Director"), Ordinal: u16Ptr(3)}}))
	require.NoError(t, store.SaveResult(ctx, "2026-04", []QualificationHistoryEntry{{UserID: uidA, Rank: strPtr("Director"), Ordinal: u16Ptr(3)}}))

	mock := &mockTransport{response: json.RawMessage(fmt.Sprintf(`{"ranks":{"%s":{"kind":"qualified","rank":"Director","ordinal":3}}}`, userA))}
	client := newEngineClientWithTransport(mock)

	provider := NewMemoryPeriodInputProvider()
	provider.Set("2026-06", PeriodInputs{
		Distributors:  map[string]DistributorPrimitivesDTO{userA: {PersonalVolume: 100, Status: "active", HasOrderInPeriod: true, ActiveProducts: []string{}}},
		VolumeSources: []VolumeSourceDTO{},
	})

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-01-01", 2), provider)
	require.NoError(t, err)

	result, err := driver.EvaluatePeriod(ctx, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NotNil(t, result)

	var sent EvaluateRanksRequest
	require.NoError(t, json.Unmarshal(mock.lastParams, &sent))
	assert.Equal(t, []string{"2026-05", "2026-04"}, sent.HistoryWindow) // DESC
	require.NotNil(t, sent.History[userA]["2026-05"])
	assert.Equal(t, uint16(3), *sent.History[userA]["2026-05"])

	rows, err := store.GetByPeriod(ctx, "2026-06")
	require.NoError(t, err)
	require.Len(t, rows, 1) // persisted
}

func TestRankDriver_EvaluatePeriod_NoGatePlanSendsNoHistory(t *testing.T) {
	ctx := context.Background()
	userA := "00000000-0000-0000-0000-000000000001"

	store := NewMemoryQualificationHistoryStore()
	mock := &mockTransport{response: json.RawMessage(`{"ranks":{}}`)}
	client := newEngineClientWithTransport(mock)

	provider := NewMemoryPeriodInputProvider()
	provider.Set("2026-06", PeriodInputs{
		Distributors:  map[string]DistributorPrimitivesDTO{userA: {PersonalVolume: 100, Status: "active", HasOrderInPeriod: true, ActiveProducts: []string{}}},
		VolumeSources: []VolumeSourceDTO{},
	})

	driver, err := NewRankDriver(client, store, monthlyNoGatePlan("2026-01-01"), provider)
	require.NoError(t, err)

	_, err = driver.EvaluatePeriod(ctx, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	var sent EvaluateRanksRequest
	require.NoError(t, json.Unmarshal(mock.lastParams, &sent))
	assert.Empty(t, sent.HistoryWindow) // no time gate -> no axis
	assert.Empty(t, sent.History)       // no history fetched

	// Raw-byte guard: Empty() above collapses omitted/null/[], so also assert the
	// wire omits both history fields rather than sending them at all. Since
	// HEU-626 a null would parse fine, so this no longer guards against a decode
	// failure — it guards the omitempty contract itself, which is what lets a
	// no-gate plan skip the keys entirely.
	// "history" is a prefix of "history_window", so this one check proves neither
	// the axis nor the history map is serialized.
	params := string(mock.lastParams)
	assert.NotContains(t, params, "history")
	assert.NotContains(t, params, ":null") // no field serialized as null

	rows, err := store.GetByPeriod(ctx, "2026-06")
	require.NoError(t, err)
	assert.Empty(t, rows) // period persisted (empty result -> empty rows)
}

func TestRankDriver_EvaluatePeriod_UnknownPeriodSendsEmptyNotNull(t *testing.T) {
	ctx := context.Background()

	store := NewMemoryQualificationHistoryStore()
	mock := &mockTransport{response: json.RawMessage(`{"ranks":{}}`)}
	client := newEngineClientWithTransport(mock)

	// Provider has no entry for 2026-06, so InputsFor returns a zero PeriodInputs
	// with nil Distributors and nil VolumeSources.
	provider := NewMemoryPeriodInputProvider()

	driver, err := NewRankDriver(client, store, monthlyNoGatePlan("2026-01-01"), provider)
	require.NoError(t, err)

	_, err = driver.EvaluatePeriod(ctx, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	// nil must normalize to empty {} / [], never null. Since HEU-626 the worker
	// reads null as empty on distributors/volume_sources too, so this pins the
	// driver's normalization rather than protecting against a rejection.
	params := string(mock.lastParams)
	assert.Contains(t, params, `"distributors":{}`)
	assert.Contains(t, params, `"volume_sources":[]`)
}

func TestRankDriver_EvaluatePeriod_NilActiveProductsSendsEmptyNotNull(t *testing.T) {
	ctx := context.Background()
	userA := "00000000-0000-0000-0000-000000000001"

	store := NewMemoryQualificationHistoryStore()
	mock := &mockTransport{response: json.RawMessage(`{"ranks":{}}`)}
	client := newEngineClientWithTransport(mock)

	// Distributor with a nil ActiveProducts slice -- the struct literal below
	// leaves the field out, and the DTO carries no omitempty, so nil still
	// reaches the wire. Without normalization it marshals to
	// "active_products":null. Since HEU-626 the Rust
	// worker reads that as empty, so this pins the driver's normalization rather
	// than protecting against a rejection -- the same belt-and-braces as the nil
	// distributors map, one level down.
	provider := NewMemoryPeriodInputProvider()
	provider.Set("2026-06", PeriodInputs{
		Distributors:  map[string]DistributorPrimitivesDTO{userA: {PersonalVolume: 100, Status: "active", HasOrderInPeriod: true}},
		VolumeSources: []VolumeSourceDTO{},
	})

	driver, err := NewRankDriver(client, store, monthlyNoGatePlan("2026-01-01"), provider)
	require.NoError(t, err)

	_, err = driver.EvaluatePeriod(ctx, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	params := string(mock.lastParams)
	assert.Contains(t, params, `"active_products":[]`)
	assert.NotContains(t, params, `"active_products":null`)
}

func TestRankDriver_EvaluatePeriod_DoesNotMutateProviderInputs(t *testing.T) {
	ctx := context.Background()
	userA := "00000000-0000-0000-0000-000000000001"

	store := NewMemoryQualificationHistoryStore()
	mock := &mockTransport{response: json.RawMessage(`{"ranks":{}}`)}
	client := newEngineClientWithTransport(mock)

	provider := NewMemoryPeriodInputProvider()
	provider.Set("2026-06", PeriodInputs{
		Distributors:  map[string]DistributorPrimitivesDTO{userA: {PersonalVolume: 100, Status: "active", HasOrderInPeriod: true}},
		VolumeSources: []VolumeSourceDTO{},
	})

	driver, err := NewRankDriver(client, store, monthlyNoGatePlan("2026-01-01"), provider)
	require.NoError(t, err)

	_, err = driver.EvaluatePeriod(ctx, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	// The driver normalizes a copy, not the provider's stored map. Re-fetch and
	// confirm the stored distributor's ActiveProducts is still nil (untouched).
	stored, err := provider.InputsFor(ctx, "2026-06")
	require.NoError(t, err)
	assert.Nil(t, stored.Distributors[userA].ActiveProducts, "driver must not mutate provider-owned inputs")
}

func TestRankDriver_EvaluatePeriod_BadDistributorIDErrors(t *testing.T) {
	ctx := context.Background()

	store := NewMemoryQualificationHistoryStore()
	mock := &mockTransport{response: json.RawMessage(`{"ranks":{}}`)}
	client := newEngineClientWithTransport(mock)

	provider := NewMemoryPeriodInputProvider()
	provider.Set("2026-06", PeriodInputs{
		Distributors:  map[string]DistributorPrimitivesDTO{"nope": {}},
		VolumeSources: []VolumeSourceDTO{},
	})

	driver, err := NewRankDriver(client, store, monthlyNoGatePlan("2026-01-01"), provider)
	require.NoError(t, err)

	_, err = driver.EvaluatePeriod(ctx, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")

	rows, err := store.GetByPeriod(ctx, "2026-06")
	require.NoError(t, err)
	assert.Empty(t, rows) // bad id errors before any persistence
}

// countingTransport wraps mockTransport and counts evaluate_ranks calls, so a
// backfill test can prove a same-period range evaluates exactly once.
// It embeds mockTransport by value; always use via pointer (&countingTransport{})
// so the pointer-receiver methods promote correctly.
type countingTransport struct {
	mockTransport
	calls int
}

func (c *countingTransport) Call(ctx context.Context, op string, params json.RawMessage) (json.RawMessage, error) {
	if op == "evaluate_ranks" {
		c.calls++
	}
	return c.mockTransport.Call(ctx, op, params)
}

// failOnPeriodStore wraps a QualificationHistoryStore and fails SaveResult for
// one period, so a backfill test can prove fail-stop on a persistence failure.
type failOnPeriodStore struct {
	QualificationHistoryStore
	failPeriod string
}

func (s *failOnPeriodStore) SaveResult(ctx context.Context, periodID string, entries []QualificationHistoryEntry) error {
	if periodID == s.failPeriod {
		return fmt.Errorf("boom")
	}
	return s.QualificationHistoryStore.SaveResult(ctx, periodID, entries)
}

// qualifiedDirectorInputs builds a single active distributor whose engine
// response (from the shared mock) qualifies as Director (ordinal 3).
func qualifiedDirectorInputs(userID string) PeriodInputs {
	return PeriodInputs{
		Distributors:  map[string]DistributorPrimitivesDTO{userID: {PersonalVolume: 100, Status: "active", HasOrderInPeriod: true, ActiveProducts: []string{}}},
		VolumeSources: []VolumeSourceDTO{},
	}
}

func TestRankDriver_Backfill_Accumulates(t *testing.T) {
	ctx := context.Background()
	userA := "00000000-0000-0000-0000-000000000001"

	store := NewMemoryQualificationHistoryStore()
	mock := &mockTransport{response: json.RawMessage(fmt.Sprintf(`{"ranks":{"%s":{"kind":"qualified","rank":"Director","ordinal":3}}}`, userA))}
	client := newEngineClientWithTransport(mock)

	provider := NewMemoryPeriodInputProvider()
	provider.Set("2026-01", qualifiedDirectorInputs(userA))
	provider.Set("2026-02", qualifiedDirectorInputs(userA))
	provider.Set("2026-03", qualifiedDirectorInputs(userA))

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-01-01", 2), provider)
	require.NoError(t, err)

	err = driver.Backfill(ctx, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	// lastParams holds the final (2026-03) request. Its axis is the two prior
	// periods, and both appear in History because the earlier loop iterations
	// persisted them before 2026-03 was evaluated.
	var sent EvaluateRanksRequest
	require.NoError(t, json.Unmarshal(mock.lastParams, &sent))
	assert.Equal(t, []string{"2026-02", "2026-01"}, sent.HistoryWindow) // DESC
	require.NotNil(t, sent.History[userA]["2026-02"])
	require.NotNil(t, sent.History[userA]["2026-01"])

	for _, periodID := range []string{"2026-01", "2026-02", "2026-03"} {
		rows, err := store.GetByPeriod(ctx, periodID)
		require.NoError(t, err)
		require.Len(t, rows, 1, "expected persisted row for %s", periodID)
	}
}

func TestRankDriver_Backfill_InvertedRangeErrors(t *testing.T) {
	ctx := context.Background()

	client := newEngineClientWithTransport(&mockTransport{response: json.RawMessage(`{"ranks":{}}`)})
	store := NewMemoryQualificationHistoryStore()
	provider := NewMemoryPeriodInputProvider()

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-01-01", 2), provider)
	require.NoError(t, err)

	err = driver.Backfill(ctx, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty backfill range")
}

func TestRankDriver_Backfill_SamePeriodInvertedRangeErrors(t *testing.T) {
	ctx := context.Background()

	client := newEngineClientWithTransport(&mockTransport{response: json.RawMessage(`{"ranks":{}}`)})
	store := NewMemoryQualificationHistoryStore()
	provider := NewMemoryPeriodInputProvider()

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-01-01", 2), provider)
	require.NoError(t, err)

	// from and to land in the same month, but from is the later civil date. The
	// inverted range must be rejected, not silently evaluated as a single period.
	err = driver.Backfill(ctx, time.Date(2026, 1, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty backfill range")
}

func TestRankDriver_Backfill_PreStartErrors(t *testing.T) {
	ctx := context.Background()

	client := newEngineClientWithTransport(&mockTransport{response: json.RawMessage(`{"ranks":{}}`)})
	store := NewMemoryQualificationHistoryStore()
	provider := NewMemoryPeriodInputProvider()

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-03-01", 2), provider)
	require.NoError(t, err)

	err = driver.Backfill(ctx, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before the plan start")
}

func TestRankDriver_Backfill_FailStop(t *testing.T) {
	ctx := context.Background()
	userA := "00000000-0000-0000-0000-000000000001"

	store := NewMemoryQualificationHistoryStore()
	mock := &mockTransport{response: json.RawMessage(fmt.Sprintf(`{"ranks":{"%s":{"kind":"qualified","rank":"Director","ordinal":3}}}`, userA))}
	client := newEngineClientWithTransport(mock)

	provider := NewMemoryPeriodInputProvider()
	provider.Set("2026-01", qualifiedDirectorInputs(userA))
	// 2026-02 has a bad-UUID distributor key, so it fails before any engine call.
	provider.Set("2026-02", PeriodInputs{
		Distributors:  map[string]DistributorPrimitivesDTO{"nope": {}},
		VolumeSources: []VolumeSourceDTO{},
	})
	provider.Set("2026-03", qualifiedDirectorInputs(userA))

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-01-01", 2), provider)
	require.NoError(t, err)

	err = driver.Backfill(ctx, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2026-02")

	rows, err := store.GetByPeriod(ctx, "2026-03")
	require.NoError(t, err)
	assert.Empty(t, rows) // stopped at 2026-02; never evaluated 2026-03
}

func TestRankDriver_Backfill_SamePeriodEvaluatesOnce(t *testing.T) {
	ctx := context.Background()
	userA := "00000000-0000-0000-0000-000000000001"

	store := NewMemoryQualificationHistoryStore()
	transport := &countingTransport{mockTransport: mockTransport{response: json.RawMessage(fmt.Sprintf(`{"ranks":{"%s":{"kind":"qualified","rank":"Director","ordinal":3}}}`, userA))}}
	client := newEngineClientWithTransport(transport)

	provider := NewMemoryPeriodInputProvider()
	provider.Set("2026-01", qualifiedDirectorInputs(userA))

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-01-01", 2), provider)
	require.NoError(t, err)

	// from and to land in the same month, so the range is exactly one period.
	err = driver.Backfill(ctx, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 25, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	assert.Equal(t, 1, transport.calls) // evaluated exactly once (design BR4)

	rows, err := store.GetByPeriod(ctx, "2026-01")
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestRankDriver_Backfill_FailStopOnStoreWrite(t *testing.T) {
	ctx := context.Background()
	userA := "00000000-0000-0000-0000-000000000001"

	store := &failOnPeriodStore{QualificationHistoryStore: NewMemoryQualificationHistoryStore(), failPeriod: "2026-02"}
	mock := &mockTransport{response: json.RawMessage(fmt.Sprintf(`{"ranks":{"%s":{"kind":"qualified","rank":"Director","ordinal":3}}}`, userA))}
	client := newEngineClientWithTransport(mock)

	provider := NewMemoryPeriodInputProvider()
	provider.Set("2026-01", qualifiedDirectorInputs(userA))
	provider.Set("2026-02", qualifiedDirectorInputs(userA))
	provider.Set("2026-03", qualifiedDirectorInputs(userA))

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-01-01", 2), provider)
	require.NoError(t, err)

	// The engine call for 2026-02 succeeds but its persistence fails, so
	// EvaluateRanks's partial-success contract surfaces an error and the run
	// fail-stops before evaluating 2026-03 (design NFR3).
	err = driver.Backfill(ctx, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2026-02")

	rows, err := store.GetByPeriod(ctx, "2026-01")
	require.NoError(t, err)
	require.Len(t, rows, 1) // 2026-01 persisted before the failure

	rows, err = store.GetByPeriod(ctx, "2026-03")
	require.NoError(t, err)
	assert.Empty(t, rows) // never reached 2026-03
}
