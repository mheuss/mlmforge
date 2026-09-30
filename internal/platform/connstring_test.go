package platform

import (
	"errors"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestConnStringError_NamesTheDriverAndTheStage(t *testing.T) {
	for _, tc := range []struct {
		err    *ConnStringError
		driver string
		stage  string
	}{
		{&ConnStringError{driver: driverMigrate, stage: stageParse}, "golang-migrate", "parse"},
		{&ConnStringError{driver: driverPgx, stage: stageParse}, "pgx", "parse"},
		{&ConnStringError{driver: driverPgx, stage: stageRefused}, "pgx", "refused"},
		{&ConnStringError{driver: driverMigrate, stage: stageScheme}, "golang-migrate", "scheme"},
		{&ConnStringError{driver: driverMigrate, stage: stageRefused}, "golang-migrate", "refused"},
	} {
		t.Run(tc.driver+"/"+tc.stage, func(t *testing.T) {
			require.Equal(t, testutil.RefusalText(t, tc.driver, tc.stage), tc.err.Error())
			require.Equal(t, tc.driver, tc.err.Driver())
			require.Equal(t, tc.stage, tc.err.Stage())
		})
	}
}

func TestConnStringError_EndsTheChain(t *testing.T) {
	require.Nil(t, errors.Unwrap(&ConnStringError{driver: driverPgx, stage: stageRefused}))
}

func TestPgxConnStringError_ReplacesEachRefusal(t *testing.T) {
	for _, tc := range testutil.ConnStringCases() {
		if tc.PgxStage == "" {
			t.Run(tc.Name, func(t *testing.T) {
				testutil.IsolatePgxEnv(t)
				_, err := pgxpool.ParseConfig(tc.ConnString)
				require.NoError(t, err, "a case with no pgx stage expects pgx to accept it")
			})
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			testutil.IsolatePgxEnv(t)
			_, parseErr := pgxpool.ParseConfig(tc.ConnString)
			require.Error(t, parseErr)

			err := PgxConnStringError(parseErr)

			var cse *ConnStringError
			require.ErrorAs(t, err, &cse)
			require.Equal(t, "pgx", cse.Driver())
			require.Equal(t, tc.PgxStage, cse.Stage())
			testutil.RequireNoDriverParseError(t, err)
			testutil.RequireNoPasswordWindow(t, err.Error(), tc.Password, tc.WithoutPassword())
		})
	}
}

func TestPgxConnStringError_PassesOtherErrorsThrough(t *testing.T) {
	other := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")

	require.Same(t, other, PgxConnStringError(other))
	require.NoError(t, PgxConnStringError(nil))
}

func TestMigrateConnStringError_ReplacesAParseError(t *testing.T) {
	parseErr := &url.Error{Op: "parse", URL: "postgres://app:Zm9vQmFy/cXV4eHl6@h/app", Err: errors.New("invalid port")}

	err := migrateConnStringError(parseErr)

	var cse *ConnStringError
	require.ErrorAs(t, err, &cse)
	require.Equal(t, "golang-migrate", cse.Driver())
	require.Equal(t, "parse", cse.Stage())
	testutil.RequireNoDriverParseError(t, err)
}

func TestMigrateConnStringError_PassesOtherErrorsThrough(t *testing.T) {
	notParse := &url.Error{Op: "Get", URL: "http://h", Err: errors.New("boom")}
	other := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")

	require.Same(t, notParse, migrateConnStringError(notParse))
	require.Same(t, other, migrateConnStringError(other))
	require.NoError(t, migrateConnStringError(nil))
}

func TestMigrateSchemeError_RefusesAnythingButAPostgresURL(t *testing.T) {
	for _, refused := range []string{
		"host=h password=x",
		"password=x host=::1 dbname=app",
		"postgres:app@h/app",
		"postgresql:app@h/app",
		"POSTGRES://h/app",
		"mysql://h/app",
		":x",
		"",
	} {
		var cse *ConnStringError
		require.ErrorAs(t, migrateSchemeError(refused), &cse, refused)
		require.Equal(t, "scheme", cse.Stage(), refused)
	}
	for _, accepted := range []string{"postgres://h/app", "postgresql://h/app"} {
		require.NoError(t, migrateSchemeError(accepted), accepted)
	}
}

func TestMigrateDriverParseError_RefusesWhatLibPQCannotParse(t *testing.T) {
	for _, tc := range testutil.ConnStringCases() {
		if tc.MigrateStage != "refused" {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			var cse *ConnStringError
			require.ErrorAs(t, migrateDriverParseError(tc.ConnString), &cse)
			require.Equal(t, "refused", cse.Stage())
		})
	}
	for _, accepted := range []string{
		"postgres://app@127.0.0.1:1/app?sslmode=disable",
		"postgres://app@127.0.0.1:1/app?password=x&sslmode=bogus",
		"postgres://app:Zm9vQmFy/cXV4eHl6@127.0.0.1:1/app",
	} {
		require.NoError(t, migrateDriverParseError(accepted), accepted)
	}
}
