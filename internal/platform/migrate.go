package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// dirtyError returns a DirtyError when err carries golang-migrate's dirty-record error.
func (mg *migration) dirtyError(err error) (*DirtyError, bool) {
	var dirty migrate.ErrDirty
	if !errors.As(err, &dirty) {
		return nil, false
	}
	rec := Record{Version: dirty.Version, Dirty: true}
	return &DirtyError{Record: rec, Source: mg.sourceInfo(rec.Version)}, true
}

// LockWait is told which backend holds the migration lock when a command has to wait for it.
// A PID of 0 means the holder's PID was not read.
type LockWait func(holderPID int)

// MigrateUp applies the pending database migrations from the given directory, stopping between migrations once ctx ends.
func MigrateUp(ctx context.Context, dbURL, migrationsPath string, wait LockWait) (err error) {
	if err = refuseUnusedSettings("up", dbURL); err != nil {
		return err
	}
	if err = refuseMultiStatement("up", dbURL); err != nil {
		return err
	}
	mg, err := openMigration(ctx, dbURL, migrationsPath, wait)
	if err != nil {
		return err
	}
	defer mg.closeInto(&err)

	before := mg.recordRead()
	return mg.upOutcome(ctx, before, mg.holdingDriverLock(func() error { return mg.upInSteps(ctx) }))
}

// upOutcome classifies the result of applying the pending migrations.
func (mg *migration) upOutcome(ctx context.Context, before RecordRead, raw error) error {
	releases, upErr := SplitRelease(raw)
	if upErr == nil && ctx.Err() != nil {
		return withReleases(mg.upStopped(before), releases)
	}
	return withReleases(mg.upResult(upErr), releases)
}

// upResult classifies an error from applying the pending migrations.
func (mg *migration) upResult(upErr error) error {
	if upErr == nil || errors.Is(upErr, migrate.ErrNoChange) {
		return nil
	}
	// No lock means this run wrote nothing, so a record read now says nothing about it.
	if errors.Is(upErr, migrate.ErrLockTimeout) {
		return fmt.Errorf("apply migrations: %w", upErr)
	}
	var notTaken *LockNotTakenError
	if errors.As(upErr, &notTaken) {
		return fmt.Errorf("apply migrations: %w", upErr)
	}
	if dirty, ok := mg.dirtyError(upErr); ok {
		return dirty
	}
	after := mg.recordRead()
	return &ApplyError{Err: upErr, After: after, Source: mg.sourceFor(after), BodyFailed: isBodyFailure(upErr)}
}

// ErrNoChange is returned by MigrateDown when there are no migrations to roll back.
var ErrNoChange = migrate.ErrNoChange

// MigrateDown rolls back the most recent migration.
// Returns ErrNoChange when there are no migrations left to roll back.
func MigrateDown(ctx context.Context, dbURL, migrationsPath string, wait LockWait) (err error) {
	if err = refuseUnusedSettings("down", dbURL); err != nil {
		return err
	}
	if err = refuseMultiStatement("down", dbURL); err != nil {
		return err
	}
	mg, err := openMigration(ctx, dbURL, migrationsPath, wait)
	if err != nil {
		return err
	}
	defer mg.closeInto(&err)

	before, raw := mg.downStep(ctx)
	return mg.downOutcome(ctx, before, raw)
}

// downOutcome classifies the result of Steps(-1).
func (mg *migration) downOutcome(ctx context.Context, before RecordRead, raw error) error {
	releases, downErr := SplitRelease(raw)
	if downErr == nil && ctx.Err() != nil {
		return withReleases(mg.downStopped(before), releases)
	}
	return withReleases(mg.downResult(before, downErr), releases)
}

// downResult classifies the result of golang-migrate's Steps(-1).
func (mg *migration) downResult(before RecordRead, downErr error) error {
	var notTaken *LockNotTakenError
	switch {
	case downErr == nil:
		return nil
	case errors.Is(downErr, migrate.ErrNoChange):
		return ErrNoChange
	// No lock means this run wrote nothing, so a record read now says nothing about it.
	case errors.Is(downErr, migrate.ErrLockTimeout):
		return fmt.Errorf("rollback migration: %w", downErr)
	case errors.As(downErr, &notTaken):
		return fmt.Errorf("rollback migration: %w", downErr)
	case errors.Is(downErr, os.ErrNotExist) && before.Err == nil && before.Record == Record{Version: database.NilVersion}:
		return ErrNoChange
	}
	if dirty, ok := mg.dirtyError(downErr); ok {
		return dirty
	}
	after := mg.recordRead()
	return &RollbackError{Err: downErr, Before: before, After: after, Source: mg.sourceFor(after), BodyFailed: isBodyFailure(downErr)}
}

// migration holds one source driver, one database driver, and the migrator built from them.
type migration struct {
	m      *migrate.Migrate
	source source.Driver
	db     releaseTagged
	path   string
	sess   session
	unlock func() error
}

// releaseTagged marks the database driver's Lock and Unlock failures with their own error types.
type releaseTagged struct {
	database.Driver
	held *bool
}

// holding reports whether holdLock holds the driver's lock.
func (d releaseTagged) holding() bool {
	return d.held != nil && *d.held
}

func (d releaseTagged) Lock() error {
	if d.holding() {
		return nil
	}
	if err := d.Driver.Lock(); err != nil {
		return &LockNotTakenError{Err: err}
	}
	return nil
}

func (d releaseTagged) Unlock() error {
	if d.holding() {
		return nil
	}
	if err := d.Driver.Unlock(); err != nil {
		return &ReleaseError{What: "releasing the migration lock failed", Err: err}
	}
	return nil
}

// holdLock takes the driver's lock for a run of several steps.
func (d releaseTagged) holdLock() error {
	if err := d.Driver.Lock(); err != nil {
		return &LockNotTakenError{Err: err}
	}
	*d.held = true
	return nil
}

// releaseHold releases the lock holdLock took.
func (d releaseTagged) releaseHold() error {
	if !d.holding() {
		return nil
	}
	*d.held = false
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
func openMigration(ctx context.Context, dbURL, migrationsPath string, wait LockWait) (*migration, error) {
	if err := refuseUnsupportedEnv(); err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(migrationsPath)
	if err != nil {
		return nil, fmt.Errorf("resolve migrations path: %w", err)
	}
	src, err := source.Open("file://" + absPath)
	if err != nil {
		return nil, fmt.Errorf("open migrations source %s: %w", absPath, err)
	}
	sess, err := connectSession(ctx, dbURL)
	if err != nil {
		return nil, errors.Join(openFailure(err),
			releaseErr("closing the migrations source failed", src.Close()))
	}
	db, err := lockAndOpen(ctx, sess, wait)
	if err != nil {
		return nil, errors.Join(err, releaseErr("closing the migrations source failed", src.Close()))
	}
	tagged := releaseTagged{Driver: db, held: new(bool)}
	m, err := migrate.NewWithInstance("file", src, "postgres", tagged)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create migrator: %w", err),
			releaseErr("closing the migrations source failed", src.Close()),
			releaseErr("closing the database failed", sess.close()))
	}
	return &migration{m: m, source: src, db: tagged, path: absPath, sess: sess, unlock: func() error { return unlockMigrations(sess.conn) }}, nil
}

// closeInto releases the migration lock, closes the migrator and the session, and joins any failure into *errp as a ReleaseError.
func (mg *migration) closeInto(errp *error) {
	if err := releaseSession(mg.unlock, mg.m.Close, mg.sess.db.Close); err != nil {
		*errp = errors.Join(*errp, err)
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
	if version == database.NilVersion {
		first, err := mg.source.First()
		switch {
		case err == nil:
			info.Next, info.HasNext = first, true
		case !errors.Is(err, os.ErrNotExist):
			info.Err = err
		}
		return info
	}
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

	next, err := mg.source.Next(n)
	switch {
	case err == nil:
		info.Next, info.HasNext = next, true
	case !errors.Is(err, os.ErrNotExist):
		info.Err = err
		return info
	}

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
func MigrateVersion(ctx context.Context, dbURL, migrationsPath string, wait LockWait) (st Status, err error) {
	if err = refuseUnusedSettings("version", dbURL); err != nil {
		return Status{}, err
	}
	mg, err := openMigration(ctx, dbURL, migrationsPath, wait)
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

// ResetDirty changes a dirty migration record to the migration before it, clean.
func ResetDirty(ctx context.Context, dbURL, migrationsPath string, wait LockWait) (res ResetResult, err error) {
	if err = refuseUnusedSettings("reset-dirty", dbURL); err != nil {
		return ResetResult{}, err
	}
	if err = refuseMultiStatement("reset-dirty", dbURL); err != nil {
		return ResetResult{}, err
	}
	mg, err := openMigration(ctx, dbURL, migrationsPath, wait)
	if err != nil {
		return ResetResult{}, err
	}
	defer mg.closeInto(&err)

	return mg.resetDirty()
}

// ResetAfterFailedDown changes a dirty migration record to the migration after it, clean.
func ResetAfterFailedDown(ctx context.Context, dbURL, migrationsPath string, wait LockWait) (res ResetResult, err error) {
	if err = refuseUnusedSettings("reset-dirty --after-failed-down", dbURL); err != nil {
		return ResetResult{}, err
	}
	if err = refuseMultiStatement("reset-dirty --after-failed-down", dbURL); err != nil {
		return ResetResult{}, err
	}
	mg, err := openMigration(ctx, dbURL, migrationsPath, wait)
	if err != nil {
		return ResetResult{}, err
	}
	defer mg.closeInto(&err)

	return mg.resetLocked(mg.nextTarget)
}

// resetLocked reads the record and writes the record target picks, while holding the migration lock.
func (mg *migration) resetLocked(target func(Record) (Record, error)) (res ResetResult, err error) {
	if err = mg.db.Lock(); err != nil {
		return ResetResult{}, &NotWrittenError{Err: fmt.Errorf("take migration lock: %w", err)}
	}
	defer func() {
		if unlockErr := mg.db.Unlock(); unlockErr != nil {
			err = errors.Join(err, unlockErr)
		}
	}()

	rec, err := mg.readRecord()
	if err != nil {
		return ResetResult{}, &NotWrittenError{Err: fmt.Errorf("read migration record: %w", err)}
	}
	if !rec.Dirty {
		return ResetResult{}, &NotDirtyError{Record: rec}
	}
	to, err := target(rec)
	if err != nil {
		return ResetResult{}, err
	}
	if err = mg.db.SetVersion(to.Version, false); err != nil {
		return ResetResult{}, &WriteError{Err: err, After: mg.recordRead()}
	}
	return ResetResult{From: rec, To: to}, nil
}

// resetDirty moves a dirty record back to the previous migration, clean, while holding the migration lock.
func (mg *migration) resetDirty() (ResetResult, error) {
	return mg.resetLocked(mg.previousTarget)
}

// previousTarget picks the migration before a dirty record's version, or no version from the first migration.
func (mg *migration) previousTarget(rec Record) (Record, error) {
	if rec.Version < 0 {
		return Record{}, &NegativeVersionError{Record: rec}
	}
	src := mg.sourceInfo(rec.Version)
	if src.Err != nil {
		return Record{}, &NotWrittenError{Err: fmt.Errorf("read migration %d from %s: %w", rec.Version, src.Path, src.Err)}
	}
	if !src.InSource {
		return Record{}, &VersionNotInSourceError{Record: rec, Path: src.Path}
	}
	to := Record{Version: database.NilVersion}
	if src.HasPrevious {
		to.Version = int(src.Previous)
	}
	return to, nil
}

// nextTarget picks the migration after a dirty record's version, or the first migration from -1.
func (mg *migration) nextTarget(rec Record) (Record, error) {
	var next uint
	var err error
	switch {
	case rec.Version < database.NilVersion:
		return Record{}, &NegativeVersionError{Record: rec}
	case rec.Version == database.NilVersion:
		next, err = mg.source.First()
	default:
		n := uint(rec.Version)
		body, _, readErr := mg.source.ReadUp(n)
		if errors.Is(readErr, os.ErrNotExist) {
			return Record{}, &VersionNotInSourceError{Record: rec, Path: mg.path}
		}
		if readErr != nil {
			return Record{}, &NotWrittenError{Err: fmt.Errorf("read migration %d from %s: %w", n, mg.path, readErr)}
		}
		_ = body.Close()
		next, err = mg.source.Next(n)
	}
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, &NoNextMigrationError{Record: rec, Path: mg.path}
	}
	if err != nil {
		return Record{}, &NotWrittenError{Err: fmt.Errorf("read the migrations directory %s: %w", mg.path, err)}
	}

	down, _, err := mg.source.ReadDown(next)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, &NoDownFileError{Record: rec, Version: next, Path: mg.path}
	}
	if err != nil {
		return Record{}, &NotWrittenError{Err: fmt.Errorf("read the down file of migration %d from %s: %w", next, mg.path, err)}
	}
	if err = down.Close(); err != nil {
		return Record{}, &NotWrittenError{Err: fmt.Errorf("close the down file of migration %d from %s: %w", next, mg.path, err)}
	}
	return Record{Version: int(next)}, nil
}
