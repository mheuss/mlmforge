package period

import (
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
