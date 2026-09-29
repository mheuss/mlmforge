package platform

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4/database"
)

// Record is the row the migration version table holds. Version is -1 for a table with no row.
type Record struct {
	Version int
	Dirty   bool
}

// IsNone reports whether the record holds no version.
func (r Record) IsNone() bool { return r.Version == -1 && !r.Dirty }

// String names the record as "none", "6, dirty" or "5, clean".
func (r Record) String() string {
	switch {
	case r.IsNone():
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

// Status is a migration record and where its version sits in the migrations directory.
type Status struct {
	Record Record
	Source SourceInfo
}

// ResetResult is a migration record before and after a reset.
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

// NegativeVersionError reports a dirty record below 0.
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
// BodyFailed is true only when the error shows Postgres refused the migration file itself.
type ApplyError struct {
	Err        error
	After      RecordRead
	Source     SourceInfo
	BodyFailed bool
}

func (e *ApplyError) Error() string { return "apply migrations: " + migrationErrorText(e.Err) }

func (e *ApplyError) Unwrap() error { return e.Err }

// RollbackError reports a failed down migration and the record read before and after it.
type RollbackError struct {
	Err    error
	Before RecordRead
	After  RecordRead
	Source SourceInfo
}

func (e *RollbackError) Error() string { return "rollback migration: " + migrationErrorText(e.Err) }

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

// flattenJoined returns the parts of err, descending into any error that wraps several.
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

// NotWrittenError reports a reset that failed before it wrote the record.
type NotWrittenError struct {
	Err error
}

func (e *NotWrittenError) Error() string { return e.Err.Error() }

func (e *NotWrittenError) Unwrap() error { return e.Err }

// WriteError reports a failed write of the record and the record read after it.
type WriteError struct {
	Err   error
	After RecordRead
}

func (e *WriteError) Error() string { return "write migration record: " + e.Err.Error() }

func (e *WriteError) Unwrap() error { return e.Err }

// migrationErrorText renders err, leaving out any query text a driver error holds.
func migrationErrorText(err error) string {
	switch e := err.(type) {
	case database.Error:
		return databaseErrorText(e)
	case *database.Error:
		return databaseErrorText(*e)
	}
	return err.Error()
}

// databaseErrorText renders a driver error's message and underlying error, leaving out its query.
func databaseErrorText(e database.Error) string {
	text := e.Err
	if text == "" {
		text = fmt.Sprint(e.OrigErr)
	} else {
		text = fmt.Sprintf("%s (details: %v)", text, e.OrigErr)
	}
	if e.Line > 0 {
		text = fmt.Sprintf("%s in line %d", text, e.Line)
	}
	return text
}

// isBodyFailure reports whether err shows Postgres refusing the migration file itself.
// Anything it does not recognise reads as false.
func isBodyFailure(err error) bool {
	dbErr, ok := err.(database.Error)
	if !ok || !strings.HasPrefix(dbErr.Err, "migration failed") {
		return false
	}
	var serverErr interface{ SQLState() string }
	if !errors.As(dbErr.OrigErr, &serverErr) {
		return false
	}
	state := serverErr.SQLState()
	if len(state) != 5 {
		return false
	}
	// Connection, resource, operator-intervention, system and internal errors do not count as a rejection.
	switch state[:2] {
	case "08", "53", "57", "58", "XX":
		return false
	}
	return true
}
