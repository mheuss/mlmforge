package platform

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

type connDriver string

const (
	driverMigrate connDriver = "golang-migrate"
	driverPgx     connDriver = "pgx"
)

type connStage string

const (
	stageParse   connStage = "parse"
	stageRefused connStage = "refused"
)

// ConnStringError reports a connection string a driver refused, holding nothing from the string.
type ConnStringError struct {
	driver connDriver
	stage  connStage
}

// Driver names the driver that refused the connection string.
func (e *ConnStringError) Driver() string { return string(e.driver) }

// Stage is "parse" when a URL-form string failed to parse, and "refused" otherwise.
func (e *ConnStringError) Stage() string { return string(e.stage) }

func (e *ConnStringError) Error() string {
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
