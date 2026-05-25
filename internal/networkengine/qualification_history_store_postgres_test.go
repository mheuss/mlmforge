package networkengine

import (
	"context"
	"testing"

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
	assert.False(t, rows[0].EvaluatedAt.IsZero())
}
