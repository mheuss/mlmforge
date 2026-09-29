package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/mlmforge/mlmforge/internal/platform"
)

const (
	upCommand    = "`mlmforge migrate up`"
	downCommand  = "`mlmforge migrate down`"
	resetCommand = "`mlmforge migrate reset-dirty`"

	negativeText  = "The record reads -1, dirty. reset-dirty does not change a record at -1."
	unchangedText = "The record was not changed."
)

// describeRecord states what a record reads, in the present tense.
func describeRecord(r platform.Record) string {
	switch {
	case r.Version < 0 && !r.Dirty:
		return "holds no version"
	case r.Dirty:
		return fmt.Sprintf("reads %d, dirty", r.Version)
	default:
		return fmt.Sprintf("reads %d, clean", r.Version)
	}
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
		return negativeText
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
	if rec.Version < 0 && !rec.Dirty {
		return "Version: none, Dirty: false"
	}
	head := fmt.Sprintf("Version: %d, Dirty: %v", rec.Version, rec.Dirty)
	if !rec.Dirty {
		return head
	}
	return head + "\n" + dirtyText(rec, st.Source)
}

// withoutReleaseErrors writes each release failure in err to w as a warning and returns the rest.
// done names what the operation did, and is printed only when nothing but release failures remain.
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
