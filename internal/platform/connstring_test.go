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
		{&ConnStringError{driver: driverMlmforge, stage: stageKeywordKey}, "keyword-key", ""},
		{&ConnStringError{driver: driverMlmforge, stage: stageAfterPassword}, "after-password", ""},
		{&ConnStringError{driver: driverMlmforge, stage: stageRawPlus}, "raw-plus", ""},
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
		{"postgres://app:pa?ss@127.0.0.1:1/app", "query"},
		{"postgres://h?x=1#a@b", "fragment"},
		{"postgres://h/d#x?y@z", "fragment"},
		{"postgres://h?x=1#a/b@c", "fragment"},
		{"postgres://app:pa#ss@127.0.0.1:1/app", "fragment"},
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

func TestSchemeCaseError_RefusesAMisCasedScheme(t *testing.T) {
	for _, connString := range []string{
		"POSTGRES://h/app",
		"Postgres://h/app",
		"POSTGRESQL://h/app",
		"postgresQL://h/app",
		"postGres://h/app",
		"PostgreSQL://u.invalid/app",
	} {
		var cse *ConnStringError
		require.ErrorAs(t, schemeCaseError(connString), &cse, connString)
		require.Equal(t, "mlmforge", cse.Driver(), connString)
		require.Equal(t, "scheme-case", cse.Stage(), connString)
		require.Empty(t, cse.Part(), connString)
	}
}

func TestSchemeCaseError_AcceptsEverythingElse(t *testing.T) {
	for _, connString := range []string{
		"postgres://h/app",
		"postgresql://h/app",
		"host=h password=x",
		"mysql://h/app",
		"postgres:app@h/app",
		"POSTGRES:",
		"postgres",
		"",
	} {
		require.NoError(t, schemeCaseError(connString), connString)
	}
}

func TestKeywordKeyError_RefusesAFirstKeyNoParameterNameCanHold(t *testing.T) {
	for _, connString := range []string{
		" postgres://app:Zm9v@h?binary_parameters=s3cretPW@127.0.0.1:1/app",
		" POSTGRESQL://app:Zm9v@h?binary_parameters=s3cretPW@127.0.0.1:1/app",
		" postgres://proofuser:pr0ofPWxyz@127.0.0.1:1/proofdb?sslmode=disable",
		"\tpostgres://u:pw@h/db?sslmode=disable",
		"\npostgres://u:pw@h/db?sslmode=disable",
		"\"postgres://u:pw@h/db?sslmode=disable\"",
		"\ufeffpostgres://u:pw@h/db?sslmode=disable",
		"postgres:app:Zm9vQmFy@127.0.0.1:1/app?sslmode=disable",
		"my-key=1 host=h",
		"a.b-c=1 host=h",
		"a.b:c=1 host=h",
		"a.b/c=1 host=h",
		"my key=1 host=h",
		"a\tb=1 host=h",
		"a\nb=1 host=h",
		"a\"b=1 host=h",
		"_PQ_.a-b=1 host=h",
		"_Pq_.a-b=1 host=h",
	} {
		t.Run(connString, func(t *testing.T) {
			var cse *ConnStringError
			require.ErrorAs(t, keywordKeyError(connString), &cse)
			require.Equal(t, "mlmforge", cse.Driver())
			require.Equal(t, "keyword-key", cse.Stage())
			require.Empty(t, cse.Part())
		})
	}
}

func TestKeywordKeyError_AcceptsAKeyWithNoRefusedASCIIByte(t *testing.T) {
	for _, connString := range []string{
		"host=h port=1",
		" host=h dbname=app",
		"\rhost=h dbname=app",
		"\vhost=h dbname=app",
		"\fhost=h dbname=app",
		"\thost=h dbname=app",
		"\nhost=h dbname=app",
		"host =h",
		"host\t=h",
		"Application_Name=x host=h",
		"a$b.c=1 host=h",
		"a.b$c=1 host=h",
		"é.x=1 host=h",
		"a.é=1 host=h",
		"éx=1 host=h",
		"a.1b=1 host=h",
		"_pq_.a-b=1 host=h",
		"_pq_.x=1 host=h",
		"postgres://u:pw@h/db?x=a b",
		"postgresql://h/app?x=y",
		"postgres:app:pw@h/app",
		"=x",
		"  =x",
		"",
	} {
		require.NoError(t, keywordKeyError(connString), connString)
	}
}

func TestAfterPasswordError_RefusesAnAmpersandAfterThePassword(t *testing.T) {
	for _, connString := range []string{
		"postgres://amp2@127.0.0.1:32770/proofdb?password=Zm9v&cXV4eHl6&sslmode=disable",
		"postgres://h/app?password=x&sslmode=disable",
		"postgres://h/app?pass%77ord=x&sslmode=disable",
		"postgres://h/app?password=a&password=b",
		"postgres://h/app?password=x&",
		"postgres://h/app?password=x&#frag",
		"postgres://h/app?password=x&&",
		"postgresql://h/app?password=x&a.b",
		"postgres://h/app?password=x&c;d",
		"postgres://h/app?+password=a&b=c",
		"postgres://h/app?password+=a&b=c",
		"postgres://h/app?%09password=a&b=c",
		"postgres://h/app?password%0A=a&b=c",
		"postgres://h/app?%0Bpassword=a&b=c",
		"postgres://h/app?password%0C=a&b=c",
		"postgres://h/app?%C2%A0password=a&b=c",
		"postgres://h/app?PASSWORD=x&y",
		"postgres://h/app?Password=x&y",
	} {
		t.Run(connString, func(t *testing.T) {
			var cse *ConnStringError
			require.ErrorAs(t, afterPasswordError(connString), &cse)
			require.Equal(t, "mlmforge", cse.Driver())
			require.Equal(t, "after-password", cse.Stage())
			require.Empty(t, cse.Part())
		})
	}
}

func TestAfterPasswordError_AcceptsThePasswordLast(t *testing.T) {
	for _, connString := range []string{
		"postgres://h/app?sslmode=disable&password=x",
		"postgres://h/app?password=x",
		"postgres://h/app?password=x#a&b",
		"postgres://h/d#x?password=a&b",
		"postgres://h/app?&password=x",
		"postgres://h/app?sslmode=disable",
		"postgres://h/app",
		"postgres://h/app#password=x&y",
		"postgres://u:pa&ss@h/app?sslmode=disable",
		"postgres://h/app?pass%zzword=x&y",
		"host=h password=x sslmode=disable",
		"POSTGRES://h/app?password=x&y",
		"",
	} {
		require.NoError(t, afterPasswordError(connString), connString)
	}
}

func TestRawPlusError_RefusesARawPlusInThePassword(t *testing.T) {
	for _, connString := range []string{
		"postgres://plusr@127.0.0.1:1/app?password=Zm9v+cXV4",
		"postgres://plus@127.0.0.1:1/app?password=Zm9v+cXV4",
		"postgres://h/app?sslmode=disable&password=a+b",
		"postgres://h/app?pass%77ord=a+b",
		"postgres://h/app?password=+",
		"postgresql://h/app?password=a+b",
		"postgres://h/app?+password=a+b",
		"postgres://h/app?password+=a+b",
		"postgres://h/app?%09password=a+b",
		"postgres://h/app?password%0A=a+b",
		"postgres://h/app?%0Bpassword=a+b",
		"postgres://h/app?password%0C=a+b",
		"postgres://h/app?%C2%A0password=a+b",
		"postgres://h/app?PASSWORD=a+b",
		"postgres://h/app?Password=a+b",
	} {
		t.Run(connString, func(t *testing.T) {
			var cse *ConnStringError
			require.ErrorAs(t, rawPlusError(connString), &cse)
			require.Equal(t, "mlmforge", cse.Driver())
			require.Equal(t, "raw-plus", cse.Stage())
			require.Empty(t, cse.Part())
		})
	}
}

func TestRawPlusError_AcceptsAnEncodedPlusAndAPlusElsewhere(t *testing.T) {
	for _, connString := range []string{
		"postgres://plusr@127.0.0.1:1/app?password=Zm9v%2BcXV4",
		"postgres://plus@127.0.0.1:1/app?password=Zm9v%20cXV4",
		"postgres://h/app?application_name=a+b&password=x",
		"postgres://u:a+b@h/app",
		"postgres://h/app?password=x#a+b",
		"postgres://h/app?password=x&password=a+b",
		"host=h password=a+b",
		"POSTGRES://h/app?password=a+b",
		"",
	} {
		require.NoError(t, rawPlusError(connString), connString)
	}
}

func TestPreDriverError_ReturnsTheFirstCheckThatRefuses(t *testing.T) {
	for _, tc := range []struct {
		connString string
		stage      string
		part       string
	}{
		{"POSTGRES://app:1234/s3cretPW@127.0.0.1:1/app", "scheme-case", ""},
		{"POSTGRES://h/app?x=1", "scheme-case", ""},
		{" postgres://u:pw@h/db?sslmode=disable", "keyword-key", ""},
		{"postgres://app:1234/s3cretPW@127.0.0.1:1/app", "raw-at", "path"},
		{"postgres://h/app?password=a@b&c", "raw-at", "query"},
		{"postgres://h/app?password=a+b&c", "after-password", ""},
		{"postgres://h/app?password=a+b", "raw-plus", ""},
	} {
		t.Run(tc.connString, func(t *testing.T) {
			var cse *ConnStringError
			require.ErrorAs(t, PreDriverError(tc.connString), &cse)
			require.Equal(t, tc.stage, cse.Stage())
			require.Equal(t, tc.part, cse.Part())
		})
	}
	for _, connString := range []string{
		"postgres://app:pr@of@PW@127.0.0.1:1/app",
		"postgres://h/app?sslmode=disable&password=x",
		"host=h password=x",
	} {
		require.NoError(t, PreDriverError(connString), connString)
	}
}

func TestPreDriverError_MatchesEachConnStringCaseResolveStage(t *testing.T) {
	for _, tc := range testutil.ConnStringCases() {
		t.Run(tc.Name, func(t *testing.T) {
			err := PreDriverError(tc.ConnString)
			if tc.ResolveStage == "" {
				require.NoError(t, err)
				return
			}
			var cse *ConnStringError
			require.ErrorAs(t, err, &cse)
			require.Equal(t, tc.ResolveStage, cse.Stage())
			require.Equal(t, tc.ResolvePart, cse.Part())
		})
	}
}
