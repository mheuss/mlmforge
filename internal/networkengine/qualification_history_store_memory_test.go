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
