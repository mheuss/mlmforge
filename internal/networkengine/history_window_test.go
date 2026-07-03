package networkengine

import (
	"context"
	"math/rand"
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

// countingStore embeds a real memory store and counts read calls, so a test
// can assert BuildHistoryWindow makes exactly one batched read and never fans
// out over GetByPeriod.
type countingStore struct {
	*MemoryQualificationHistoryStore
	byUsersAndRange int
	byPeriod        int
}

func (c *countingStore) GetByUsersAndPeriodRange(ctx context.Context, userIDs []uuid.UUID, fromPeriod, toPeriod string) ([]QualificationHistoryRow, error) {
	c.byUsersAndRange++
	return c.MemoryQualificationHistoryStore.GetByUsersAndPeriodRange(ctx, userIDs, fromPeriod, toPeriod)
}

func (c *countingStore) GetByPeriod(ctx context.Context, periodID string) ([]QualificationHistoryRow, error) {
	c.byPeriod++
	return c.MemoryQualificationHistoryStore.GetByPeriod(ctx, periodID)
}

// countingStore must satisfy the full interface to stand in for a real store.
var _ QualificationHistoryStore = (*countingStore)(nil)

func TestBuildHistoryWindow_OneRoundTrip(t *testing.T) {
	spy := &countingStore{MemoryQualificationHistoryStore: NewMemoryQualificationHistoryStore()}
	ctx := context.Background()
	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	require.NoError(t, spy.SaveResult(ctx, "2026-04", []QualificationHistoryEntry{{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)}}))
	require.NoError(t, spy.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)}}))

	_, _, err := BuildHistoryWindow(ctx, spy, []uuid.UUID{userA}, []string{"2026-05", "2026-04"})
	require.NoError(t, err)
	assert.Equal(t, 1, spy.byUsersAndRange, "one batched read for the whole window")
	assert.Equal(t, 0, spy.byPeriod, "no per-period fan-out")
}

func TestBuildHistoryWindow_EmptyDistributorSet(t *testing.T) {
	spy := &countingStore{MemoryQualificationHistoryStore: NewMemoryQualificationHistoryStore()}
	ctx := context.Background()
	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	require.NoError(t, spy.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)}}))

	axisOut, history, err := BuildHistoryWindow(ctx, spy, nil, []string{"2026-05"})
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-05"}, axisOut)
	assert.Empty(t, history)
	assert.Equal(t, 0, spy.byUsersAndRange, "no store call for an empty distributor set")
	assert.Equal(t, 0, spy.byPeriod)
}

func TestBuildHistoryWindow_NonContiguousAxisExcludesOffAxisPeriods(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()
	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")

	// 2026-04 is inside the [2026-03, 2026-05] BETWEEN range the rewrite computes
	// but is NOT on the axis. The inAxis filter must drop it.
	for _, p := range []string{"2026-03", "2026-04", "2026-05"} {
		require.NoError(t, store.SaveResult(ctx, p, []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		}))
	}

	_, history, err := BuildHistoryWindow(ctx, store, []uuid.UUID{userA}, []string{"2026-05", "2026-03"})
	require.NoError(t, err)
	require.Contains(t, history, userA.String())
	assert.Contains(t, history[userA.String()], "2026-05")
	assert.Contains(t, history[userA.String()], "2026-03")
	assert.NotContains(t, history[userA.String()], "2026-04", "off-axis period must be dropped by inAxis")
}

// referenceHistoryWindow is the pre-HEU-516 per-period logic, kept as an oracle.
func referenceHistoryWindow(t *testing.T, store QualificationHistoryStore, ids []uuid.UUID, axis []string) map[string]map[string]*uint16 {
	t.Helper()
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make(map[string]map[string]*uint16)
	for _, period := range axis {
		rows, err := store.GetByPeriod(context.Background(), period)
		require.NoError(t, err)
		for _, r := range rows {
			if _, ok := want[r.UserID]; !ok {
				continue
			}
			key := r.UserID.String()
			if out[key] == nil {
				out[key] = make(map[string]*uint16)
			}
			out[key][period] = r.Ordinal
		}
	}
	return out
}

func TestBuildHistoryWindow_Equivalence(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	ctx := context.Background()
	users := []uuid.UUID{
		mustParseUUID(t, "00000000-0000-0000-0000-000000000001"),
		mustParseUUID(t, "00000000-0000-0000-0000-000000000002"),
		mustParseUUID(t, "00000000-0000-0000-0000-000000000003"),
	}
	axis := []string{"2026-05", "2026-04", "2026-03"} // contiguous, most-recent-first
	rng := rand.New(rand.NewSource(42))               // fixed seed for reproducibility
	for _, period := range []string{"2026-03", "2026-04", "2026-05"} {
		var entries []QualificationHistoryEntry
		for _, u := range users {
			if rng.Intn(3) == 0 {
				continue // some (user, period) rows absent
			}
			if rng.Intn(4) == 0 {
				entries = append(entries, QualificationHistoryEntry{UserID: u}) // Unranked (nil rank/ordinal)
				continue
			}
			ord := uint16(rng.Intn(3) + 1)
			entries = append(entries, QualificationHistoryEntry{UserID: u, Rank: strPtr("r"), Ordinal: &ord})
		}
		require.NoError(t, store.SaveResult(ctx, period, entries))
	}

	requested := users[:2]
	_, got, err := BuildHistoryWindow(ctx, store, requested, axis)
	require.NoError(t, err)
	want := referenceHistoryWindow(t, store, requested, axis)
	assert.Equal(t, want, got, "new BuildHistoryWindow must match the old per-period logic")
}
