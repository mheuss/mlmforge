package platform

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/require"
)

// pgxReason returns the text pgx gives after the masked connection string.
func pgxReason(t *testing.T, err error) string {
	t.Helper()
	_, reason, ok := strings.Cut(err.Error(), "`: ")
	require.True(t, ok, "unexpected pgx error shape: %v", err)
	return reason
}

func TestResolveRefusedTwins_ReachTheDriverParsers(t *testing.T) {
	checked := 0
	for _, tc := range testutil.ResolveRefusedCases() {
		if tc.TwinCheck == testutil.TwinResolve {
			continue
		}
		checked++
		t.Run(tc.Name, func(t *testing.T) {
			testutil.ClearTimeoutEnv(t)
			testutil.ClearLibPQEnv(t)

			require.NoError(t, migrateDriverParseError(tc.Twin), "lib/pq")

			_, twinErr := pgconn.ParseConfig(tc.Twin)
			if tc.TwinCheck == testutil.TwinParsers {
				require.NoError(t, twinErr, "pgx")
				return
			}
			_, rowErr := pgconn.ParseConfig(tc.ConnString)
			require.Error(t, twinErr, "expected pgx to refuse the twin; it parsed without error")
			require.Error(t, rowErr)
			require.Equal(t, pgxReason(t, rowErr), pgxReason(t, twinErr))
			require.Contains(t, pgxReason(t, twinErr), "invalid port")
		})
	}
	require.Equal(t, 29, checked, "twins checked at the parsers")
}
