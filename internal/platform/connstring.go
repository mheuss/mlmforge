package platform

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

type connDriver string

const (
	driverMigrate connDriver = "golang-migrate"
	driverPgx     connDriver = "pgx"
)

type connStage string

const (
	stageParse   connStage = "parse"
	stageScheme  connStage = "scheme"
	stageRefused connStage = "refused"
)

// ConnStringError reports a connection string a driver refused, holding nothing from the string.
type ConnStringError struct {
	driver connDriver
	stage  connStage
}

// Driver names the driver whose call site refused the connection string.
func (e *ConnStringError) Driver() string { return string(e.driver) }

// Stage is "parse" when a URL-form string failed to parse, "scheme" when migrate refused the string's scheme, and "refused" otherwise.
func (e *ConnStringError) Stage() string { return string(e.stage) }

func (e *ConnStringError) Error() string {
	if e.stage == stageScheme {
		return "mlmforge migrate accepts only a connection string that starts with postgres:// or postgresql://. The connection string is withheld because it can contain a password."
	}
	what := "could not parse the connection string"
	if e.stage == stageRefused {
		what = "refused the connection string"
	}
	return fmt.Sprintf("%s %s. The connection string and %s's message are withheld because they can contain a password.",
		e.driver, what, e.driver)
}

// PgxConnStringError returns a ConnStringError in place of a pgx parse error, and any other error unchanged.
func PgxConnStringError(err error) error {
	var pce *pgconn.ParseConfigError
	if !errors.As(err, &pce) {
		return err
	}
	return &ConnStringError{driver: driverPgx, stage: pgxStage(pce.ConnString)}
}

// pgxStage reports parse for a URL-form string that url.Parse rejects, and refused otherwise.
func pgxStage(connString string) connStage {
	if !strings.HasPrefix(connString, "postgres://") && !strings.HasPrefix(connString, "postgresql://") {
		return stageRefused
	}
	if _, err := url.Parse(connString); err != nil {
		return stageParse
	}
	return stageRefused
}

// migrateConnStringError returns a ConnStringError in place of a URL parse error, and any other error unchanged.
func migrateConnStringError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) || ue.Op != "parse" {
		return err
	}
	return &ConnStringError{driver: driverMigrate, stage: stageParse}
}

// migrateSchemeError returns a ConnStringError for a string without a postgres:// or postgresql:// prefix, and nil otherwise.
func migrateSchemeError(dbURL string) error {
	if strings.HasPrefix(dbURL, "postgres://") || strings.HasPrefix(dbURL, "postgresql://") {
		return nil
	}
	return &ConnStringError{driver: driverMigrate, stage: stageScheme}
}

// libPQEnvProbe is a connection string that holds nothing from any operator's string.
const libPQEnvProbe = "postgres://u@h/d"

// migrateDriverParseError returns lib/pq's own error for a refused environment or an undetectable user, a ConnStringError when lib/pq refuses the filtered connection string, and nil otherwise.
func migrateDriverParseError(dbURL string) error {
	purl, err := url.Parse(dbURL)
	if err != nil {
		return nil
	}
	_, err = pq.NewConnector(migrate.FilterCustomQuery(purl).String())
	if err == nil {
		return nil
	}
	// Order matters: probed only after the string fails, so a string that overrides a bad environment still passes.
	if _, probeErr := pq.NewConnector(libPQEnvProbe); probeErr != nil {
		return probeErr
	}
	if errors.Is(err, pq.ErrCouldNotDetectUsername) {
		return err
	}
	return &ConnStringError{driver: driverMigrate, stage: stageRefused}
}
