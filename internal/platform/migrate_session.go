package platform

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/lib/pq"
)

// migrateApplicationName is the fallback application name of a migrate connection.
const migrateApplicationName = "mlmforge-migrate"

// session holds one database connection and the pool that owns it.
type session struct {
	db           *sql.DB
	conn         *sql.Conn
	databaseName string
}

// close ends the session.
func (s session) close() error {
	return errors.Join(s.conn.Close(), s.db.Close())
}

// dialSession opens one lib/pq connection for dbURL.
func dialSession(ctx context.Context, dbURL string) (session, error) {
	var s session
	err := TimeConnect(ctx, dbURL, func() error {
		if err := migrateSchemeError(dbURL); err != nil {
			return err
		}
		if err := migrateDriverParseError(dbURL); err != nil {
			return err
		}
		purl, err := url.Parse(dbURL)
		if err != nil {
			return migrateConnStringError(err)
		}
		connector, err := pq.NewConnector(driverURL(purl))
		if err != nil {
			return &ConnStringError{driver: driverMigrate, stage: stageRefused}
		}
		db := sql.OpenDB(connector)
		db.SetMaxOpenConns(1)
		conn, err := db.Conn(context.Background())
		if err != nil {
			_ = db.Close()
			return migrateConnStringError(err)
		}
		s = session{db: db, conn: conn, databaseName: purl.Path}
		return nil
	})
	return s, err
}

// checkIntervalOption asks the server to check every second that the client is still connected.
const checkIntervalOption = "-c client_connection_check_interval=1000"

// driverURL returns purl without golang-migrate's settings, with a fallback application name and the check interval added.
func driverURL(purl *url.URL) string {
	filtered := migrate.FilterCustomQuery(purl)
	q := filtered.Query()
	if !q.Has("fallback_application_name") {
		q.Set("fallback_application_name", migrateApplicationName)
	}
	q.Set("options", sessionOptions(q.Get("options"), os.Getenv("PGOPTIONS")))
	filtered.RawQuery = q.Encode()
	return filtered.String()
}

// sessionOptions puts the check interval ahead of the URL's options, or of PGOPTIONS when the URL's are empty.
func sessionOptions(fromURL, fromEnv string) string {
	theirs := fromURL
	if theirs == "" {
		theirs = fromEnv
	}
	if theirs == "" {
		return checkIntervalOption
	}
	return checkIntervalOption + " " + theirs
}

// openDriver hands the session's connection to golang-migrate's Postgres driver.
func openDriver(ctx context.Context, s session) (database.Driver, error) {
	driver, err := postgres.WithConnection(ctx, s.conn, &postgres.Config{DatabaseName: s.databaseName})
	if err != nil {
		return nil, err
	}
	return driver, nil
}

// releaseSession unlocks the migration lock, closes the migrator, then closes the database, and returns each failure as a ReleaseError.
func releaseSession(unlock func() error, closeMigrator func() (error, error), closeDB func() error) error {
	var errs []error
	if err := unlock(); err != nil {
		errs = append(errs, &ReleaseError{What: "releasing the mlmforge migration lock failed", Err: err})
	}
	srcErr, dbErr := closeMigrator()
	if err := errors.Join(srcErr, dbErr); err != nil {
		errs = append(errs, &ReleaseError{What: "closing the migration drivers failed", Err: err})
	}
	if err := closeDB(); err != nil {
		errs = append(errs, &ReleaseError{What: "closing the database connection failed", Err: err})
	}
	return errors.Join(errs...)
}
