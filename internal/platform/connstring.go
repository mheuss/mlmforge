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
	driverMigrate  connDriver = "golang-migrate"
	driverPgx      connDriver = "pgx"
	driverMlmforge connDriver = "mlmforge"
)

type connStage string

const (
	stageParse      connStage = "parse"
	stageScheme     connStage = "scheme"
	stageRefused    connStage = "refused"
	stageRawAt      connStage = "raw-at"
	stageSchemeCase connStage = "scheme-case"
)

type connPart string

const (
	partPath     connPart = "path"
	partQuery    connPart = "query"
	partFragment connPart = "fragment"
)

// ConnStringError reports a connection string that mlmforge or a driver refused, holding nothing from the string.
type ConnStringError struct {
	driver connDriver
	stage  connStage
	part   connPart
}

// Driver names the driver whose call site refused the connection string, or mlmforge for a check that runs before any driver.
func (e *ConnStringError) Driver() string { return string(e.driver) }

// Stage is "parse" when a URL-form string failed to parse, "scheme" when migrate refused the string's scheme, "raw-at" or "scheme-case" when mlmforge refused it before any driver, and "refused" otherwise.
func (e *ConnStringError) Stage() string { return string(e.stage) }

// Part names where a raw-at refusal found the '@', and is empty for every other stage.
func (e *ConnStringError) Part() string { return string(e.part) }

func (e *ConnStringError) Error() string {
	switch e.stage {
	case stageScheme:
		return "mlmforge migrate accepts only a connection string that starts with postgres:// or postgresql://. The connection string is withheld because it can contain a password."
	case stageRawAt:
		// Concatenated, not formatted: the text holds %40.
		return "mlmforge found a raw @ after the host part, in the connection string's " + string(e.part) +
			". If a password holds @ / ? or #, percent-encode them. Write any other literal @ there as %40." +
			" The connection string is withheld because it can contain a password."
	case stageSchemeCase:
		return "mlmforge found a connection string whose scheme is not all lowercase. Write postgres:// or postgresql:// in lowercase." +
			" The connection string is withheld because it can contain a password."
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

// rawAtError returns a ConnStringError for a postgres:// or postgresql:// string with a raw '@' after the host part, and nil otherwise.
func rawAtError(dbURL string) error {
	rest, ok := strings.CutPrefix(dbURL, "postgres://")
	if !ok {
		if rest, ok = strings.CutPrefix(dbURL, "postgresql://"); !ok {
			return nil
		}
	}
	// Cut in this order. Any other order puts an '@' in the wrong part, or misses one.
	beforeFragment, fragment, _ := strings.Cut(rest, "#")
	beforeQuery, query, _ := strings.Cut(beforeFragment, "?")
	_, path, _ := strings.Cut(beforeQuery, "/")
	for _, p := range []struct {
		part connPart
		text string
	}{{partPath, path}, {partQuery, query}, {partFragment, fragment}} {
		if strings.Contains(p.text, "@") {
			return &ConnStringError{driver: driverMlmforge, stage: stageRawAt, part: p.part}
		}
	}
	return nil
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
