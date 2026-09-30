package testutil

import (
	"errors"
	"net/url"
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
}

// WithoutPassword returns the connection string with its password removed.
func (c ConnStringCase) WithoutPassword() string {
	return strings.ReplaceAll(c.ConnString, c.Password, "")
}

// ConnStringCases returns the refused connection strings the leak tests run.
func ConnStringCases() []ConnStringCase {
	return []ConnStringCase{
		{Name: "slash", ConnString: "postgres://app:Zm9vQmFy/cXV4eHl6@127.0.0.1:1/app",
			Password: "Zm9vQmFy/cXV4eHl6", PgxStage: "parse", MigrateStage: "parse"},
		{Name: "bad-escape", ConnString: "postgres://app:Zm9vQmFy%zzcXV4eHl6@127.0.0.1:1/app",
			Password: "Zm9vQmFy%zzcXV4eHl6", PgxStage: "parse", MigrateStage: "parse"},
		{Name: "fragment", ConnString: "postgres://app:Zm9vQmFy#cXV4eHl6@127.0.0.1:1/app",
			Password: "Zm9vQmFy#cXV4eHl6", PgxStage: "parse", MigrateStage: "parse"},
		{Name: "bad-sslmode", ConnString: "postgres://app@127.0.0.1:1/app?password=qs3cretpwXYZ&sslmode=bogus",
			Password: "qs3cretpwXYZ", PgxStage: "refused"},
		{Name: "missing-service", ConnString: "postgres://app@127.0.0.1:1/app?password=qs3cretpwXYZ&service=nosuch",
			Password: "qs3cretpwXYZ", PgxStage: "refused"},
		{Name: "empty-timeout", ConnString: "postgres://app@127.0.0.1:1/app?password=qs3cretpwXYZ&connect_timeout=",
			Password: "qs3cretpwXYZ", PgxStage: "refused"},
		{Name: "keyword-form", ConnString: "host=127.0.0.1 port=1 password=kv3cretpwXYZ sslmode=bogus",
			Password: "kv3cretpwXYZ", PgxStage: "refused", MigrateStage: "scheme"},
		{Name: "bad-pool-option", ConnString: "postgres://app@127.0.0.1:1/app?password=qs3cretpwXYZ&pool_max_conns=abc",
			Password: "qs3cretpwXYZ", PgxStage: "refused"},
		{Name: "keyword-colon", ConnString: "password=kc3cretpwXYZ host=::1 dbname=app",
			Password: "kc3cretpwXYZ", MigrateStage: "scheme"},
		{Name: "opaque-scheme", ConnString: "postgres:app:op3cretpwXYZ@127.0.0.1:1/app",
			Password: "op3cretpwXYZ", PgxStage: "refused", MigrateStage: "scheme"},
		{Name: "uppercase-scheme", ConnString: "POSTGRES://app:up3cretpwXYZ@127.0.0.1:1/app",
			Password: "up3cretpwXYZ", PgxStage: "refused", MigrateStage: "scheme"},
	}
}

var refusalTexts = map[string]string{
	"golang-migrate/parse":  "golang-migrate could not parse the connection string. The connection string and golang-migrate's message are withheld because they can contain a password.",
	"golang-migrate/scheme": "mlmforge migrate accepts only a connection string that starts with postgres:// or postgresql://. The connection string is withheld because it can contain a password.",
	"pgx/parse":             "pgx could not parse the connection string. The connection string and pgx's message are withheld because they can contain a password.",
	"pgx/refused":           "pgx refused the connection string. The connection string and pgx's message are withheld because they can contain a password.",
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

// RequireNoDriverParseError fails the test when err's chain holds a *url.Error or a *pgconn.ParseConfigError.
func RequireNoDriverParseError(t *testing.T, err error) {
	t.Helper()
	var ue *url.Error
	if errors.As(err, &ue) {
		t.Fatalf("the error chain holds a *url.Error")
	}
	var pce *pgconn.ParseConfigError
	if errors.As(err, &pce) {
		t.Fatalf("the error chain holds a *pgconn.ParseConfigError")
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
