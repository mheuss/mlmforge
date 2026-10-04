package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/spf13/cobra"
)

// treeWriter is the surface the write commands drive.
type treeWriter interface {
	AddRoot(ctx context.Context, r networkengine.AddRootRequest) (networkengine.WriteResult, error)
	Place(ctx context.Context, r networkengine.PlaceRequest) (networkengine.WriteResult, error)
	Remove(ctx context.Context, r networkengine.RemoveRequest) (networkengine.WriteResult, error)
	Reject(ctx context.Context, r networkengine.RejectRequest) (networkengine.RejectResult, error)
}

// writerFor builds the writer a write command drives.
type writerFor func(deps *treeDeps) treeWriter

// timeFlag reads an RFC 3339 time flag, defaulting to now in UTC.
func timeFlag(name, value string, now time.Time) (time.Time, error) {
	if value == "" {
		return now.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("--%s %q is not an RFC 3339 time: %w", name, value, err)
	}
	return t.UTC(), nil
}

// reportWrite prints a write's outcome and returns the error to report.
func reportWrite(ctx context.Context, out, warn io.Writer, res networkengine.WriteResult, err error) error {
	printRedelivered(out, res.CaughtUp, "")
	var notCurrent error
	if err == nil {
		projected := "projected"
		if res.ProjectionErr != nil {
			projected = "projection returned an error"
		}
		_, _ = fmt.Fprintf(out, "appended event %s at version %d to stream %s; %s\n",
			res.EventID, res.Version, res.Stream, projected)
		if res.ProjectionErr != nil {
			notCurrent = warnProjection(warn, res)
		}
	}
	printReleaseWarning(warn, res.ReleaseErr)
	if err == nil {
		return notCurrent
	}
	var unknown *networkengine.AppendOutcomeUnknownError
	if errors.As(err, &unknown) || ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("the command's context ended (%v) and no append was confirmed: %w", context.Cause(ctx), err)
}

// warnProjection prints what a write observed after its projection returned an
// error. It returns an exit-3 error unless the store was observed current.
func warnProjection(warn io.Writer, res networkengine.WriteResult) error {
	head := fmt.Sprintf("warning: event %s at version %d was appended and its projection returned an error: %s.",
		res.EventID, res.Version, res.ProjectionErr)
	notCurrent := &exitCodeError{code: exitNotCurrent, err: fmt.Errorf(
		"event %s at version %d was appended and the store was not observed current", res.EventID, res.Version)}
	obs := res.Observed
	switch {
	case obs == nil:
		_, _ = fmt.Fprintf(warn, "%s The tree's projected version was not read.\n", head)
		return notCurrent
	case obs.Err != nil:
		_, _ = fmt.Fprintf(warn, "%s The tree's projected version could not be read: %s.\n", head, obs.Err)
		return notCurrent
	}
	seen := fmt.Sprintf("The tree's projected version is %d.", obs.Version)
	if !obs.Found {
		seen = "The tree has no projection row."
	}
	if obs.Found && obs.Version >= res.Version {
		_, _ = fmt.Fprintf(warn, "%s %s\n", head, seen)
		return nil
	}
	if obs.Version == res.Version-1 {
		seen += " The next write or tree load of this tree redelivers it."
	}
	_, _ = fmt.Fprintf(warn, "%s %s\n", head, seen)
	return notCurrent
}

// printRedelivered prints the redelivered-event line, when there was one.
func printRedelivered(out io.Writer, ev *networkengine.CaughtUpEvent, suffix string) {
	if ev == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "redelivered event %s at version %d%s\n", ev.EventID, ev.Version, suffix)
}

// printReleaseWarning prints a failed lock release, when there was one.
func printReleaseWarning(warn io.Writer, err error) {
	if err == nil {
		return
	}
	_, _ = fmt.Fprintf(warn, "warning: releasing the tree lock reported: %s\n", err)
}

func newTreeAddRootCmd(resolve flagResolver, open depsOpener, writer writerFor) *cobra.Command {
	var treeID, userID, sponsorID, treeType, spillover, enrolledAt string
	var width int

	cmd := &cobra.Command{
		Use:   "add-root",
		Short: "Append a tree's root_added event and try to project it",
		Long: "Opens a database pool, starts the engine worker, appends one tree.root_added event, tries to project it, and exits. " +
			"The tree type and matrix flags shape the tree when its stream is empty. After that the stream's first event decides the shape, " +
			"and a type or matrix flag that differs from it is refused. " +
			"Exits 0 when the event was appended and the store was observed current, with any warnings on stderr. " +
			"Exits 3 when the event was appended and the store was not observed current. Exits 1 when no append was confirmed.",
		Args: cobra.NoArgs,
		// Moving this to the tree group leaves a real invocation dumping
		// usage after the error line.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			at, err := timeFlag("enrolled-at", enrolledAt, time.Now())
			if err != nil {
				return err
			}
			req := networkengine.AddRootRequest{
				TreeID: treeID, UserID: userID, SponsorID: sponsorID, TreeType: treeType, EnrolledAt: at,
			}
			if cmd.Flags().Changed("matrix-width") {
				req.MatrixWidth = &width
			}
			if cmd.Flags().Changed("matrix-spillover") {
				req.MatrixSpillover = &spillover
			}
			return runTreeCommand(cmd, resolve, open, func(ctx context.Context, deps *treeDeps) error {
				res, err := writer(deps).AddRoot(ctx, req)
				return reportWrite(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), res, err)
			})
		},
	}
	cmd.Flags().StringVar(&treeID, "tree-id", "", "Tree to write (UUID)")
	cmd.Flags().StringVar(&userID, "user-id", "", "Root user (UUID)")
	cmd.Flags().StringVar(&sponsorID, "sponsor-id", "", "Root user's sponsor (UUID)")
	cmd.Flags().StringVar(&treeType, "tree-type", "", "unilevel, binary or matrix")
	cmd.Flags().IntVar(&width, "matrix-width", 0, "Matrix width (matrix trees only)")
	cmd.Flags().StringVar(&spillover, "matrix-spillover", "", "Matrix spillover rule (matrix trees only)")
	cmd.Flags().StringVar(&enrolledAt, "enrolled-at", "", "Enrolment time, RFC 3339 (default now, in UTC)")
	for _, name := range []string{"tree-id", "user-id", "sponsor-id", "tree-type"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func newTreePlaceCmd(resolve flagResolver, open depsOpener, writer writerFor) *cobra.Command {
	var treeID, userID, parentID, sponsorID, enrolledAt string
	var position int

	cmd := &cobra.Command{
		Use:   "place",
		Short: "Append a placement at an explicit parent and try to project it",
		Long: "Opens a database pool, starts the engine worker, appends one tree.node_placed event, tries to project it, and exits. " +
			"Matrix and binary trees need --position. Unilevel trees take none. " +
			"Exits 0 when the event was appended and the store was observed current, with any warnings on stderr. " +
			"Exits 3 when the event was appended and the store was not observed current. Exits 1 when no append was confirmed.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			at, err := timeFlag("enrolled-at", enrolledAt, time.Now())
			if err != nil {
				return err
			}
			req := networkengine.PlaceRequest{
				TreeID: treeID, UserID: userID, ParentID: parentID, SponsorID: sponsorID, EnrolledAt: at,
			}
			if cmd.Flags().Changed("position") {
				req.Position = &position
			}
			return runTreeCommand(cmd, resolve, open, func(ctx context.Context, deps *treeDeps) error {
				res, err := writer(deps).Place(ctx, req)
				return reportWrite(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), res, err)
			})
		},
	}
	cmd.Flags().StringVar(&treeID, "tree-id", "", "Tree to write (UUID)")
	cmd.Flags().StringVar(&userID, "user-id", "", "User to place (UUID)")
	cmd.Flags().StringVar(&parentID, "parent-id", "", "Parent to place under (UUID)")
	cmd.Flags().StringVar(&sponsorID, "sponsor-id", "", "Sponsor (UUID)")
	cmd.Flags().IntVar(&position, "position", 0, "Slot under the parent (matrix and binary trees only)")
	cmd.Flags().StringVar(&enrolledAt, "enrolled-at", "", "Enrolment time, RFC 3339 (default now, in UTC)")
	for _, name := range []string{"tree-id", "user-id", "parent-id", "sponsor-id"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func newTreeRemoveCmd(resolve flagResolver, open depsOpener, writer writerFor) *cobra.Command {
	var treeID, userID, removedAt string

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Append a leaf's removal and try to project it",
		Long: "Opens a database pool, starts the engine worker, appends one tree.node_removed event, tries to project it, and exits. " +
			"Removal from a matrix tree is refused. " +
			"Exits 0 when the event was appended and the store was observed current, with any warnings on stderr. " +
			"Exits 3 when the event was appended and the store was not observed current. Exits 1 when no append was confirmed.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			at, err := timeFlag("removed-at", removedAt, time.Now())
			if err != nil {
				return err
			}
			req := networkengine.RemoveRequest{TreeID: treeID, UserID: userID, RemovedAt: at}
			return runTreeCommand(cmd, resolve, open, func(ctx context.Context, deps *treeDeps) error {
				res, err := writer(deps).Remove(ctx, req)
				return reportWrite(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), res, err)
			})
		},
	}
	cmd.Flags().StringVar(&treeID, "tree-id", "", "Tree to write (UUID)")
	cmd.Flags().StringVar(&userID, "user-id", "", "User to remove (UUID)")
	cmd.Flags().StringVar(&removedAt, "removed-at", "", "Removal time recorded in the event, RFC 3339 (default now, in UTC)")
	for _, name := range []string{"tree-id", "user-id"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
