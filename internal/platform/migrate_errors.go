package platform

import (
	"errors"
	"fmt"
)

// Record is the row the migration version table holds. Version is -1 for a table with no row.
type Record struct {
	Version int
	Dirty   bool
}

// String names the record as "none", "6, dirty" or "5, clean".
func (r Record) String() string {
	switch {
	case r.Version < 0 && !r.Dirty:
		return "none"
	case r.Dirty:
		return fmt.Sprintf("%d, dirty", r.Version)
	default:
		return fmt.Sprintf("%d, clean", r.Version)
	}
}

// SourceInfo places a record's version in the migrations directory.
type SourceInfo struct {
	Path        string
	InSource    bool
	Previous    uint
	HasPrevious bool
	Err         error
}

// Status is the record MigrateVersion read, and where its version sits in the migrations directory.
type Status struct {
	Record Record
	Source SourceInfo
}

// ResetResult is the record before and after ResetDirty changed it.
type ResetResult struct {
	From Record
	To   Record
}

// RecordRead is a record read, or the error the read returned.
type RecordRead struct {
	Record Record
	Err    error
}

// NotDirtyError reports a clean record, or a table with no row, where a dirty record was required.
type NotDirtyError struct {
	Record Record
}

func (e *NotDirtyError) Error() string {
	return fmt.Sprintf("migration record is not dirty: %s", e.Record)
}

// NegativeVersionError reports a dirty record at -1.
type NegativeVersionError struct {
	Record Record
}

func (e *NegativeVersionError) Error() string {
	return fmt.Sprintf("migration record is %s", e.Record)
}

// VersionNotInSourceError reports a dirty record whose version has no up file in the migrations directory.
type VersionNotInSourceError struct {
	Record Record
	Path   string
}

func (e *VersionNotInSourceError) Error() string {
	return fmt.Sprintf("migration record is %s; %s has no migration %d", e.Record, e.Path, e.Record.Version)
}

// MultiStatementError reports a database URL that turns on multi-statement mode for a command that refuses it.
type MultiStatementError struct {
	Command string
	Value   string
}

func (e *MultiStatementError) Error() string {
	return fmt.Sprintf("migrate %s refused: the database URL sets x-multi-statement=%s", e.Command, e.Value)
}

// DirtyError reports a dirty record found before a migration ran.
type DirtyError struct {
	Record Record
	Source SourceInfo
}

func (e *DirtyError) Error() string {
	return fmt.Sprintf("migration record is %s", e.Record)
}

// ApplyError reports a failed up migration and the record read after it.
type ApplyError struct {
	Err    error
	After  RecordRead
	Source SourceInfo
}

func (e *ApplyError) Error() string { return "apply migrations: " + e.Err.Error() }

func (e *ApplyError) Unwrap() error { return e.Err }

// RollbackError reports a failed down migration and the record read before and after it.
type RollbackError struct {
	Err    error
	Before RecordRead
	After  RecordRead
	Source SourceInfo
}

func (e *RollbackError) Error() string { return "rollback migration: " + e.Err.Error() }

func (e *RollbackError) Unwrap() error { return e.Err }

// ReleaseError reports a failure to release the migration lock or close the drivers.
type ReleaseError struct {
	What string
	Err  error
}

func (e *ReleaseError) Error() string { return e.What + ": " + e.Err.Error() }

func (e *ReleaseError) Unwrap() error { return e.Err }

// SplitRelease separates the ReleaseErrors joined into err from everything else.
func SplitRelease(err error) (releases []error, rest error) {
	var others []error
	for _, part := range flattenJoined(err) {
		if _, ok := part.(*ReleaseError); ok {
			releases = append(releases, part)
			continue
		}
		others = append(others, part)
	}
	switch len(others) {
	case 0:
		return releases, nil
	case 1:
		return releases, others[0]
	}
	return releases, errors.Join(others...)
}

// flattenJoined returns the parts of err, descending into joined errors only.
func flattenJoined(err error) []error {
	if err == nil {
		return nil
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return []error{err}
	}
	var parts []error
	for _, part := range joined.Unwrap() {
		parts = append(parts, flattenJoined(part)...)
	}
	return parts
}

// withReleases joins releases back onto err.
func withReleases(err error, releases []error) error {
	if len(releases) == 0 {
		return err
	}
	return errors.Join(append([]error{err}, releases...)...)
}
