package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mlmforge/mlmforge/internal/platform"
)

const (
	upCommand    = "`mlmforge migrate up`"
	downCommand  = "`mlmforge migrate down`"
	resetCommand = "`mlmforge migrate reset-dirty`"

	unchangedText = "The record was not changed."
)

// describeRecord states what a record reads, in the present tense.
func describeRecord(r platform.Record) string {
	if r.Version == -1 && !r.Dirty {
		return "holds no version"
	}
	return "reads " + r.String()
}

// negativeText names a dirty record below 0 and says a reset does not change it.
func negativeText(version int) string {
	return fmt.Sprintf("The record reads %d, dirty. reset-dirty does not change a record at %d.", version, version)
}

// resetDoes describes the record a reset would leave, as a clause.
func resetDoes(src platform.SourceInfo) string {
	if src.HasPrevious {
		return fmt.Sprintf("it sets the record to %d, clean", src.Previous)
	}
	return "afterwards the record holds no version"
}

// upRecovery is the recovery for a dirty record left by migrate up.
func upRecovery(rec platform.Record, src platform.SourceInfo, fix string) string {
	switch {
	case src.Err != nil:
		return fmt.Sprintf("The migrations directory %s could not be read for migration %d: %v.", src.Path, rec.Version, src.Err)
	case !src.InSource:
		return fmt.Sprintf("The migrations directory %s has no migration %d, so %s will refuse.", src.Path, rec.Version, resetCommand)
	}
	return fmt.Sprintf("%s, run %s (%s), then run %s.", fix, resetCommand, resetDoes(src), upCommand)
}

// dirtyText is the text for a dirty record when the command that left it is unknown.
func dirtyText(rec platform.Record, src platform.SourceInfo) string {
	if rec.Version < 0 {
		return negativeText(rec.Version)
	}
	return strings.Join([]string{
		fmt.Sprintf("The record %s.", describeRecord(rec)),
		fmt.Sprintf("If the command that failed was %s: %s", upCommand, upRecovery(rec, src, "fix the cause shown in its error")),
		fmt.Sprintf("If the command that failed was %s: do not run %s.", downCommand, resetCommand),
	}, "\n")
}

// versionText renders a Status as a version line and, for a dirty record, its recovery text.
func versionText(st platform.Status) string {
	rec := st.Record
	if rec.Version == -1 && !rec.Dirty {
		return "Version: none, Dirty: false"
	}
	head := fmt.Sprintf("Version: %d, Dirty: %v", rec.Version, rec.Dirty)
	if !rec.Dirty {
		return head
	}
	return head + "\n" + dirtyText(rec, st.Source)
}

// withoutReleaseErrors writes each release failure in err to w as a warning and returns the rest.
func withoutReleaseErrors(w io.Writer, done string, err error) error {
	releases, rest := platform.SplitRelease(err)
	for _, release := range releases {
		if rest == nil {
			_, _ = fmt.Fprintf(w, "warning: %s; %v\n", done, release)
		} else {
			_, _ = fmt.Fprintf(w, "warning: %v\n", release)
		}
	}
	return rest
}

// operatorError carries replacement text for an error and still unwraps to it.
type operatorError struct {
	text string
	err  error
}

func (e *operatorError) Error() string { return e.text }

func (e *operatorError) Unwrap() error { return e.err }

// multiStatementText is the text for a refused multi-statement URL.
func multiStatementText(e *platform.MultiStatementError) string {
	return fmt.Sprintf("migrate %s refused: the database URL sets x-multi-statement=%s.", e.Command, e.Value)
}

// applyFailureText is the text for a failed migrate up.
func applyFailureText(e *platform.ApplyError) string {
	lines := []string{e.Error()}
	after := e.After
	switch {
	case after.Err != nil:
		lines = append(lines, fmt.Sprintf("The record could not be read after the failure: %v.", after.Err))
	case !after.Record.Dirty:
	case after.Record.Version < 0:
		lines = append(lines,
			fmt.Sprintf("This run was %s. The record now %s.", upCommand, describeRecord(after.Record)),
			fmt.Sprintf("reset-dirty does not change a record at %d.", after.Record.Version))
	default:
		lines = append(lines,
			fmt.Sprintf("This run was %s. The record now %s.", upCommand, describeRecord(after.Record)),
			upRecovery(after.Record, e.Source, "Fix the cause shown above"))
	}
	return strings.Join(lines, "\n")
}

// migrateErrorText returns the operator text for the typed migrate error err holds.
func migrateErrorText(command string, err error) (string, bool) {
	var (
		multi    *platform.MultiStatementError
		dirty    *platform.DirtyError
		apply    *platform.ApplyError
		rollback *platform.RollbackError
		notDirty *platform.NotDirtyError
		negative *platform.NegativeVersionError
		missing  *platform.VersionNotInSourceError
		notWrote *platform.NotWrittenError
		write    *platform.WriteError
	)
	switch {
	case errors.As(err, &multi):
		return multiStatementText(multi), true
	case errors.As(err, &dirty):
		return fmt.Sprintf("migrate %s did not run. %s", command, dirtyText(dirty.Record, dirty.Source)), true
	case errors.As(err, &apply):
		return applyFailureText(apply), true
	case errors.As(err, &rollback):
		return rollbackFailureText(rollback), true
	case errors.As(err, &notDirty):
		return fmt.Sprintf("reset-dirty changes only a dirty record. The record %s. %s", describeRecord(notDirty.Record), unchangedText), true
	case errors.As(err, &negative):
		return negativeText(negative.Record.Version) + " " + unchangedText, true
	case errors.As(err, &missing):
		return fmt.Sprintf("The record %s. The migrations directory %s has no migration %d. %s",
			describeRecord(missing.Record), missing.Path, missing.Record.Version, unchangedText), true
	case errors.As(err, &notWrote):
		return notWrote.Error() + "\nThis run did not write the record.", true
	case errors.As(err, &write):
		return writeFailureText(write), true
	}
	return "", false
}

// migrateError replaces a typed migrate error with its operator text and returns any other error unchanged.
func migrateError(command string, err error) error {
	if err == nil {
		return nil
	}
	if text, ok := migrateErrorText(command, err); ok {
		return &operatorError{text: text, err: err}
	}
	return err
}

// describeRecordBefore states what a record read, in the past tense.
func describeRecordBefore(r platform.Record) string {
	if r.Version == -1 && !r.Dirty {
		return "held no version"
	}
	return "read " + r.String()
}

// resetWould describes the record a reset would leave, as a sentence.
func resetWould(src platform.SourceInfo) string {
	if src.HasPrevious {
		return fmt.Sprintf("It would set the record to %d, clean.", src.Previous)
	}
	return "Afterwards the record would hold no version."
}

// rollbackFailureText is the text for a failed migrate down.
func rollbackFailureText(e *platform.RollbackError) string {
	lines := []string{e.Error()}
	after := e.After
	switch {
	case after.Err != nil:
		lines = append(lines, fmt.Sprintf("The record could not be read after the failure: %v.", after.Err))
		return strings.Join(lines, "\n")
	case !after.Record.Dirty:
		return e.Error()
	}

	observed := fmt.Sprintf("The record now %s.", describeRecord(after.Record))
	if e.Before.Err == nil {
		observed = fmt.Sprintf("The record %s before this run and now %s.",
			describeRecordBefore(e.Before.Record), describeRecord(after.Record))
	}
	lines = append(lines, "This run was "+downCommand+". "+observed)

	switch {
	case after.Record.Version < 0:
		lines = append(lines, fmt.Sprintf("reset-dirty does not change a record at %d.", after.Record.Version))
	case e.Source.Err == nil && e.Source.InSource:
		lines = append(lines, fmt.Sprintf("Do not run %s. %s", resetCommand, resetWould(e.Source)))
	default:
		lines = append(lines, fmt.Sprintf("Do not run %s.", resetCommand))
	}
	return strings.Join(lines, "\n")
}

// resetText describes a completed reset.
func resetText(r platform.ResetResult) string {
	return fmt.Sprintf("The record %s. It now %s.\nRun %s next.", describeRecordBefore(r.From), describeRecord(r.To), upCommand)
}

// writeFailureText is the text for a failed write of the record.
func writeFailureText(e *platform.WriteError) string {
	if e.After.Err != nil {
		return fmt.Sprintf("%s\nThe record could not be read after the failure: %v.", e.Error(), e.After.Err)
	}
	return fmt.Sprintf("%s\nThe record now %s.", e.Error(), describeRecord(e.After.Record))
}
