package platform

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/golang-migrate/migrate/v4"
)

// upInSteps applies pending migrations one at a time.
func (mg *migration) upInSteps(ctx context.Context) error {
	applied := false
	for ctx.Err() == nil {
		if mg.upToDate() {
			if applied {
				return nil
			}
			return migrate.ErrNoChange
		}
		if err := mg.m.Steps(1); err != nil {
			return err
		}
		applied = true
	}
	return nil
}

// holdingDriverLock runs op while holding the database driver's own lock, and joins any release failure to op's result.
func (mg *migration) holdingDriverLock(op func() error) error {
	if err := mg.db.holdLock(); err != nil {
		return err
	}
	return errors.Join(op(), mg.db.releaseHold())
}

// upToDate reports whether the record reads as the last migration in the source.
func (mg *migration) upToDate() bool {
	rec, err := mg.readRecord()
	return err == nil && mg.isLast(rec)
}

// isLast reports whether rec is clean and names a version the source holds, with no migration after it.
// It reports false on any source read failure.
func (mg *migration) isLast(rec Record) bool {
	if rec.Dirty || rec.Version < 0 {
		return false
	}
	n := uint(rec.Version)
	if !mg.sourceHolds(n) {
		return false
	}
	_, err := mg.source.Next(n)
	return errors.Is(err, os.ErrNotExist)
}

// sourceHolds reports whether the source has an up file or a down file for version n.
// It reports false when a read fails for any reason other than a missing file.
func (mg *migration) sourceHolds(n uint) bool {
	for _, read := range []func(uint) (io.ReadCloser, string, error){mg.source.ReadUp, mg.source.ReadDown} {
		body, _, err := read(n)
		if err == nil {
			_ = body.Close()
			return true
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	return false
}

// downStep reads the record and rolls back one migration unless ctx has ended, both under the database driver's own lock.
func (mg *migration) downStep(ctx context.Context) (before RecordRead, err error) {
	err = mg.holdingDriverLock(func() error {
		before = mg.recordRead()
		if ctx.Err() != nil {
			return nil
		}
		return mg.m.Steps(-1)
	})
	return before, err
}

// upStopped is the outcome of an up that ended early: nil when the record reads as the last migration in the source, a StoppedError otherwise.
func (mg *migration) upStopped(before RecordRead) error {
	after := mg.recordRead()
	if after.Err != nil {
		return &StoppedError{Command: "up", Before: before, After: after}
	}
	if mg.isLast(after.Record) {
		return nil
	}
	return &StoppedError{Command: "up", Before: before, After: after, Source: mg.sourceInfo(after.Record.Version)}
}

// downStopped is the outcome of a down whose context ended: nil when the record moved, a StoppedError otherwise.
func (mg *migration) downStopped(before RecordRead) error {
	after := mg.recordRead()
	if after.Err == nil && before.Err == nil && after.Record != before.Record {
		return nil
	}
	return &StoppedError{Command: "down", Before: before, After: after}
}
