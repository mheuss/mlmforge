package networkengine

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }
func u16Ptr(v uint16) *uint16 { return &v }

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	require.NoError(t, err)
	return id
}

func TestMemoryQualificationHistoryStore_ImplementsInterface(t *testing.T) {
	var _ QualificationHistoryStore = (*MemoryQualificationHistoryStore)(nil)
}

func TestMemoryQualificationHistoryStore_SaveAndGetByPeriod_RoundTrip(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")

	entries := []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}
	require.NoError(t, store.SaveResult(ctx, "2026-05", entries))

	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, rows, 2)

	// GetByPeriod sorts by user_id ASC.
	assert.Equal(t, userA, rows[0].UserID)
	assert.Equal(t, "2026-05", rows[0].PeriodID)
	require.NotNil(t, rows[0].Rank)
	assert.Equal(t, "silver", *rows[0].Rank)
	require.NotNil(t, rows[0].Ordinal)
	assert.Equal(t, uint16(2), *rows[0].Ordinal)
	assert.False(t, rows[0].EvaluatedAt.IsZero())

	assert.Equal(t, userB, rows[1].UserID)
	require.NotNil(t, rows[1].Rank)
	assert.Equal(t, "gold", *rows[1].Rank)
}

func TestMemoryQualificationHistoryStore_SaveResult_ReplacesPriorPeriod(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")
	userC := mustParseUUID(t, "00000000-0000-0000-0000-000000000003")

	// First write: {A, B, C}.
	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		{UserID: userC, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)},
	}))

	// Re-write the same period with only {A, B}.
	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))

	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, rows, 2)

	userIDs := []uuid.UUID{rows[0].UserID, rows[1].UserID}
	assert.NotContains(t, userIDs, userC, "C must be removed after re-evaluation drops them")
}

func TestMemoryQualificationHistoryStore_SaveResult_EmptyEntriesEmptiesPeriod(t *testing.T) {
	// Degenerate case of BR5: re-evaluating with an empty distributor set
	// must remove every prior row for the period. Pins the deletion-only
	// semantic of SaveResult(ctx, periodID, nil).
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")

	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))

	require.NoError(t, store.SaveResult(ctx, "2026-05", nil))

	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	assert.Empty(t, rows, "empty entries must wipe the period")
}

func TestMemoryQualificationHistoryStore_UnrankedRoundTrip(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")

	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: nil, Ordinal: nil}, // Unranked
	}))

	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, userA, rows[0].UserID)
	assert.Nil(t, rows[0].Rank)
	assert.Nil(t, rows[0].Ordinal)

	// BR7: missing row is distinguishable from explicit Unranked.
	missing, err := store.GetByPeriod(ctx, "2026-04")
	require.NoError(t, err)
	assert.Empty(t, missing)
}

func TestMemoryQualificationHistoryStore_GetByUserAndPeriodRange(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")

	// Three periods for userA, one period for userB.
	require.NoError(t, store.SaveResult(ctx, "2026-03", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)},
	}))
	require.NoError(t, store.SaveResult(ctx, "2026-04", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
	}))
	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))

	// Closed range [2026-03, 2026-04] for userA → two rows in ascending order.
	rows, err := store.GetByUserAndPeriodRange(ctx, userA, "2026-03", "2026-04")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "2026-03", rows[0].PeriodID)
	assert.Equal(t, "2026-04", rows[1].PeriodID)

	// Inverted range returns nothing.
	rows, err = store.GetByUserAndPeriodRange(ctx, userA, "2026-04", "2026-03")
	require.NoError(t, err)
	assert.Empty(t, rows)

	// Empty result for unknown user.
	unknown := mustParseUUID(t, "00000000-0000-0000-0000-0000000000ff")
	rows, err = store.GetByUserAndPeriodRange(ctx, unknown, "2026-03", "2026-05")
	require.NoError(t, err)
	assert.Empty(t, rows)
}
