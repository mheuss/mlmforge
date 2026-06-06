package networkengine

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildHistoryWindow_PivotsFiltersAndPreservesAxis exercises the full
// contract: the caller-supplied DESC axis is returned unchanged, achieved
// ordinals are pivoted per distributor, distributors outside the requested
// set are dropped, and a (period, user) pair with no row is simply absent.
func TestBuildHistoryWindow_PivotsFiltersAndPreservesAxis(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")
	userC := mustParseUUID(t, "00000000-0000-0000-0000-000000000003")

	// 2026-04: A and B (and unrelated C) all evaluated.
	require.NoError(t, store.SaveResult(ctx, "2026-04", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)},
		{UserID: userC, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))
	// 2026-05: A and C evaluated; B is missing for this period.
	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		{UserID: userC, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}))

	axisIn := []string{"2026-05", "2026-04"} // most-recent-first
	axisOut, history, err := BuildHistoryWindow(ctx, store, []uuid.UUID{userA, userB}, axisIn)
	require.NoError(t, err)

	// Axis is returned unchanged (DESC), so the caller controls ordering.
	assert.Equal(t, []string{"2026-05", "2026-04"}, axisOut)

	// A's achieved ordinals are pivoted into the per-distributor map.
	require.Contains(t, history, userA.String())
	require.Contains(t, history[userA.String()], "2026-05")
	require.NotNil(t, history[userA.String()]["2026-05"])
	assert.Equal(t, uint16(3), *history[userA.String()]["2026-05"])
	require.NotNil(t, history[userA.String()]["2026-04"])
	assert.Equal(t, uint16(2), *history[userA.String()]["2026-04"])

	// Unrelated user C is filtered out entirely (I1).
	assert.NotContains(t, history, userC.String())

	// B was requested but has no 2026-05 row: that (period, user) is absent,
	// while its 2026-04 row is present.
	require.Contains(t, history, userB.String())
	assert.NotContains(t, history[userB.String()], "2026-05")
	require.Contains(t, history[userB.String()], "2026-04")
	assert.Equal(t, uint16(1), *history[userB.String()]["2026-04"])
}

// TestBuildHistoryWindow_PreservesUnrankedAsNil pins that an explicit Unranked
// row (rank/ordinal NULL) pivots to a present key with a nil *uint16 value,
// distinct from a missing key. The engine reads nil as "evaluated, Unranked".
func TestBuildHistoryWindow_PreservesUnrankedAsNil(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	require.NoError(t, store.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
		{UserID: userA, Rank: nil, Ordinal: nil}, // Unranked
	}))

	_, history, err := BuildHistoryWindow(ctx, store, []uuid.UUID{userA}, []string{"2026-05"})
	require.NoError(t, err)

	require.Contains(t, history, userA.String())
	val, ok := history[userA.String()]["2026-05"]
	require.True(t, ok, "Unranked must be a present key, not a missing one")
	assert.Nil(t, val, "Unranked pivots to a nil *uint16")
}

// TestBuildHistoryWindow_EmptyAxis returns an empty history and the empty axis
// unchanged. The Go client does not validate the axis; the BR9 fail-loud guard
// lives engine-side.
func TestBuildHistoryWindow_EmptyAxis(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	axisOut, history, err := BuildHistoryWindow(ctx, store, []uuid.UUID{userA}, nil)
	require.NoError(t, err)
	assert.Empty(t, axisOut)
	assert.Empty(t, history)
}
