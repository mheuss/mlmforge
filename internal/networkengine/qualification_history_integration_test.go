package networkengine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQualificationHistoryPersistence_EndToEnd(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	store := NewPostgresQualificationHistoryStore(pool)

	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	rootID := "00000000-0000-0000-0000-000000000001"
	childID := "00000000-0000-0000-0000-000000000002"

	require.NoError(t, client.LoadPlan(ctx, json.RawMessage(rankIntegrationPlanJSON)))
	require.NoError(t, client.CreateTree(ctx, structureName, "unilevel"))
	require.NoError(t, client.AddRoot(ctx, structureName, rootID, 100))
	require.NoError(t, client.AddNode(ctx, structureName, childID, rootID, rootID, 200))

	req := EvaluateRanksRequest{
		Distributors: map[string]DistributorPrimitivesDTO{
			rootID:  {PersonalVolume: 100.0, Status: "active", HasOrderInPeriod: true, ActiveProducts: []string{}},
			childID: {PersonalVolume: 100.0, Status: "active", HasOrderInPeriod: true, ActiveProducts: []string{}},
		},
		VolumeSources: []VolumeSourceDTO{},
	}

	// Period 2026-05: both users qualify and persist.
	result, err := client.EvaluateRanks(ctx, req, WithPersistence("2026-05", store))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Ranks, 2)

	// Period 2026-06: same distributors, different period_id.
	result, err = client.EvaluateRanks(ctx, req, WithPersistence("2026-06", store))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Ranks, 2)

	rootUUID := mustParseUUID(t, rootID)
	childUUID := mustParseUUID(t, childID)

	// Multi-period range read: GetByUserAndPeriodRange returns both periods
	// in period_id ASC order. This is the access pattern HEU-446 will use.
	rootRange, err := store.GetByUserAndPeriodRange(ctx, rootUUID, "2026-05", "2026-06")
	require.NoError(t, err)
	require.Len(t, rootRange, 2)
	assert.Equal(t, "2026-05", rootRange[0].PeriodID)
	assert.Equal(t, "2026-06", rootRange[1].PeriodID)
	require.NotNil(t, rootRange[0].Rank)
	assert.Equal(t, "member", *rootRange[0].Rank)
	require.NotNil(t, rootRange[0].Ordinal)
	assert.Equal(t, uint16(1), *rootRange[0].Ordinal)

	// Single-period bulk read: GetByPeriod returns all distributors sorted
	// by user_id ASC.
	period05, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, period05, 2)

	// Re-evaluate 2026-05 with only the root. BR5: child row disappears for
	// that period only; 2026-06 must be untouched.
	reqRootOnly := req
	reqRootOnly.Distributors = map[string]DistributorPrimitivesDTO{
		rootID: req.Distributors[rootID],
	}
	_, err = client.EvaluateRanks(ctx, reqRootOnly, WithPersistence("2026-05", store))
	require.NoError(t, err)

	after05, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, after05, 1)
	assert.Equal(t, rootUUID, after05[0].UserID)

	after06, err := store.GetByPeriod(ctx, "2026-06")
	require.NoError(t, err)
	require.Len(t, after06, 2, "2026-06 must be untouched by 2026-05 re-evaluation")

	childRows, err := store.GetByUserAndPeriodRange(ctx, childUUID, "2026-05", "2026-06")
	require.NoError(t, err)
	require.Len(t, childRows, 1, "child must remain in 2026-06 but be removed from 2026-05")
	assert.Equal(t, "2026-06", childRows[0].PeriodID)
}

func TestQualificationHistoryPersistence_NoOptionLeavesStoreEmpty(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	store := NewPostgresQualificationHistoryStore(pool)

	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	rootID := "00000000-0000-0000-0000-000000000001"

	require.NoError(t, client.LoadPlan(ctx, json.RawMessage(rankIntegrationPlanJSON)))
	require.NoError(t, client.CreateTree(ctx, structureName, "unilevel"))
	require.NoError(t, client.AddRoot(ctx, structureName, rootID, 100))

	req := EvaluateRanksRequest{
		Distributors: map[string]DistributorPrimitivesDTO{
			rootID: {PersonalVolume: 100.0, Status: "active", HasOrderInPeriod: true, ActiveProducts: []string{}},
		},
		VolumeSources: []VolumeSourceDTO{},
	}

	_, err = client.EvaluateRanks(ctx, req) // no options
	require.NoError(t, err)

	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	assert.Empty(t, rows, "BR2: no writes when WithPersistence is absent")
}
