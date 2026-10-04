package platform

import (
	"net/url"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
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

// pqSettings returns lib/pq's keyword text as a map of key to unquoted value.
func pqSettings(t *testing.T, text string) map[string]string {
	t.Helper()
	settings := map[string]string{}
	full := len(text)
	for text != "" {
		key, rest, ok := strings.Cut(text, "='")
		require.True(t, ok, "lib/pq text has no key='value' entry at offset %d", full-len(text))
		var value strings.Builder
		i := 0
		for ; i < len(rest) && rest[i] != '\''; i++ {
			if rest[i] == '\\' && i+1 < len(rest) {
				i++
			}
			value.WriteByte(rest[i])
		}
		require.Less(t, i, len(rest), "lib/pq text has an unterminated value at offset %d", full-len(rest))
		settings[key] = value.String()
		text = strings.TrimPrefix(rest[i+1:], " ")
	}
	return settings
}

// readBoth returns pgx's reading of connString and lib/pq's settings for its FilterCustomQuery form.
func readBoth(t *testing.T, connString string) (pgxReading, map[string]string) {
	t.Helper()
	cfg, err := pgconn.ParseConfig(connString)
	require.NoError(t, err)
	u, err := url.Parse(connString)
	require.NoError(t, err)
	text, err := pq.ParseURL(migrate.FilterCustomQuery(u).String())
	require.NoError(t, err)
	return pgxReading{cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password, cfg.RuntimeParams}, pqSettings(t, text)
}

func TestResolveRefusedTwins_ReachTheSameTarget(t *testing.T) {
	checked := 0
	for _, tc := range testutil.ResolveRefusedCases() {
		if tc.SameTarget == "" {
			continue
		}
		checked++
		t.Run(tc.Name, func(t *testing.T) {
			testutil.ClearLibPQEnv(t)
			clearPgxDefaults(t)
			rowPgx, rowPQ := readBoth(t, tc.ConnString)
			twinPgx, twinPQ := readBoth(t, tc.Twin)

			if tc.SameTarget == testutil.SameReading {
				require.Equal(t, rowPgx, twinPgx)
				require.Equal(t, rowPQ, twinPQ)
				return
			}
			require.Equal(t, tc.TwinPassword, twinPgx.Password)
			require.Equal(t, tc.TwinPassword, twinPQ["password"])
			for _, key := range tc.SplitKeys {
				delete(rowPgx.RuntimeParams, key)
				delete(rowPQ, key)
			}
			rowPgx.Password, twinPgx.Password = "", ""
			delete(rowPQ, "password")
			delete(twinPQ, "password")
			require.Equal(t, rowPgx, twinPgx)
			require.Equal(t, rowPQ, twinPQ)
		})
	}
	require.Equal(t, 11, checked, "twins compared for the same target")
}
