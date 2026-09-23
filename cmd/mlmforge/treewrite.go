package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/spf13/cobra"
)

// treeWriter is the surface the write commands drive.
type treeWriter interface {
	AddRoot(ctx context.Context, r networkengine.AddRootRequest) (networkengine.WriteResult, error)
	Place(ctx context.Context, r networkengine.PlaceRequest) (networkengine.WriteResult, error)
	Remove(ctx context.Context, r networkengine.RemoveRequest) (networkengine.WriteResult, error)
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
	if err == nil {
		if res.CaughtUp != nil {
			_, _ = fmt.Fprintf(out, "redelivered event %s at version %d\n", res.CaughtUp.EventID, res.CaughtUp.Version)
		}
		projected := "projected"
		if res.ProjectionErr != nil {
			projected = "not projected"
		}
		_, _ = fmt.Fprintf(out, "appended event %s at version %d to stream %s; %s\n",
			res.EventID, res.Version, res.Stream, projected)
		if res.ProjectionErr != nil {
			_, _ = fmt.Fprintf(warn, "warning: event %s at version %d was appended and did not project: %s. "+
				"The next write to this tree retries it.\n", res.EventID, res.Version, res.ProjectionErr)
		}
	}
	if res.ReleaseErr != nil {
		_, _ = fmt.Fprintf(warn, "warning: releasing the tree lock reported: %s\n", res.ReleaseErr)
	}
	var unknown *networkengine.AppendOutcomeUnknownError
	if err == nil || errors.As(err, &unknown) || ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("the command's context ended (%v) and no append was confirmed: %w", context.Cause(ctx), err)
}

// runTreeWrite resolves the connection flags and runs one write under the
// command's own signal context.
func runTreeWrite(cmd *cobra.Command, resolve flagResolver, open depsOpener, run treeRunner) error {
	url, workerPath, err := resolve()
	if err != nil {
		return err
	}
	// Established here rather than on the root command, which would disable the
	// default SIGINT kill for every command in the binary.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return withTreeDeps(ctx, cmd.ErrOrStderr(), open, url, workerPath, run)
}

func newTreeAddRootCmd(resolve flagResolver, open depsOpener, writer writerFor) *cobra.Command {
	var treeID, userID, sponsorID, treeType, spillover, enrolledAt string
	var width int

	cmd := &cobra.Command{
		Use:   "add-root",
		Short: "Append a tree's root_added event and try to project it",
		Long: "Opens a database pool, starts the engine worker, appends one tree.root_added event, tries to project it, and exits. " +
			"The tree type and matrix flags shape the tree when its stream is empty. After that the stream's first event decides the type.",
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
			return runTreeWrite(cmd, resolve, open, func(ctx context.Context, deps *treeDeps) error {
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
			"Matrix and binary trees need --position. Unilevel trees take none.",
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
			return runTreeWrite(cmd, resolve, open, func(ctx context.Context, deps *treeDeps) error {
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
			"Removal from a matrix tree is refused.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			at, err := timeFlag("removed-at", removedAt, time.Now())
			if err != nil {
				return err
			}
			req := networkengine.RemoveRequest{TreeID: treeID, UserID: userID, RemovedAt: at}
			return runTreeWrite(cmd, resolve, open, func(ctx context.Context, deps *treeDeps) error {
				res, err := writer(deps).Remove(ctx, req)
				return reportWrite(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), res, err)
			})
		},
	}
	cmd.Flags().StringVar(&treeID, "tree-id", "", "Tree to write (UUID)")
	cmd.Flags().StringVar(&userID, "user-id", "", "User to remove (UUID)")
	cmd.Flags().StringVar(&removedAt, "removed-at", "", "Removal time, RFC 3339 (default now, in UTC)")
	for _, name := range []string{"tree-id", "user-id"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
