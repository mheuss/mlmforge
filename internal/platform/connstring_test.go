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

func TestConnStringError_NamesThePreDriverStageAndPart(t *testing.T) {
	for _, tc := range []struct {
		err   *ConnStringError
		stage string
		part  string
	}{
		{&ConnStringError{driver: driverMlmforge, stage: stageRawAt, part: partPath}, "raw-at", "path"},
		{&ConnStringError{driver: driverMlmforge, stage: stageRawAt, part: partQuery}, "raw-at", "query"},
		{&ConnStringError{driver: driverMlmforge, stage: stageRawAt, part: partFragment}, "raw-at", "fragment"},
		{&ConnStringError{driver: driverMlmforge, stage: stageSchemeCase}, "scheme-case", ""},
	} {
		t.Run(tc.stage+"/"+tc.part, func(t *testing.T) {
			require.Equal(t, testutil.PreDriverText(t, tc.stage, tc.part), tc.err.Error())
			require.Equal(t, "mlmforge", tc.err.Driver())
			require.Equal(t, tc.stage, tc.err.Stage())
			require.Equal(t, tc.part, tc.err.Part())
		})
	}
}

func TestConnStringError_HasNoPartForTheDriverStages(t *testing.T) {
	for _, err := range []*ConnStringError{
		{driver: driverMigrate, stage: stageParse},
		{driver: driverMigrate, stage: stageScheme},
		{driver: driverMigrate, stage: stageRefused},
		{driver: driverPgx, stage: stageParse},
		{driver: driverPgx, stage: stageRefused},
	} {
		require.Empty(t, err.Part(), "%s/%s", err.Driver(), err.Stage())
	}
}

func TestConnStringError_EndsTheChain(t *testing.T) {
	require.Nil(t, errors.Unwrap(&ConnStringError{driver: driverPgx, stage: stageRefused}))
}

func TestPgxConnStringError_ReplacesEachRefusal(t *testing.T) {
	accepted := 0
	for _, tc := range testutil.ConnStringCases() {
		if tc.PgxStage == "" {
			accepted++
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
	require.Equal(t, 3, accepted, "cases pgx accepts")
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
	testutil.ClearLibPQEnv(t)
	selected := 0
	for _, tc := range testutil.ConnStringCases() {
		if tc.Name != "pq-quoted-key" && tc.Name != "pq-spaced-key" {
			continue
		}
		selected++
		t.Run(tc.Name, func(t *testing.T) {
			var cse *ConnStringError
			require.ErrorAs(t, migrateDriverParseError(tc.ConnString), &cse)
			require.Equal(t, "refused", cse.Stage())
		})
	}
	require.Equal(t, 2, selected, "lib/pq cases selected")
	for _, accepted := range []string{
		"postgres://app@127.0.0.1:1/app?sslmode=disable",
		"postgres://app@127.0.0.1:1/app?password=x&sslmode=bogus",
		"postgres://app:Zm9vQmFy/cXV4eHl6@127.0.0.1:1/app",
	} {
		require.NoError(t, migrateDriverParseError(accepted), accepted)
	}
}

func TestMigrateDriverParseError_ReportsARefusedEnvironmentAsItself(t *testing.T) {
	testutil.ClearLibPQEnv(t)
	t.Setenv("PGCLIENTENCODING", "LATIN1")

	err := migrateDriverParseError("postgres://app:pq3cretpwXYZ@127.0.0.1:1/app?p%3D%27a=z%3D")

	require.EqualError(t, err, "client_encoding must be absent or 'UTF8'")
	var cse *ConnStringError
	require.False(t, errors.As(err, &cse), "expected lib/pq's environment error; got a ConnStringError")
}

func TestRawAtError_NamesThePartThatHoldsTheAt(t *testing.T) {
	for _, tc := range []struct {
		connString string
		part       string
	}{
		{"postgres://app:Zm9vQmFy@cXV4eHl6/d2l0aA@127.0.0.1:1/app", "path"},
		{"postgresql://h/my@db", "path"},
		{"postgres://h/a@b?c@d#e@f", "path"},
		{"postgres://h/app?password=p@ss", "query"},
		{"postgres://h/db?x=a/b@c", "query"},
		{"postgres://h/app?x=a@b#c@d", "query"},
		{"postgres://h?x=1#a@b", "fragment"},
		{"postgres://h/d#x?y@z", "fragment"},
		{"postgres://h?x=1#a/b@c", "fragment"},
	} {
		t.Run(tc.connString, func(t *testing.T) {
			var cse *ConnStringError
			require.ErrorAs(t, rawAtError(tc.connString), &cse)
			require.Equal(t, "mlmforge", cse.Driver())
			require.Equal(t, "raw-at", cse.Stage())
			require.Equal(t, tc.part, cse.Part())
		})
	}
}

func TestRawAtError_AcceptsAnAtOnlyInTheHostPart(t *testing.T) {
	for _, connString := range []string{
		"postgres://app:pr@of@PW@127.0.0.1:1/app",
		"postgres://app:Zm9vQmFy%40cXV4eHl6%2Fd2l0aA@127.0.0.1:1/app",
		"postgres://h/my%40db?password=p%40ss#x",
		"postgresql://h/app",
		"host=h password=p@ss",
		"POSTGRES://app:1234/s3cretPW@127.0.0.1:1/app",
		"mysql://h/a@b",
		"",
	} {
		require.NoError(t, rawAtError(connString), connString)
	}
}
