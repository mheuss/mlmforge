package testutil

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// passwordWindow is the length of the password pieces a leak check looks for.
const passwordWindow = 4

// ConnStringCase is a connection string a driver refuses, with the password it carries.
type ConnStringCase struct {
	Name         string
	ConnString   string
	Password     string
	PgxStage     string
	MigrateStage string
	ResolveStage string
	ResolvePart  string
}

// WithoutPassword returns the connection string with its password removed.
func (c ConnStringCase) WithoutPassword() string {
	return strings.ReplaceAll(c.ConnString, c.Password, "")
}

// ConnStringCases returns the refused connection strings, each with its password.
func ConnStringCases() []ConnStringCase {
	return []ConnStringCase{
		{Name: "slash", ConnString: "postgres://app:Zm9vQmFy/cXV4eHl6@127.0.0.1:1/app",
			Password: "Zm9vQmFy/cXV4eHl6", PgxStage: "parse", MigrateStage: "parse", ResolveStage: "raw-at", ResolvePart: "path"}, // gitleaks:allow
		{Name: "bad-escape", ConnString: "postgres://app:Zm9vQmFy%zzcXV4eHl6@127.0.0.1:1/app",
			Password: "Zm9vQmFy%zzcXV4eHl6", PgxStage: "parse", MigrateStage: "parse"},
		{Name: "fragment", ConnString: "postgres://app:Zm9vQmFy#cXV4eHl6@127.0.0.1:1/app",
			Password: "Zm9vQmFy#cXV4eHl6", PgxStage: "parse", MigrateStage: "parse", ResolveStage: "raw-at", ResolvePart: "fragment"},
		{Name: "bad-sslmode", ConnString: "postgres://app@127.0.0.1:1/app?sslmode=bogus&password=qs3cretpwXYZ", // gitleaks:allow
			Password: "qs3cretpwXYZ", PgxStage: "refused"}, // gitleaks:allow
		{Name: "missing-service", ConnString: "postgres://app@127.0.0.1:1/app?service=nosuch&password=qs3cretpwXYZ", // gitleaks:allow
			Password: "qs3cretpwXYZ", PgxStage: "refused"}, // gitleaks:allow
		{Name: "empty-timeout", ConnString: "postgres://app@127.0.0.1:1/app?connect_timeout=&password=qs3cretpwXYZ", // gitleaks:allow
			Password: "qs3cretpwXYZ", PgxStage: "refused"}, // gitleaks:allow
		{Name: "keyword-form", ConnString: "host=127.0.0.1 port=1 password=kv3cretpwXYZ sslmode=bogus", // gitleaks:allow
			Password: "kv3cretpwXYZ", PgxStage: "refused", MigrateStage: "scheme"}, // gitleaks:allow
		{Name: "bad-pool-option", ConnString: "postgres://app@127.0.0.1:1/app?pool_max_conns=abc&password=qs3cretpwXYZ", // gitleaks:allow
			Password: "qs3cretpwXYZ", PgxStage: "refused"}, // gitleaks:allow
		{Name: "keyword-colon", ConnString: "password=kc3cretpwXYZ host=::1 dbname=app",
			Password: "kc3cretpwXYZ", MigrateStage: "scheme"},
		{Name: "opaque-scheme", ConnString: "postgres:app:op3cretpwXYZ@127.0.0.1:1/app",
			Password: "op3cretpwXYZ", PgxStage: "refused", MigrateStage: "scheme"},
		{Name: "uppercase-scheme", ConnString: "POSTGRES://app:up3cretpwXYZ@127.0.0.1:1/app",
			Password: "up3cretpwXYZ", PgxStage: "refused", MigrateStage: "scheme", ResolveStage: "scheme-case"},
		{Name: "pq-quoted-key", ConnString: "postgres://app:pq3cretpwXYZ@127.0.0.1:1/app?p%3D%27a=z%3D",
			Password: "pq3cretpwXYZ", MigrateStage: "refused"},
		{Name: "pq-spaced-key", ConnString: "postgres://app:Zm9vQmFy@cXV4?d2l0aHh5bXdk ZXZl=YWJjZA@127.0.0.1:1/app",
			Password: "Zm9vQmFy@cXV4?d2l0aHh5bXdk ZXZl=YWJjZA", MigrateStage: "refused", ResolveStage: "raw-at", ResolvePart: "query"},
	}
}

// The checks a ResolveRefusedCase's twin is held to.
const (
	TwinParsers    = "parsers"
	TwinPgxRefuses = "pgx-refuses"
	TwinResolve    = "resolve"
)

// ResolveRefusedCase is a connection string mlmforge refuses before any driver sees it.
type ResolveRefusedCase struct {
	Name           string
	ConnString     string
	Password       string
	Wiring         string
	WiringPassword string
	Twin           string
	TwinCheck      string
	Stage          string
	Part           string
}

// WiringWithoutPassword returns the wiring string with its password removed.
func (c ResolveRefusedCase) WiringWithoutPassword() string {
	return strings.ReplaceAll(c.Wiring, c.WiringPassword, "")
}

// ResolveRefusedCases returns the connection strings mlmforge refuses before any driver, each with its twin, held to the check its TwinCheck names.
func ResolveRefusedCases() []ResolveRefusedCase {
	return []ResolveRefusedCase{
		{Name: "ticket-path",
			ConnString: "postgres://app:Zm9vQmFy@cXV4eHl6/d2l0aA@127.0.0.1:1/app", Password: "Zm9vQmFy@cXV4eHl6/d2l0aA", // gitleaks:allow
			Wiring: "postgres://app:Zm9vQmFy@cXV4.invalid/d2l0aA@127.0.0.1:1/app", WiringPassword: "Zm9vQmFy@cXV4.invalid/d2l0aA", // gitleaks:allow
			Twin: "postgres://app:Zm9vQmFy@cXV4eHl6/d2l0aA%40127.0.0.1:1/app", TwinCheck: TwinParsers, // gitleaks:allow
			Stage: "raw-at", Part: "path"},
		{Name: "numeric-port-path",
			ConnString: "postgres://app:1234/s3cretPW@127.0.0.1:1/app", Password: "1234/s3cretPW", // gitleaks:allow
			Wiring: "postgres://u.invalid:1234/s3cretPW@127.0.0.1:1/app", WiringPassword: "1234/s3cretPW", // gitleaks:allow
			Twin: "postgres://app:1234/s3cretPW%40127.0.0.1:1/app", TwinCheck: TwinParsers,
			Stage: "raw-at", Part: "path"},
		{Name: "out-of-range-port-path",
			ConnString: "postgres://app:123456/s3cretPW@127.0.0.1:1/app", Password: "123456/s3cretPW", // gitleaks:allow
			Wiring: "postgres://u.invalid:123456/s3cretPW@127.0.0.1:1/app", WiringPassword: "123456/s3cretPW", // gitleaks:allow
			Twin: "postgres://app:123456/s3cretPW%40127.0.0.1:1/app", TwinCheck: TwinPgxRefuses,
			Stage: "raw-at", Part: "path"},
		{Name: "option-value-query",
			ConnString: "postgres://app:Zm9v@h?binary_parameters=s3cretPW@127.0.0.1:1/app", Password: "Zm9v@h?binary_parameters=s3cretPW", // gitleaks:allow
			Wiring: "postgres://app:Zm9v@h.invalid?binary_parameters=s3cretPW@127.0.0.1:1/app", WiringPassword: "Zm9v@h.invalid?binary_parameters=s3cretPW", // gitleaks:allow
			Twin: "postgres://app:Zm9v@h?binary_parameters=s3cretPW%40127.0.0.1:1/app", TwinCheck: TwinParsers, // gitleaks:allow
			Stage: "raw-at", Part: "query"},
		{Name: "fragment",
			ConnString: "postgres://u.invalid:1234#s3cretPWxyz@127.0.0.1:1/app", Password: "1234#s3cretPWxyz", // gitleaks:allow
			Wiring: "postgres://u.invalid:1234#s3cretPWxyz@127.0.0.1:1/app", WiringPassword: "1234#s3cretPWxyz", // gitleaks:allow
			Twin: "postgres://u.invalid:1234#s3cretPWxyz%40127.0.0.1:1/app", TwinCheck: TwinParsers,
			Stage: "raw-at", Part: "fragment"},
		{Name: "database-name",
			ConnString: "postgres://app:pw4xyzQr@127.0.0.1:1/my@db", Password: "pw4xyzQr", // gitleaks:allow
			Wiring: "postgres://app:pw4xyzQr@127.0.0.1:1/my@db", WiringPassword: "pw4xyzQr", // gitleaks:allow
			Twin: "postgres://app:pw4xyzQr@127.0.0.1:1/my%40db", TwinCheck: TwinParsers, // gitleaks:allow
			Stage: "raw-at", Part: "path"},
		{Name: "query-password",
			ConnString: "postgres://app@127.0.0.1:1/app?password=qs3cret@pwXYZ", Password: "qs3cret@pwXYZ", // gitleaks:allow
			Wiring: "postgres://app@127.0.0.1:1/app?password=qs3cret@pwXYZ", WiringPassword: "qs3cret@pwXYZ", // gitleaks:allow
			Twin: "postgres://app@127.0.0.1:1/app?password=qs3cret%40pwXYZ", TwinCheck: TwinParsers, // gitleaks:allow
			Stage: "raw-at", Part: "query"},
		{Name: "uppercase-scheme-path",
			ConnString: "POSTGRES://app:1234/s3cretPW@127.0.0.1:1/app", Password: "1234/s3cretPW", // gitleaks:allow
			Wiring: "POSTGRES://u.invalid:1234/s3cretPW@127.0.0.1:1/app", WiringPassword: "1234/s3cretPW", // gitleaks:allow
			Twin: "postgres://app:1234/s3cretPW%40127.0.0.1:1/app", TwinCheck: TwinParsers,
			Stage: "scheme-case"},
		{Name: "mixed-case-scheme-path",
			ConnString: "Postgres://app:Zm9vQmFy@cXV4eHl6/d2l0aA@127.0.0.1:1/app", Password: "Zm9vQmFy@cXV4eHl6/d2l0aA", // gitleaks:allow
			Wiring: "Postgres://app:Zm9vQmFy@cXV4.invalid/d2l0aA@127.0.0.1:1/app", WiringPassword: "Zm9vQmFy@cXV4.invalid/d2l0aA", // gitleaks:allow
			Twin: "postgres://app:Zm9vQmFy@cXV4eHl6/d2l0aA%40127.0.0.1:1/app", TwinCheck: TwinParsers, // gitleaks:allow
			Stage: "scheme-case"},
		{Name: "uppercase-scheme-query",
			ConnString: "POSTGRESQL://app:Zm9v@h?binary_parameters=s3cretPW@127.0.0.1:1/app", Password: "Zm9v@h?binary_parameters=s3cretPW", // gitleaks:allow
			Wiring: "POSTGRESQL://app:Zm9v@h.invalid?binary_parameters=s3cretPW@127.0.0.1:1/app", WiringPassword: "Zm9v@h.invalid?binary_parameters=s3cretPW", // gitleaks:allow
			Twin: "postgres://app:Zm9v@h?binary_parameters=s3cretPW%40127.0.0.1:1/app", TwinCheck: TwinParsers, // gitleaks:allow
			Stage: "scheme-case"},
		{Name: "mixed-case-scheme-plain",
			ConnString: "PostgreSQL://u.invalid/app",
			Wiring:     "PostgreSQL://u.invalid/app",
			Twin:       "postgresql://u.invalid/app", TwinCheck: TwinResolve,
			Stage: "scheme-case"},
	}
}

// ReachesDriverCase is a connection string that reaches a driver, with the dial it reaches.
type ReachesDriverCase struct {
	Name       string
	Env        map[string]string
	ConnString string
	Dial       string
	Tree       bool
}

// ReachesDriverCases returns the connection strings that must still reach a driver.
func ReachesDriverCases() []ReachesDriverCase {
	return []ReachesDriverCase{
		{Name: "postgres scheme", ConnString: "postgres://app@127.0.0.1:1/app?sslmode=disable", Dial: "dial tcp 127.0.0.1:1"},
		{Name: "postgresql scheme", ConnString: "postgresql://app@127.0.0.1:1/app?sslmode=disable", Dial: "dial tcp 127.0.0.1:1"},
		{Name: "client_encoding overrides the environment", Env: map[string]string{"PGCLIENTENCODING": "LATIN1"},
			ConnString: "postgres://app:S3cretPWxyz@127.0.0.1:1/app?client_encoding=UTF8&sslmode=disable", Dial: "dial tcp 127.0.0.1:1"},
		{Name: "datestyle overrides the environment", Env: map[string]string{"PGDATESTYLE": "German"},
			ConnString: "postgres://app:S3cretPWxyz@127.0.0.1:1/app?datestyle=ISO%2C+MDY&sslmode=disable", Dial: "dial tcp 127.0.0.1:1"},
		{Name: "query port overrides an out-of-range authority port",
			ConnString: "postgres://app:S3cretPWxyz@127.0.0.1:100000/app?port=2&sslmode=disable", Dial: "dial tcp 127.0.0.1:2"},
		{Name: "query port overrides authority port 0",
			ConnString: "postgres://app:S3cretPWxyz@127.0.0.1:0/app?port=1&sslmode=disable", Dial: "dial tcp 127.0.0.1:1"},
		{Name: "authority port 0", ConnString: "postgres://app:S3cretPWxyz@127.0.0.1:0/app?sslmode=disable", Dial: "dial tcp 127.0.0.1:0"},
		{Name: "bare at in the userinfo", ConnString: "postgres://app:pr@of@PW@127.0.0.1:1/app", Dial: "dial tcp 127.0.0.1:1", Tree: true},
		{Name: "ticket password encoded", ConnString: "postgres://app:Zm9vQmFy%40cXV4eHl6%2Fd2l0aA@127.0.0.1:1/app", Dial: "dial tcp 127.0.0.1:1", Tree: true},
		{Name: "numeric password encoded", ConnString: "postgres://app:1234%2Fs3cretPW@127.0.0.1:1/app", Dial: "dial tcp 127.0.0.1:1", Tree: true},
		{Name: "query password encoded", ConnString: "postgres://app:Zm9v%40h%3Fbinary_parameters=s3cretPW@127.0.0.1:1/app", Dial: "dial tcp 127.0.0.1:1", Tree: true},
		{Name: "fragment password encoded", ConnString: "postgres://u.invalid:1234%23s3cretPWxyz@127.0.0.1:1/app", Dial: "dial tcp 127.0.0.1:1", Tree: true},
		{Name: "database name with %40", ConnString: "postgres://app:pw4xyzQr@127.0.0.1:1/my%40db", Dial: "dial tcp 127.0.0.1:1", Tree: true},
		{Name: "query password with %40", ConnString: "postgres://app@127.0.0.1:1/app?password=qs3cret%40pwXYZ", Dial: "dial tcp 127.0.0.1:1", Tree: true},
	}
}

var refusalTexts = map[string]string{
	"golang-migrate/parse":   "golang-migrate could not parse the connection string. The connection string and golang-migrate's message are withheld because they can contain a password.",
	"golang-migrate/refused": "golang-migrate refused the connection string. The connection string and golang-migrate's message are withheld because they can contain a password.",
	"golang-migrate/scheme":  "mlmforge migrate accepts only a connection string that starts with postgres:// or postgresql://. The connection string is withheld because it can contain a password.",
	"pgx/parse":              "pgx could not parse the connection string. The connection string and pgx's message are withheld because they can contain a password.",
	"pgx/refused":            "pgx refused the connection string. The connection string and pgx's message are withheld because they can contain a password.",
}

// RefusalText returns the text a refused connection string is expected to print for driver and stage.
func RefusalText(t *testing.T, driver, stage string) string {
	t.Helper()
	text, ok := refusalTexts[driver+"/"+stage]
	if !ok {
		t.Fatalf("no refusal text for driver %q and stage %q", driver, stage)
	}
	return text
}

var preDriverTexts = map[string]string{
	"raw-at/path":     "mlmforge found a raw @ after the host part, in the connection string's path. If a password holds @ / ? or #, percent-encode them. Write any other literal @ there as %40. The connection string is withheld because it can contain a password.",
	"raw-at/query":    "mlmforge found a raw @ after the host part, in the connection string's query. If a password holds @ / ? or #, percent-encode them. Write any other literal @ there as %40. The connection string is withheld because it can contain a password.",
	"raw-at/fragment": "mlmforge found a raw @ after the host part, in the connection string's fragment. If a password holds @ / ? or #, percent-encode them. Write any other literal @ there as %40. The connection string is withheld because it can contain a password.",
	"scheme-case/":    "mlmforge found a connection string whose scheme is not all lowercase. Write postgres:// or postgresql:// in lowercase. The connection string is withheld because it can contain a password.",
	"keyword-key/":    "mlmforge found a connection string that is not a postgres:// or postgresql:// URL, and its first keyword holds a character no setting name can hold. Check for a stray character, such as a space or a quote, before postgres://. The connection string is withheld because it can contain a password.",
	"after-password/": "mlmforge found a query key or & after password in the connection string. Put password last in the query, or move the password into the user part before the @. Write each & in the password as %26. The connection string is withheld because it can contain a password.",
	"raw-plus/":       "mlmforge found a raw + in the connection string's query password, which is read as a space. Write a plus as %2B and a space as %20. The connection string is withheld because it can contain a password.",
}

// PreDriverText returns the text a connection string refused before any driver is expected to print for stage and part.
func PreDriverText(t *testing.T, stage, part string) string {
	t.Helper()
	text, ok := preDriverTexts[stage+"/"+part]
	if !ok {
		t.Fatalf("no pre-driver text for stage %q and part %q", stage, part)
	}
	return text
}

// PasswordWindows returns each window of password, in order, that appears in text.
func PasswordWindows(text, password string) []string {
	var found []string
	for i := 0; i+passwordWindow <= len(password); i++ {
		if window := password[i : i+passwordWindow]; strings.Contains(text, window) {
			found = append(found, window)
		}
	}
	return found
}

// RequireNoPasswordWindow fails the test when a window of password appears in text.
func RequireNoPasswordWindow(t *testing.T, text, password string, known ...string) {
	t.Helper()
	if len(password) < passwordWindow {
		t.Fatalf("test password %q is shorter than %d characters", password, passwordWindow)
	}
	// Checked first, so an overlap with expected text is not mistaken for a leak.
	for _, k := range known {
		if found := PasswordWindows(k, password); len(found) > 0 {
			t.Fatalf("test password windows %q also appear in expected text %q", found, k)
		}
	}
	if found := PasswordWindows(text, password); len(found) > 0 {
		t.Fatalf("password windows %q appear in: %s", found, text)
	}
}

// RequireNoDriverParseError fails the test when err is nil or its chain holds a *url.Error or a *pgconn.ParseConfigError.
func RequireNoDriverParseError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error; got nil")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		t.Fatalf("the error chain holds a *url.Error")
	}
	var pce *pgconn.ParseConfigError
	if errors.As(err, &pce) {
		t.Fatalf("the error chain holds a *pgconn.ParseConfigError")
	}
}

// ClearLibPQEnv unsets the lib/pq settings that make it refuse any connection string, for one test.
func ClearLibPQEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"PGCLIENTENCODING", "PGDATESTYLE"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

// IsolatePgxEnv clears the connect timeout settings and points the Postgres service files at an empty directory, for one test.
func IsolatePgxEnv(t *testing.T) {
	t.Helper()
	ClearTimeoutEnv(t)
	dir := t.TempDir()
	t.Setenv("PGSERVICEFILE", filepath.Join(dir, "pg_service.conf"))
	t.Setenv("PGSYSCONFDIR", dir)
}
