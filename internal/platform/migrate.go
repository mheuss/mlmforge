package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// migrationSourceURL converts a migrations directory path to a file:// URL,
// normalizing to an absolute path so behavior is not CWD-dependent.
func migrationSourceURL(migrationsPath string) (string, error) {
	absPath, err := filepath.Abs(migrationsPath)
	if err != nil {
		return "", fmt.Errorf("resolve migrations path: %w", err)
	}
	return fmt.Sprintf("file://%s", absPath), nil
}

// dirtyError returns a DirtyError when err carries golang-migrate's dirty-record error.
func (mg *migration) dirtyError(err error) (*DirtyError, bool) {
	var dirty migrate.ErrDirty
	if !errors.As(err, &dirty) {
		return nil, false
	}
	rec := Record{Version: dirty.Version, Dirty: true}
	return &DirtyError{Record: rec, Source: mg.sourceInfo(rec.Version)}, true
}

// MigrateUp applies all pending database migrations from the given directory.
func MigrateUp(dbURL, migrationsPath string) (err error) {
	if err = refuseMultiStatement("up", dbURL); err != nil {
		return err
	}
	mg, err := openMigration(dbURL, migrationsPath)
	if err != nil {
		return err
	}
	defer mg.closeInto(&err)

	return mg.upOutcome(mg.m.Up())
}

// upOutcome splits release failures off an Up result, classifies the rest, and joins them back.
func (mg *migration) upOutcome(raw error) error {
	releases, upErr := SplitRelease(raw)
	return withReleases(mg.upResult(upErr), releases)
}

// upResult classifies the result of golang-migrate's Up.
func (mg *migration) upResult(upErr error) error {
	if upErr == nil || errors.Is(upErr, migrate.ErrNoChange) {
		return nil
	}
	// No lock means this run wrote nothing, so a record read now says nothing about it.
	if errors.Is(upErr, migrate.ErrLockTimeout) {
		return fmt.Errorf("apply migrations: %w", upErr)
	}
	if dirty, ok := mg.dirtyError(upErr); ok {
		return dirty
	}
	after := mg.recordRead()
	return &ApplyError{Err: upErr, After: after, Source: mg.sourceFor(after)}
}

// ErrNoChange is returned by MigrateDown when there are no migrations to roll back.
var ErrNoChange = migrate.ErrNoChange

// MigrateDown rolls back the most recent migration.
// Returns ErrNoChange when there are no migrations left to roll back.
func MigrateDown(dbURL, migrationsPath string) error {
	sourceURL, err := migrationSourceURL(migrationsPath)
	if err != nil {
		return err
	}
	m, err := migrate.New(sourceURL, dbURL)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Steps(-1); err != nil {
		if err == migrate.ErrNoChange {
			return ErrNoChange
		}
		return fmt.Errorf("rollback migration: %w", err)
	}
	return nil
}

// migration holds one source driver, one database driver, and the migrator built from them.
type migration struct {
	m      *migrate.Migrate
	source source.Driver
	db     database.Driver
	path   string
}

// releaseTagged marks the database driver's Unlock failures as ReleaseError.
type releaseTagged struct {
	database.Driver
}

func (d releaseTagged) Unlock() error {
	if err := d.Driver.Unlock(); err != nil {
		return &ReleaseError{What: "releasing the migration lock failed", Err: err}
	}
	return nil
}

// releaseErr wraps a cleanup failure as a ReleaseError.
func releaseErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return &ReleaseError{What: what, Err: err}
}

// openMigration opens the drivers one migrate command uses.
func openMigration(dbURL, migrationsPath string) (*migration, error) {
	absPath, err := filepath.Abs(migrationsPath)
	if err != nil {
		return nil, fmt.Errorf("resolve migrations path: %w", err)
	}
	src, err := source.Open("file://" + absPath)
	if err != nil {
		return nil, fmt.Errorf("open migrations source %s: %w", absPath, err)
	}
	db, err := database.Open(dbURL)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open database: %w", err),
			releaseErr("closing the migrations source failed", src.Close()))
	}
	tagged := releaseTagged{Driver: db}
	m, err := migrate.NewWithInstance("file", src, "postgres", tagged)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create migrator: %w", err),
			releaseErr("closing the migrations source failed", src.Close()),
			releaseErr("closing the database failed", db.Close()))
	}
	return &migration{m: m, source: src, db: tagged, path: absPath}, nil
}

// closeInto closes the migrator and both drivers, joining any failure into *errp as a ReleaseError.
func (mg *migration) closeInto(errp *error) {
	srcErr, dbErr := mg.m.Close()
	if err := errors.Join(srcErr, dbErr); err != nil {
		*errp = errors.Join(*errp, &ReleaseError{What: "closing the migration drivers failed", Err: err})
	}
}

// readRecord reads the version table through the database driver.
func (mg *migration) readRecord() (Record, error) {
	version, dirty, err := mg.db.Version()
	if err != nil {
		return Record{}, err
	}
	return Record{Version: version, Dirty: dirty}, nil
}

// recordRead reads the version table and keeps the read error beside the record.
func (mg *migration) recordRead() RecordRead {
	rec, err := mg.readRecord()
	return RecordRead{Record: rec, Err: err}
}

// sourceInfo places version in the migrations directory.
func (mg *migration) sourceInfo(version int) SourceInfo {
	info := SourceInfo{Path: mg.path}
	if version < 0 {
		return info
	}
	n := uint(version)
	body, _, err := mg.source.ReadUp(n)
	if errors.Is(err, os.ErrNotExist) {
		return info
	}
	if err != nil {
		info.Err = err
		return info
	}
	_ = body.Close()
	info.InSource = true

	first, err := mg.source.First()
	if err != nil {
		info.Err = err
		return info
	}
	if first == n {
		return info
	}
	prev, err := mg.source.Prev(n)
	if err != nil {
		info.Err = err
		return info
	}
	info.Previous, info.HasPrevious = prev, true
	return info
}

// sourceFor places a dirty record read in the migrations directory.
func (mg *migration) sourceFor(read RecordRead) SourceInfo {
	if read.Err != nil || !read.Record.Dirty {
		return SourceInfo{Path: mg.path}
	}
	return mg.sourceInfo(read.Record.Version)
}

// MigrateVersion returns the migration record and where a dirty record's version sits in the migrations directory.
func MigrateVersion(dbURL, migrationsPath string) (st Status, err error) {
	mg, err := openMigration(dbURL, migrationsPath)
	if err != nil {
		return Status{}, err
	}
	defer mg.closeInto(&err)

	read := mg.recordRead()
	if read.Err != nil {
		return Status{}, fmt.Errorf("read migration record: %w", read.Err)
	}
	return Status{Record: read.Record, Source: mg.sourceFor(read)}, nil
}
