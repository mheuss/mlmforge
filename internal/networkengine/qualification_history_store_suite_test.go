package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runQualificationHistoryStoreSuite is the behavioral contract for a
// QualificationHistoryStore. newStore must return a fresh, empty store on each
// call.
func runQualificationHistoryStoreSuite(t *testing.T, newStore func(t *testing.T) QualificationHistoryStore) {
	t.Helper()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")
	userC := mustParseUUID(t, "00000000-0000-0000-0000-000000000003")
	seed := []QualificationHistoryEntry{
		{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
	}

	periodUsers := func(t *testing.T, s QualificationHistoryStore, periodID string) []uuid.UUID {
		t.Helper()
		rows, err := s.GetByPeriod(context.Background(), periodID)
		require.NoError(t, err)
		ids := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.UserID)
		}
		return ids
	}

	t.Run("ranked and unranked entries round-trip", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		userD := mustParseUUID(t, "00000000-0000-0000-0000-000000000004")
		userE := mustParseUUID(t, "00000000-0000-0000-0000-000000000005")
		userF := mustParseUUID(t, "00000000-0000-0000-0000-000000000006")
		require.NoError(t, s.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userB, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
			{UserID: userD, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)},
			{UserID: userE, Rank: strPtr("platinum"), Ordinal: u16Ptr(65535)},
			{UserID: userC},
			{UserID: userF, Rank: strPtr("diamond"), Ordinal: u16Ptr(4)},
		}))

		rows, err := s.GetByPeriod(ctx, "2026-05")
		require.NoError(t, err)
		require.Len(t, rows, 6)
		for i, want := range []struct {
			user    uuid.UUID
			rank    *string
			ordinal *uint16
		}{
			{userA, strPtr("silver"), u16Ptr(2)},
			{userB, strPtr("gold"), u16Ptr(3)},
			{userC, nil, nil},
			{userD, strPtr("bronze"), u16Ptr(1)},
			{userE, strPtr("platinum"), u16Ptr(65535)},
			{userF, strPtr("diamond"), u16Ptr(4)},
		} {
			assert.Equal(t, want.user, rows[i].UserID, "row %d", i)
			assert.Equal(t, "2026-05", rows[i].PeriodID, "row %d", i)
			assert.Equal(t, want.rank, rows[i].Rank, "row %d", i)
			assert.Equal(t, want.ordinal, rows[i].Ordinal, "row %d", i)
		}

		missing, err := s.GetByPeriod(ctx, "2026-04")
		require.NoError(t, err)
		assert.Empty(t, missing, "a period never saved returned rows")
	})

	t.Run("changing saved entries after the save leaves the stored rows alone", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		rank, ordinal := "silver", uint16(2)
		entries := []QualificationHistoryEntry{{UserID: userA, Rank: &rank, Ordinal: &ordinal}}
		require.NoError(t, s.SaveResult(ctx, "2026-05", entries))

		rank, ordinal = "gold", 9

		reads := []struct {
			name string
			read func() ([]QualificationHistoryRow, error)
		}{
			{"GetByPeriod", func() ([]QualificationHistoryRow, error) { return s.GetByPeriod(ctx, "2026-05") }},
			{"GetByUserAndPeriodRange", func() ([]QualificationHistoryRow, error) {
				return s.GetByUserAndPeriodRange(ctx, userA, "2026-05", "2026-05")
			}},
			{"GetByUsersAndPeriodRange", func() ([]QualificationHistoryRow, error) {
				return s.GetByUsersAndPeriodRange(ctx, []uuid.UUID{userA}, "2026-05", "2026-05")
			}},
		}
		for _, r := range reads {
			rows, err := r.read()
			require.NoError(t, err, r.name)
			require.Len(t, rows, 1, r.name)
			require.NotNil(t, rows[0].Rank, r.name)
			require.NotNil(t, rows[0].Ordinal, r.name)
			assert.Equal(t, "silver", *rows[0].Rank,
				"%s read rank %q after the caller changed its saved variable to %q", r.name, *rows[0].Rank, rank)
			assert.Equal(t, uint16(2), *rows[0].Ordinal,
				"%s read ordinal %d after the caller changed its saved variable to %d", r.name, *rows[0].Ordinal, ordinal)

			*rows[0].Rank, *rows[0].Ordinal = "bronze", 7

			again, err := r.read()
			require.NoError(t, err, r.name)
			require.Len(t, again, 1, r.name)
			require.NotNil(t, again[0].Rank, r.name)
			require.NotNil(t, again[0].Ordinal, r.name)
			assert.Equal(t, "silver", *again[0].Rank,
				"%s read rank %q after a returned row's rank was changed to bronze", r.name, *again[0].Rank)
			assert.Equal(t, uint16(2), *again[0].Ordinal,
				"%s read ordinal %d after a returned row's ordinal was changed to 7", r.name, *again[0].Ordinal)
		}
	})

	t.Run("one save stamps every row with one microsecond instant", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		before := time.Now().Truncate(time.Microsecond)
		require.NoError(t, s.SaveResult(ctx, "2026-05", seed))
		after := time.Now()

		rows, err := s.GetByPeriod(ctx, "2026-05")
		require.NoError(t, err)
		require.Len(t, rows, 2)
		at := rows[0].EvaluatedAt
		assert.False(t, at.Before(before) || at.After(after),
			"EvaluatedAt %s is outside the save's window [%s, %s]", at, before, after)
		assert.True(t, at.Equal(rows[1].EvaluatedAt),
			"one save stamped two instants, %s and %s", at, rows[1].EvaluatedAt)
		assert.Equal(t, at.Round(0).Truncate(time.Microsecond), at,
			"EvaluatedAt %s differs from itself after Round(0).Truncate(time.Microsecond)", at)
		assert.Equal(t, time.Local, at.Location(), "EvaluatedAt came back in location %v", at.Location())
	})

	t.Run("GetByPeriod orders rows by user id", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		userZ := mustParseUUID(t, "ffffffff-ffff-ffff-ffff-ffffffffffff")
		userM := mustParseUUID(t, "88888888-8888-8888-8888-888888888888")

		require.NoError(t, s.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
			{UserID: userZ, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userM, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		}))

		assert.Equal(t, []uuid.UUID{userA, userM, userZ}, periodUsers(t, s, "2026-05"))
	})

	t.Run("a save replaces every prior row of its period", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		require.NoError(t, s.SaveResult(ctx, "2026-05", append(seed,
			QualificationHistoryEntry{UserID: userC, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)})))
		require.NoError(t, s.SaveResult(ctx, "2026-04", seed))

		require.NoError(t, s.SaveResult(ctx, "2026-05", seed))

		assert.Equal(t, []uuid.UUID{userA, userB}, periodUsers(t, s, "2026-05"))
		assert.Equal(t, []uuid.UUID{userA, userB}, periodUsers(t, s, "2026-04"),
			"2026-04 rows after saving 2026-05 differ from the rows saved to 2026-04")
	})

	t.Run("a save with no entries empties its period", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.SaveResult(ctx, "2026-05", seed))

		require.NoError(t, s.SaveResult(ctx, "2026-05", nil))

		assert.Empty(t, periodUsers(t, s, "2026-05"))
	})

	t.Run("GetByUserAndPeriodRange reads an inclusive range in period order", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.SaveResult(ctx, "2026-03", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)},
		}))
		require.NoError(t, s.SaveResult(ctx, "2026-04", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userB, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
		}))
		require.NoError(t, s.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		}))

		rows, err := s.GetByUserAndPeriodRange(ctx, userA, "2026-03", "2026-04")
		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, "2026-03", rows[0].PeriodID)
		assert.Equal(t, strPtr("bronze"), rows[0].Rank)
		assert.Equal(t, u16Ptr(1), rows[0].Ordinal)
		assert.Equal(t, "2026-04", rows[1].PeriodID)
		assert.Equal(t, strPtr("silver"), rows[1].Rank)
		assert.Equal(t, u16Ptr(2), rows[1].Ordinal)

		inverted, err := s.GetByUserAndPeriodRange(ctx, userA, "2026-04", "2026-03")
		require.NoError(t, err)
		assert.Empty(t, inverted, "an inverted range returned rows")

		unknown, err := s.GetByUserAndPeriodRange(ctx, mustParseUUID(t, "00000000-0000-0000-0000-0000000000ff"),
			"2026-03", "2026-05")
		require.NoError(t, err)
		assert.Empty(t, unknown, "a user never saved returned rows")
	})

	t.Run("GetByUsersAndPeriodRange reads the requested users in period then user order", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		userZ := mustParseUUID(t, "ffffffff-ffff-ffff-ffff-ffffffffffff")
		userM := mustParseUUID(t, "88888888-8888-8888-8888-888888888888")
		require.NoError(t, s.SaveResult(ctx, "2026-03", []QualificationHistoryEntry{
			{UserID: userZ, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)},
			{UserID: userM, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)},
			{UserID: userB, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userA, Rank: strPtr("bronze"), Ordinal: u16Ptr(1)},
		}))
		require.NoError(t, s.SaveResult(ctx, "2026-04", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
			{UserID: userC, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		}))
		require.NoError(t, s.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		}))

		rows, err := s.GetByUsersAndPeriodRange(ctx, []uuid.UUID{userZ, userB, userM, userA}, "2026-03", "2026-04")
		require.NoError(t, err)
		type key struct {
			period string
			user   uuid.UUID
		}
		var got []key
		for _, r := range rows {
			got = append(got, key{r.PeriodID, r.UserID})
		}
		assert.Equal(t, []key{
			{"2026-03", userA}, {"2026-03", userB}, {"2026-03", userM}, {"2026-03", userZ},
			{"2026-04", userA},
		}, got)

		empty, err := s.GetByUsersAndPeriodRange(ctx, nil, "2026-03", "2026-05")
		require.NoError(t, err)
		assert.Empty(t, empty, "an empty user set returned rows")
		inverted, err := s.GetByUsersAndPeriodRange(ctx, []uuid.UUID{userA}, "2026-05", "2026-03")
		require.NoError(t, err)
		assert.Empty(t, inverted, "an inverted range returned rows")
	})

	t.Run("an empty period id is refused and writes nothing", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		err := s.SaveResult(ctx, "", seed)
		require.EqualError(t, err, "save qualification history: period_id must be non-empty")

		err = s.SaveResult(ctx, "", nil)
		require.EqualError(t, err, "save qualification history: period_id must be non-empty",
			"an empty period id with no entries")

		assert.Empty(t, periodUsers(t, s, ""))
	})

	t.Run("a save under a cancelled context is refused and leaves the prior rows", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.SaveResult(context.Background(), "2026-05", seed))
		before, err := s.GetByPeriod(context.Background(), "2026-05")
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err = s.SaveResult(ctx, "2026-05", nil)
		require.ErrorContains(t, err, "save qualification history")
		assert.ErrorIs(t, err, context.Canceled)

		after, err := s.GetByPeriod(context.Background(), "2026-05")
		require.NoError(t, err)
		assert.Equal(t, before, after, "a save under a cancelled context changed the period's rows")
	})

	refusals := []struct {
		name    string
		entries []QualificationHistoryEntry
	}{
		{"a rank with no ordinal", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userC, Rank: strPtr("bronze")},
		}},
		{"an ordinal with no rank", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userC, Ordinal: u16Ptr(1)},
		}},
		{"a user named twice", []QualificationHistoryEntry{
			{UserID: userC, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userC, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		}},
		{"an ordinal of 0", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userC, Rank: strPtr("bronze"), Ordinal: u16Ptr(0)},
		}},
	}
	for _, tc := range refusals {
		t.Run("a save with "+tc.name+" is refused and leaves the prior rows", func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			require.NoError(t, s.SaveResult(ctx, "2026-05", seed))
			before, err := s.GetByPeriod(ctx, "2026-05")
			require.NoError(t, err)

			require.ErrorContains(t, s.SaveResult(ctx, "2026-05", tc.entries), "save qualification history")

			after, err := s.GetByPeriod(ctx, "2026-05")
			require.NoError(t, err)
			assert.Equal(t, before, after, "a refused save changed the period's rows")
		})
	}

	t.Run("period ids compare as strings, so mixed widths sort lexicographically", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		for _, p := range []string{"2026-1", "2026-10", "2026-2"} {
			require.NoError(t, s.SaveResult(ctx, p, []QualificationHistoryEntry{
				{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			}))
		}

		rows, err := s.GetByUserAndPeriodRange(ctx, userA, "2026-0", "2026-9")
		require.NoError(t, err)
		var got []string
		for _, r := range rows {
			got = append(got, r.PeriodID)
		}
		assert.Equal(t, []string{"2026-1", "2026-10", "2026-2"}, got)
	})

	t.Run("BuildHistoryWindow reads the window through the store", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		other := mustParseUUID(t, "00000000-0000-0000-0000-000000000099")
		require.NoError(t, s.SaveResult(ctx, "2026-04", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("silver"), Ordinal: u16Ptr(2)},
			{UserID: userB},
			{UserID: other, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		}))
		require.NoError(t, s.SaveResult(ctx, "2026-05", []QualificationHistoryEntry{
			{UserID: userA, Rank: strPtr("gold"), Ordinal: u16Ptr(3)},
		}))

		axis, history, err := BuildHistoryWindow(ctx, s, []uuid.UUID{userA, userB}, []string{"2026-05", "2026-04"})
		require.NoError(t, err)

		assert.Equal(t, []string{"2026-05", "2026-04"}, axis)
		assert.NotContains(t, history, other.String(), "a user outside the request reached the window")
		require.NotNil(t, history[userA.String()]["2026-05"])
		assert.Equal(t, uint16(3), *history[userA.String()]["2026-05"])
		require.NotNil(t, history[userA.String()]["2026-04"])
		assert.Equal(t, uint16(2), *history[userA.String()]["2026-04"])
		val, ok := history[userB.String()]["2026-04"]
		require.True(t, ok, "an unranked evaluation is missing from the window")
		assert.Nil(t, val, "an unranked evaluation carries an ordinal")
		assert.NotContains(t, history[userB.String()], "2026-05", "a period never evaluated reached the window")
	})
}
