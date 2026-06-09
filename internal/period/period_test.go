package period

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dateUTC builds a UTC midnight date. Used for expected period starts.
func dateUTC(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func TestNewSequence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		length      string
		startDate   string
		wantErr     bool
		errContains string
	}{
		{
			name:      "valid month sequence",
			length:    "month",
			startDate: "2026-01-01",
		},
		{
			name:        "empty start date",
			length:      "month",
			startDate:   "",
			wantErr:     true,
			errContains: "start_date",
		},
		{
			name:      "malformed start date",
			length:    "month",
			startDate: "2026-13-99",
			wantErr:   true,
		},
		{
			name:      "unknown length",
			length:    "yearly",
			startDate: "2026-01-01",
			wantErr:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewSequence(tc.length, tc.startDate)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errContains != "" {
					assert.Contains(t, err.Error(), tc.errContains)
				}
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, got)
		})
	}
}

func TestParseLength(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input   string
		want    Length
		wantErr bool
	}{
		{input: "week", want: Week},
		{input: "semi_month", want: SemiMonth},
		{input: "month", want: Month},
		{input: "quarter", want: Quarter},
		{input: "yearly", wantErr: true},
		{input: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLength(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPeriodStart(t *testing.T) {
	t.Parallel()

	month := &Sequence{length: Month, anchor: dateUTC(2026, time.January, 1)}
	quarter := &Sequence{length: Quarter, anchor: dateUTC(2026, time.January, 1)}
	semiMonth := &Sequence{length: SemiMonth, anchor: dateUTC(2026, time.January, 1)}
	semiMonth2024 := &Sequence{length: SemiMonth, anchor: dateUTC(2024, time.January, 1)}
	// Anchor is a Wednesday; week buckets are 7-day blocks off this grid.
	week := &Sequence{length: Week, anchor: dateUTC(2026, time.January, 7)}

	// Civil-date instants carry an offset so the UTC-shifted day differs from
	// the local day. periodStart must honor the caller's local Y/M/D (BR10).
	estMinus5 := time.FixedZone("EST", -5*60*60)
	pstMinus8 := time.FixedZone("PST", -8*60*60)

	cases := []struct {
		name string
		seq  *Sequence
		in   time.Time
		want time.Time
	}{
		// Month: calendar-aligned to the 1st.
		{name: "month mid", seq: month, in: dateUTC(2026, time.March, 15), want: dateUTC(2026, time.March, 1)},
		{name: "month on start", seq: month, in: dateUTC(2026, time.March, 1), want: dateUTC(2026, time.March, 1)},

		// Quarter: calendar-aligned to Jan/Apr/Jul/Oct 1st.
		{name: "quarter mid Q2", seq: quarter, in: dateUTC(2026, time.May, 20), want: dateUTC(2026, time.April, 1)},
		{name: "quarter on start Q1", seq: quarter, in: dateUTC(2026, time.January, 1), want: dateUTC(2026, time.January, 1)},
		{name: "quarter end of year", seq: quarter, in: dateUTC(2026, time.December, 31), want: dateUTC(2026, time.October, 1)},

		// SemiMonth: H1 is the 1st (days 1-15), H2 is the 16th (days 16-end).
		{name: "semimonth H1", seq: semiMonth, in: dateUTC(2026, time.February, 15), want: dateUTC(2026, time.February, 1)},
		{name: "semimonth H2 start", seq: semiMonth, in: dateUTC(2026, time.February, 16), want: dateUTC(2026, time.February, 16)},
		{name: "semimonth H2 non-leap end", seq: semiMonth, in: dateUTC(2026, time.February, 28), want: dateUTC(2026, time.February, 16)},
		{name: "semimonth H2 leap end", seq: semiMonth2024, in: dateUTC(2024, time.February, 29), want: dateUTC(2024, time.February, 16)},

		// Week: 7-day buckets off the anchor grid.
		{name: "week on anchor", seq: week, in: dateUTC(2026, time.January, 7), want: dateUTC(2026, time.January, 7)},
		{name: "week within first bucket", seq: week, in: dateUTC(2026, time.January, 13), want: dateUTC(2026, time.January, 7)},
		{name: "week next bucket start", seq: week, in: dateUTC(2026, time.January, 14), want: dateUTC(2026, time.January, 14)},
		{name: "week pre-anchor", seq: week, in: dateUTC(2026, time.January, 6), want: dateUTC(2025, time.December, 31)},

		// Civil-date: caller's local day wins over the UTC-shifted instant.
		{
			name: "semimonth civil date stays H1",
			seq:  semiMonth,
			in:   time.Date(2026, time.May, 15, 23, 30, 0, 0, estMinus5), // UTC: May 16
			want: dateUTC(2026, time.May, 1),
		},
		{
			name: "month civil date stays March",
			seq:  month,
			in:   time.Date(2026, time.March, 31, 20, 0, 0, 0, pstMinus8), // UTC: April 1
			want: dateUTC(2026, time.March, 1),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.seq.periodStart(tc.in)
			assert.True(t, got.Equal(tc.want),
				"periodStart(%s) = %s, want %s", tc.in, got, tc.want)
		})
	}
}

func TestAdvance(t *testing.T) {
	t.Parallel()

	month := &Sequence{length: Month, anchor: dateUTC(2026, time.January, 1)}
	quarter := &Sequence{length: Quarter, anchor: dateUTC(2026, time.January, 1)}
	semiMonth := &Sequence{length: SemiMonth, anchor: dateUTC(2026, time.January, 1)}
	// Anchor is a Wednesday; week buckets are 7-day blocks off this grid.
	week := &Sequence{length: Week, anchor: dateUTC(2026, time.January, 7)}

	cases := []struct {
		name  string
		seq   *Sequence
		start time.Time
		n     int
		want  time.Time
	}{
		// Month: step whole calendar months, including year rollover.
		{name: "month +1 across year", seq: month, start: dateUTC(2026, time.December, 1), n: 1, want: dateUTC(2027, time.January, 1)},
		{name: "month -1 across year", seq: month, start: dateUTC(2026, time.January, 1), n: -1, want: dateUTC(2025, time.December, 1)},

		// Quarter: step 3 months at a time.
		{name: "quarter +1 across year", seq: quarter, start: dateUTC(2026, time.October, 1), n: 1, want: dateUTC(2027, time.January, 1)},
		{name: "quarter -1 across year", seq: quarter, start: dateUTC(2026, time.January, 1), n: -1, want: dateUTC(2025, time.October, 1)},

		// SemiMonth: H1 (1st) and H2 (16th) alternate.
		{name: "semimonth H1 +1", seq: semiMonth, start: dateUTC(2026, time.May, 1), n: 1, want: dateUTC(2026, time.May, 16)},
		{name: "semimonth H2 +1 rolls month", seq: semiMonth, start: dateUTC(2026, time.May, 16), n: 1, want: dateUTC(2026, time.June, 1)},
		{name: "semimonth H1 +2 rolls month", seq: semiMonth, start: dateUTC(2026, time.May, 1), n: 2, want: dateUTC(2026, time.June, 1)},
		{name: "semimonth H1 -1 rolls month", seq: semiMonth, start: dateUTC(2026, time.May, 1), n: -1, want: dateUTC(2026, time.April, 16)},
		{name: "semimonth H2 -1", seq: semiMonth, start: dateUTC(2026, time.May, 16), n: -1, want: dateUTC(2026, time.May, 1)},

		// Week: 7-day steps off the anchor grid, including year rollover.
		{name: "week +1", seq: week, start: dateUTC(2026, time.January, 7), n: 1, want: dateUTC(2026, time.January, 14)},
		{name: "week -1 across year", seq: week, start: dateUTC(2026, time.January, 7), n: -1, want: dateUTC(2025, time.December, 31)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.seq.advance(tc.start, tc.n)
			assert.True(t, got.Equal(tc.want),
				"advance(%s, %d) = %s, want %s", tc.start, tc.n, got, tc.want)
		})
	}
}

func TestLabel(t *testing.T) {
	t.Parallel()

	month := &Sequence{length: Month, anchor: dateUTC(2026, time.January, 1)}
	quarter := &Sequence{length: Quarter, anchor: dateUTC(2026, time.January, 1)}
	semiMonth := &Sequence{length: SemiMonth, anchor: dateUTC(2026, time.January, 1)}
	week := &Sequence{length: Week, anchor: dateUTC(2026, time.January, 7)}

	// Compute the expected ISO week label for 2026-01-07 using ISOWeek.
	wantISOYear, wantISOWeek := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC).ISOWeek()

	cases := []struct {
		name string
		seq  *Sequence
		in   time.Time
		want string
	}{
		// Month
		{name: "month mid", seq: month, in: dateUTC(2026, time.May, 10), want: "2026-05"},

		// Quarter
		{name: "quarter Q2", seq: quarter, in: dateUTC(2026, time.May, 10), want: "2026-Q2"},
		{name: "quarter Q1", seq: quarter, in: dateUTC(2026, time.January, 1), want: "2026-Q1"},
		{name: "quarter Q4", seq: quarter, in: dateUTC(2026, time.December, 31), want: "2026-Q4"},

		// SemiMonth: H1 is days 1-15, H2 is days 16-end
		{name: "semimonth H1", seq: semiMonth, in: dateUTC(2026, time.May, 10), want: "2026-05-H1"},
		{name: "semimonth H2 at boundary", seq: semiMonth, in: dateUTC(2026, time.May, 16), want: "2026-05-H2"},
		{name: "semimonth H2 mid", seq: semiMonth, in: dateUTC(2026, time.May, 20), want: "2026-05-H2"},

		// Week: ISO week-year format
		{
			name: "week on anchor",
			seq:  week,
			in:   dateUTC(2026, time.January, 7),
			want: fmt.Sprintf("%04d-W%02d", wantISOYear, wantISOWeek),
		},
		// 2025-12-31 falls in the period starting 2025-12-31; that date's
		// ISOWeek() returns year 2026 (it is the Wednesday of 2026-W01).
		// A regression to start.Year() would produce "2025-W01" instead.
		{
			name: "week ISO year boundary",
			seq:  week,
			in:   dateUTC(2025, time.December, 31),
			want: "2026-W01",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.seq.Label(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLabelSortable(t *testing.T) {
	t.Parallel()

	// Helper: build n consecutive labels starting from the period containing start.
	labels := func(seq *Sequence, start time.Time, n int) []string {
		s := seq.periodStart(start)
		out := make([]string, n)
		for i := range out {
			out[i] = seq.Label(seq.advance(s, i))
		}
		return out
	}

	assertSorted := func(t *testing.T, got []string) {
		t.Helper()
		cp := make([]string, len(got))
		copy(cp, got)
		sort.Strings(cp)
		assert.Equal(t, got, cp, "labels are not lexicographically sortable in chronological order")
	}

	t.Run("month across year boundary", func(t *testing.T) {
		t.Parallel()
		seq := &Sequence{length: Month, anchor: dateUTC(2025, time.December, 1)}
		got := labels(seq, dateUTC(2025, time.December, 1), 14)
		assertSorted(t, got)
	})

	t.Run("week across year boundary", func(t *testing.T) {
		t.Parallel()
		seq := &Sequence{length: Week, anchor: dateUTC(2025, time.December, 29)}
		got := labels(seq, dateUTC(2025, time.December, 29), 8)
		assertSorted(t, got)
	})

	t.Run("quarter across year boundary", func(t *testing.T) {
		t.Parallel()
		seq := &Sequence{length: Quarter, anchor: dateUTC(2025, time.October, 1)}
		got := labels(seq, dateUTC(2025, time.October, 1), 6)
		assertSorted(t, got)
	})

	t.Run("semimonth across month and year boundary", func(t *testing.T) {
		t.Parallel()
		seq := &Sequence{length: SemiMonth, anchor: dateUTC(2025, time.December, 1)}
		got := labels(seq, dateUTC(2025, time.December, 1), 6)
		assertSorted(t, got)
	})
}

func TestPriorLabels(t *testing.T) {
	t.Parallel()

	month := &Sequence{length: Month, anchor: dateUTC(2026, time.January, 1)}

	t.Run("three prior months most-recent-first", func(t *testing.T) {
		t.Parallel()
		// June 10 is in 2026-06; prior 3 must be 2026-05, 2026-04, 2026-03 (never 2026-06).
		got := month.PriorLabels(dateUTC(2026, time.June, 10), 3)
		assert.Equal(t, []string{"2026-05", "2026-04", "2026-03"}, got)
	})

	t.Run("n=0 returns nil", func(t *testing.T) {
		t.Parallel()
		got := month.PriorLabels(dateUTC(2026, time.June, 10), 0)
		assert.Nil(t, got)
	})

	t.Run("n=-1 returns nil", func(t *testing.T) {
		t.Parallel()
		got := month.PriorLabels(dateUTC(2026, time.June, 10), -1)
		assert.Nil(t, got)
	})

	t.Run("year crossing", func(t *testing.T) {
		t.Parallel()
		// Jan 10 is in 2026-01; prior 2 must cross into 2025.
		got := month.PriorLabels(dateUTC(2026, time.January, 10), 2)
		assert.Equal(t, []string{"2025-12", "2025-11"}, got)
	})

	t.Run("pre-anchor periods produce labels", func(t *testing.T) {
		t.Parallel()
		// Anchor is 2026-06-01; June 10 is in 2026-06 (post-anchor).
		// Prior 3 periods are 2026-05, 2026-04, 2026-03 — all pre-anchor.
		// Labels must still be produced (they simply carry no history later).
		preAnchor := &Sequence{length: Month, anchor: dateUTC(2026, time.June, 1)}
		got := preAnchor.PriorLabels(dateUTC(2026, time.June, 10), 3)
		assert.Equal(t, []string{"2026-05", "2026-04", "2026-03"}, got)
	})
}

func TestLabelDateOnly(t *testing.T) {
	t.Parallel()

	semiMonth := &Sequence{length: SemiMonth, anchor: dateUTC(2026, time.January, 1)}

	// 23:30 on May 15 in UTC-5 → UTC is May 16 (which would be H2), but the
	// caller's civil date is still May 15, so the label must be H1.
	estMinus5 := time.FixedZone("EST", -5*60*60)
	in1 := time.Date(2026, time.May, 15, 23, 30, 0, 0, estMinus5)
	assert.Equal(t, "2026-05-H1", semiMonth.Label(in1),
		"label must use caller's civil date (May 15 in EST), not UTC-shifted May 16")

	// Two instants with the same civil date but different UTC dates must yield the
	// same label. inA is May 10 23:00 at -05:00 (UTC: May 11); inB is May 10 01:00
	// at -08:00 (UTC: May 10). Both must map to "2026-05-H1".
	pstMinus8 := time.FixedZone("PST", -8*60*60)
	inA := time.Date(2026, time.May, 10, 23, 0, 0, 0, estMinus5) // civil May 10, UTC May 11
	inB := time.Date(2026, time.May, 10, 1, 0, 0, 0, pstMinus8)  // civil May 10, UTC May 10
	assert.Equal(t, semiMonth.Label(inA), semiMonth.Label(inB),
		"same civil date in different locations must yield the same label")
}
