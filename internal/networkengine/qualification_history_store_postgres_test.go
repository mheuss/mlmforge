package networkengine

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPostgresQualificationHistoryStore(t *testing.T) *PostgresQualificationHistoryStore {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	return NewPostgresQualificationHistoryStore(pool)
}

func TestPostgresQualificationHistoryStore_GetByPeriod_UserIDAscOrder(t *testing.T) {
	store := newTestPostgresQualificationHistoryStore(t)
	ctx := context.Background()

	// Insert in reverse order; query must come back ascending.
	userZ := mustParseUUID(t, "ffffffff-ffff-ffff-ffff-ffffffffffff")
	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userM := mustParseUUID(t, "88888888-8888-8888-8888-888888888888")

	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userZ, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userM, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
	}))

	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, userA, rows[0].UserID)
	assert.Equal(t, userM, rows[1].UserID)
	assert.Equal(t, userZ, rows[2].UserID)
}

func TestPostgresQualificationHistoryStore_SaveAndGetByPeriod(t *testing.T) {
	store := newTestPostgresQualificationHistoryStore(t)
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")

	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))

	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, userA, rows[0].UserID)
	assert.Equal(t, "silver", *rows[0].Rank)
	assert.Equal(t, uint16(2), *rows[0].Ordinal)
	assert.Equal(t, userB, rows[1].UserID)
	require.NotNil(t, rows[1].Rank)
	assert.Equal(t, "gold", *rows[1].Rank)
	require.NotNil(t, rows[1].Ordinal)
	assert.Equal(t, uint16(3), *rows[1].Ordinal)
	assert.False(t, rows[1].EvaluatedAt.IsZero())
	assert.False(t, rows[0].EvaluatedAt.IsZero())
}

func TestPostgresQualificationHistoryStore_GetByUserAndPeriodRange(t *testing.T) {
	store := newTestPostgresQualificationHistoryStore(t)
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")

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

	// Inclusive [2026-03, 2026-04] for userA → two rows ascending.
	rows, err := store.GetByUserAndPeriodRange(ctx, userA, "2026-03", "2026-04")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "2026-03", rows[0].PeriodID)
	assert.Equal(t, "2026-04", rows[1].PeriodID)
	assert.Equal(t, "bronze", *rows[0].Rank)
	assert.Equal(t, "silver", *rows[1].Rank)

	// Inverted range returns empty (Postgres: WHERE period_id BETWEEN $2 AND $3
	// is false when from > to).
	rows, err = store.GetByUserAndPeriodRange(ctx, userA, "2026-04", "2026-03")
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestPostgresQualificationHistoryStore_GetByUsersAndPeriodRange(t *testing.T) {
	store := newTestPostgresQualificationHistoryStore(t)
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")
	userC := mustParseUUID(t, "00000000-0000-0000-0000-000000000003")

	require.NoError(t, store.SaveResult(ctx, "2026-03", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)},
		{UserID: userB, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
	}))
	require.NoError(t, store.SaveResult(ctx, "2026-04", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		{UserID: userC, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))
	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))

	// Proves pgx encodes []uuid.UUID for = ANY($1), plus the bounded read.
	rows, err := store.GetByUsersAndPeriodRange(ctx, []uuid.UUID{userA, userB}, "2026-03", "2026-04")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, "2026-03", rows[0].PeriodID)
	assert.Equal(t, userA, rows[0].UserID)
	assert.Equal(t, userB, rows[1].UserID)
	assert.Equal(t, "2026-04", rows[2].PeriodID)
	assert.Equal(t, userA, rows[2].UserID)

	// Postgres has its own early-return path (before any round-trip): an empty
	// user set and an inverted range must both yield no rows, matching Memory.
	empty, err := store.GetByUsersAndPeriodRange(ctx, nil, "2026-03", "2026-05")
	require.NoError(t, err)
	assert.Empty(t, empty)
	inverted, err := store.GetByUsersAndPeriodRange(ctx, []uuid.UUID{userA}, "2026-05", "2026-03")
	require.NoError(t, err)
	assert.Empty(t, inverted)
}

func TestPostgresQualificationHistoryStore_SaveResult_CompleteReplacement(t *testing.T) {
	store := newTestPostgresQualificationHistoryStore(t)
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

	// Re-evaluate with only {A, B}.
	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))

	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, rows, 2)

	var ids []uuid.UUID
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	assert.NotContains(t, ids, userC, "C must be removed by full replacement (BR5)")
}

func TestPostgresQualificationHistoryStore_SaveResult_EmptyEntriesEmptiesPeriod(t *testing.T) {
	// SaveResult(ctx, periodID, nil) must DELETE prior rows and skip
	// CopyFrom (no entries to insert). Pins the deletion-only semantic on
	// the Postgres path.
	store := newTestPostgresQualificationHistoryStore(t)
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

func TestPostgresQualificationHistoryStore_CheckConstraint_RankOrdinalPair(t *testing.T) {
	store := newTestPostgresQualificationHistoryStore(t)
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")

	// Rank set, Ordinal nil — violates CHECK ((rank IS NULL) = (ordinal IS NULL)).
	err := store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: nil},
	})
	require.Error(t, err, "CHECK constraint should reject mismatched rank/ordinal pair")
	assert.Contains(t, err.Error(), "save qualification history")
}

func TestPostgresQualificationHistoryStore_CheckConstraint_EmptyPeriodID(t *testing.T) {
	// The Go store rejects empty period_id before issuing SQL, so this CHECK
	// only matters as defense against non-app writers. Exercise it via a raw
	// INSERT to confirm the schema enforces it.
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	_, err := pool.Exec(ctx,
		"INSERT INTO qualification_history (period_id, user_id, rank, ordinal) VALUES ($1, $2, $3, $4)",
		"", userA, "silver", 2,
	)
	require.Error(t, err, "CHECK (period_id <> '') should reject empty period_id")
}

func TestPostgresQualificationHistoryStore_CheckConstraint_OrdinalRange(t *testing.T) {
	// The Go store can never write an out-of-range ordinal (uint16 + mapper
	// rejects 0), so this CHECK is defense against non-app writers. Verify
	// via raw INSERT for both ends of the range.
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")

	// ordinal = 0 is rejected.
	_, err := pool.Exec(ctx,
		"INSERT INTO qualification_history (period_id, user_id, rank, ordinal) VALUES ($1, $2, $3, $4)",
		"2026-05", userA, "silver", 0,
	)
	require.Error(t, err, "CHECK should reject ordinal = 0")

	// ordinal = 70000 (> uint16 max) is rejected.
	_, err = pool.Exec(ctx,
		"INSERT INTO qualification_history (period_id, user_id, rank, ordinal) VALUES ($1, $2, $3, $4)",
		"2026-05", userB, "silver", 70000,
	)
	require.Error(t, err, "CHECK should reject ordinal > 65535")
}

func TestPostgresQualificationHistoryStore_SaveResult_RollbackPreservesPriorRows(t *testing.T) {
	store := newTestPostgresQualificationHistoryStore(t)
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")
	userC := mustParseUUID(t, "00000000-0000-0000-0000-000000000003")

	// Seed period 2026-05 with {A, B}.
	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))

	// Attempt a re-write that fails: third entry has rank set but ordinal nil,
	// which violates the CHECK constraint and aborts the transaction.
	err := store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		{UserID: userC, Rank: strPtr("bronze"), Ordinal: nil}, // bad pair
	})
	require.Error(t, err)

	// NFR4: the original two rows must still be present. The DELETE-then-CopyFrom
	// transaction rolled back, so {A, B} survive untouched.
	rows, err := store.GetByPeriod(ctx, "2026-05")
	require.NoError(t, err)
	require.Len(t, rows, 2, "prior period rows must remain after a failed SaveResult (NFR4)")

	var ids []uuid.UUID
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	assert.Contains(t, ids, userA)
	assert.Contains(t, ids, userB)
	assert.NotContains(t, ids, userC)
}
