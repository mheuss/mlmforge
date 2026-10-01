package platform

import (
	"net/url"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/require"
)

// pgxReading is the part of a pgx config a URL split decides.
type pgxReading struct {
	Host          string
	Port          uint16
	Database      string
	User          string
	Password      string
	RuntimeParams map[string]string
}

// clearPgxDefaults stops the environment from filling in what a URL left out, for one test.
func clearPgxDefaults(t *testing.T) {
	t.Helper()
	testutil.ClearTimeoutEnv(t)
	for _, name := range []string{
		"PGHOST", "PGPORT", "PGDATABASE", "PGPASSWORD", "PGAPPNAME", "PGCONNECT_TIMEOUT",
		"PGSSLMODE", "PGSSLKEY", "PGSSLCERT", "PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION",
		"PGTARGETSESSIONATTRS", "PGTZ", "PGOPTIONS", "PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("PGUSER", "envuser")
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "none"))
}

func TestDriverReadings_SplitWhereRecorded(t *testing.T) {
	for _, tc := range []struct {
		connString string
		pgx        pgxReading
		pq         string
	}{
		{"postgres://app:pa@ss@127.0.0.1:5432/app",
			pgxReading{"127.0.0.1", 5432, "app", "app", "pa@ss", map[string]string{}},
			"dbname='app' host='127.0.0.1' password='pa@ss' port='5432' user='app'"},
		{"postgres://app:Zm9vQmFy@cXV4eHl6/d2l0aA@127.0.0.1:1/app",
			pgxReading{"cXV4eHl6", 5432, "d2l0aA@127.0.0.1:1/app", "app", "Zm9vQmFy", map[string]string{}},
			"dbname='d2l0aA@127.0.0.1:1/app' host='cXV4eHl6' password='Zm9vQmFy' user='app'"},
		{"postgres://app:1234/s3cretPW@127.0.0.1:1/app",
			pgxReading{"app", 1234, "s3cretPW@127.0.0.1:1/app", "envuser", "", map[string]string{}},
			"dbname='s3cretPW@127.0.0.1:1/app' host='app' port='1234'"},
		{"postgres://app:1234#s3cretPW@127.0.0.1:1/app",
			pgxReading{"app", 1234, "", "envuser", "", map[string]string{}},
			"host='app' port='1234'"},
		{"postgres://app:Zm9v@h?binary_parameters=s3cretPW@127.0.0.1:1/app",
			pgxReading{"h", 5432, "", "app", "Zm9v", map[string]string{"binary_parameters": "s3cretPW@127.0.0.1:1/app"}},
			"binary_parameters='s3cretPW@127.0.0.1:1/app' host='h' password='Zm9v' user='app'"},
		{"postgres://proofuser:pr@of@PW@127.0.0.1:32770/proofdb?sslmode=disable", // gitleaks:allow
			pgxReading{"127.0.0.1", 32770, "proofdb", "proofuser", "pr@of@PW", map[string]string{}},
			"dbname='proofdb' host='127.0.0.1' password='pr@of@PW' port='32770' sslmode='disable' user='proofuser'"},
		{"postgres://proofuser:pr%40of%40PW@127.0.0.1:32770/proofdb?sslmode=disable",
			pgxReading{"127.0.0.1", 32770, "proofdb", "proofuser", "pr@of@PW", map[string]string{}},
			"dbname='proofdb' host='127.0.0.1' password='pr@of@PW' port='32770' sslmode='disable' user='proofuser'"},
		{"postgres://proofuser:wrong@PW@127.0.0.1:32770/proofdb?sslmode=disable", // gitleaks:allow
			pgxReading{"127.0.0.1", 32770, "proofdb", "proofuser", "wrong@PW", map[string]string{}},
			"dbname='proofdb' host='127.0.0.1' password='wrong@PW' port='32770' sslmode='disable' user='proofuser'"},
		{"postgres://proofuser@127.0.0.1:32770/proofdb?password=pr@of@PW&sslmode=disable",
			pgxReading{"127.0.0.1", 32770, "proofdb", "proofuser", "pr@of@PW", map[string]string{}},
			"dbname='proofdb' host='127.0.0.1' password='pr@of@PW' port='32770' sslmode='disable' user='proofuser'"},
		{"postgres://proofuser:pr%40of%40PW@127.0.0.1:32770/my@db?sslmode=disable",
			pgxReading{"127.0.0.1", 32770, "my@db", "proofuser", "pr@of@PW", map[string]string{}},
			"dbname='my@db' host='127.0.0.1' password='pr@of@PW' port='32770' sslmode='disable' user='proofuser'"},
	} {
		t.Run(tc.connString, func(t *testing.T) {
			testutil.ClearLibPQEnv(t)
			clearPgxDefaults(t)

			cfg, err := pgconn.ParseConfig(tc.connString)
			require.NoError(t, err)
			require.Equal(t, tc.pgx, pgxReading{cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password, cfg.RuntimeParams})

			u, err := url.Parse(tc.connString)
			require.NoError(t, err)
			got, err := pq.ParseURL(migrate.FilterCustomQuery(u).String())
			require.NoError(t, err)
			require.Equal(t, tc.pq, got)
		})
	}
}

func TestDriverReadings_AnAlphabeticPortBeforeAQueryFailsToParse(t *testing.T) {
	testutil.ClearLibPQEnv(t)
	clearPgxDefaults(t)
	connString := "postgres://app:abcd?s3cretPW@127.0.0.1:1/app"

	_, pgxErr := pgconn.ParseConfig(connString)
	_, urlErr := url.Parse(connString)

	require.ErrorContains(t, pgxErr, `invalid port ":abcd" after host`)
	require.ErrorContains(t, urlErr, `invalid port ":abcd" after host`)
}

func TestDriverReadings_AnOutOfRangePortIsRefusedByPgxAndSplitByLibPQ(t *testing.T) {
	testutil.ClearLibPQEnv(t)
	clearPgxDefaults(t)
	connString := "postgres://app:123456/s3cretPW@127.0.0.1:1/app"

	_, pgxErr := pgconn.ParseConfig(connString)
	require.ErrorContains(t, pgxErr, `invalid port (strconv.ParseUint: parsing "123456": value out of range)`)

	u, err := url.Parse(connString)
	require.NoError(t, err)
	got, err := pq.ParseURL(migrate.FilterCustomQuery(u).String())
	require.NoError(t, err)
	require.Equal(t, "dbname='s3cretPW@127.0.0.1:1/app' host='app' port='123456'", got)
}

func TestDriverReadings_PgxReadsAMisCasedSchemeAsKeywordValueText(t *testing.T) {
	testutil.ClearLibPQEnv(t)
	clearPgxDefaults(t)

	for _, refused := range []string{
		"POSTGRES://app:1234/s3cretPW@127.0.0.1:1/app",
		"Postgres://app:Zm9vQmFy@cXV4eHl6/d2l0aA@127.0.0.1:1/app",
	} {
		_, err := pgconn.ParseConfig(refused)
		require.ErrorContains(t, err, "failed to parse as keyword/value", refused)
	}

	cfg, err := pgconn.ParseConfig("POSTGRESQL://app:Zm9v@h?binary_parameters=s3cretPW@127.0.0.1:1/app")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"POSTGRESQL://app:Zm9v@h?binary_parameters": "s3cretPW@127.0.0.1:1/app"}, cfg.RuntimeParams)
}
