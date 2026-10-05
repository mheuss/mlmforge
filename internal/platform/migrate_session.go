package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

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
	if q.Get("fallback_application_name") == "" {
		q.Set("fallback_application_name", migrateApplicationName)
	}
	q.Set("options", sessionOptions(q.Get("options"), os.Getenv("PGOPTIONS")))
	filtered.RawQuery = q.Encode()
	return filtered.String()
}

// sessionOptions puts the check interval ahead of fromURL, or of fromEnv when fromURL is empty.
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
	if ctx.Err() != nil {
		return nil, &InterruptedError{During: duringDriverOpen}
	}
	driver, err := postgres.WithConnection(ctx, s.conn, &postgres.Config{DatabaseName: s.databaseName})
	if err != nil {
		if ctx.Err() != nil {
			return nil, &InterruptedError{During: duringDriverOpen}
		}
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

// sessionResult is one dial's outcome.
type sessionResult struct {
	s   session
	err error
}

// connectSession dials one session and stops waiting for it when ctx ends.
func connectSession(ctx context.Context, dbURL string) (session, error) {
	results := make(chan sessionResult, 1)
	go func() {
		s, err := dialSession(ctx, dbURL)
		results <- sessionResult{s: s, err: err}
	}()
	// Whoever receives from results owns the session it carries, so it is closed exactly once.
	select {
	case r := <-results:
		if r.err != nil && ctx.Err() != nil {
			return session{}, &InterruptedError{During: duringConnect}
		}
		return r.s, r.err
	case <-ctx.Done():
		go func() {
			if r := <-results; r.err == nil {
				_ = r.s.close()
			}
		}()
		return session{}, &InterruptedError{During: duringConnect}
	}
}

// openFailure prefixes err with "open database: " unless it reports an interrupt.
func openFailure(err error) error {
	var interrupted *InterruptedError
	if errors.As(err, &interrupted) {
		return err
	}
	return fmt.Errorf("open database: %w", err)
}

// migrateLockPollInterval is the pause between two attempts on a held migration lock.
const migrateLockPollInterval = 100 * time.Millisecond

// migrateUnlockTimeout bounds the unlock query.
const migrateUnlockTimeout = 5 * time.Second

// lockMigrations polls for the mlmforge migration lock on conn until it is granted or ctx ends.
func lockMigrations(ctx context.Context, conn *sql.Conn, wait LockWait) error {
	told := false
	for {
		var granted bool
		err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, 0)", MigrateLockNamespace).Scan(&granted)
		if ctx.Err() != nil {
			return &InterruptedError{During: duringLockWait}
		}
		if err != nil {
			return fmt.Errorf("take the migration lock: %w", err)
		}
		if granted {
			return nil
		}
		if !told && wait != nil {
			wait(lockHolder(ctx, conn))
			told = true
		}
		select {
		case <-ctx.Done():
			return &InterruptedError{During: duringLockWait}
		case <-time.After(migrateLockPollInterval):
		}
	}
}

// lockHolder returns the backend PID holding the mlmforge migration lock in this database, or 0 when none is found.
func lockHolder(ctx context.Context, conn *sql.Conn) int {
	var pid int
	err := conn.QueryRowContext(ctx, `SELECT pid FROM pg_locks
		WHERE locktype = 'advisory'
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND classid = $1 AND objid = 0 AND objsubid = 2 AND granted
		LIMIT 1`, MigrateLockNamespace).Scan(&pid)
	if err != nil {
		return 0
	}
	return pid
}

// unlockMigrations releases the mlmforge migration lock on conn.
func unlockMigrations(conn *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), migrateUnlockTimeout)
	defer cancel()
	var released bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1, 0)", MigrateLockNamespace).Scan(&released); err != nil {
		return err
	}
	if !released {
		return errors.New("pg_advisory_unlock returned false")
	}
	return nil
}

// closeAfter closes s after a failed open, and reports a close failure unless err is an interrupt.
func closeAfter(err error, s session) error {
	closeErr := s.close()
	var interrupted *InterruptedError
	if errors.As(err, &interrupted) {
		return nil
	}
	return releaseErr("closing the database failed", closeErr)
}
