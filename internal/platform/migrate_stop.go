package platform

import (
	"context"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
)

// upInSteps applies pending migrations one at a time, and returns once none are left, a step fails, or ctx has ended between steps.
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

// upToDate reports whether the record is clean and the source has no migration after it.
// Any read failure reports false, so the next step meets the failure itself.
func (mg *migration) upToDate() bool {
	rec, err := mg.readRecord()
	if err != nil || rec.Dirty {
		return false
	}
	src := mg.sourceInfo(rec.Version)
	placed := src.InSource || rec.Version == database.NilVersion
	return src.Err == nil && placed && !src.HasNext
}

// downOnce rolls back one migration unless ctx has already ended.
func (mg *migration) downOnce(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	return mg.m.Steps(-1)
}

// upStopped is the outcome of an up that ended after its context ended: nil when no migration is left, a StoppedError otherwise.
func (mg *migration) upStopped(before RecordRead) error {
	after := mg.recordRead()
	if after.Err != nil {
		return &StoppedError{Command: "up", Before: before, After: after}
	}
	src := mg.sourceInfo(after.Record.Version)
	placed := src.InSource || after.Record.Version == database.NilVersion
	if src.Err == nil && placed && !src.HasNext && !after.Record.Dirty {
		return nil
	}
	return &StoppedError{Command: "up", Before: before, After: after, Source: src}
}

// downStopped is the outcome of a down that ended after its context ended: nil when the record moved, a StoppedError otherwise.
func (mg *migration) downStopped(before RecordRead) error {
	after := mg.recordRead()
	if after.Err == nil && before.Err == nil && after.Record != before.Record {
		return nil
	}
	return &StoppedError{Command: "down", Before: before, After: after}
}
