package period

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
