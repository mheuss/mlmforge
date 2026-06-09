package period

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
