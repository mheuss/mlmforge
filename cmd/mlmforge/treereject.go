package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/spf13/cobra"
)

// rejectEventLong is reject-event's help text.
const rejectEventLong = "Opens a database pool, starts the engine worker, and appends one tree.event_rejected event naming the stream's last event, then tries to project it. " +
	"A rejection undoes the event's change to the tree. A row the event left is soft-deleted. " +
	"Rejecting a removal leaves the user in the tree. " +
	"The tree type and matrix shape that version 1 records still apply after its root is rejected. " +
	"The command first tries the event once more, and refuses unless that attempt fails in a way that shows the event cannot apply. " +
	"Exits 0 when the store was observed at or past the rejection. " +
	"Exits 3 when a rejection is in the stream and the store was not observed current; run the command again to project it. " +
	"On a rerun that projects a rejection already in the stream, --reason is required but not recorded: nothing is appended, and the rejection keeps its own reason. " +
	"Exits 1 when it refused and appended nothing, or when it could not confirm whether its append landed. The reason is on stderr."

func newTreeRejectEventCmd(resolve flagResolver, open depsOpener, writer writerFor) *cobra.Command {
	var treeID, eventID, reason string

	cmd := &cobra.Command{
		Use:          "reject-event",
		Short:        "Append a rejection of a tree stream's last event and try to project it",
		Long:         rejectEventLong,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := networkengine.RejectRequest{TreeID: treeID, EventID: eventID, Reason: reason}
			return runTreeCommand(cmd, resolve, open, func(ctx context.Context, deps *treeDeps) error {
				res, err := writer(deps).Reject(ctx, req)
				return reportReject(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), res, err)
			})
		},
	}
	cmd.Flags().StringVar(&treeID, "tree-id", "", "Tree whose stream ends with the event (UUID)")
	cmd.Flags().StringVar(&eventID, "event-id", "", "The stream's last event, to reject (UUID)")
	cmd.Flags().StringVar(&reason, "reason", "", "Why the event is rejected, recorded in the rejection")
	for _, name := range []string{"tree-id", "event-id", "reason"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// reportReject prints a Reject's outcome and returns the error to report.
func reportReject(ctx context.Context, out, warn io.Writer, res networkengine.RejectResult, err error) error {
	if err != nil {
		if res.RetryErr != nil && !errors.Is(err, res.RetryErr) {
			_, _ = fmt.Fprintf(warn, "retrying event %s at version %d returned: %s\n", res.RejectedEventID, res.RejectedVersion, res.RetryErr)
		}
		printReleaseWarning(warn, res.ReleaseErr)
		var unknown *networkengine.AppendOutcomeUnknownError
		if errors.As(err, &unknown) || ctx.Err() == nil {
			return err
		}
		return fmt.Errorf("the command's context ended (%v) and no append was confirmed: %w", context.Cause(ctx), err)
	}
	if res.RetryErr != nil {
		_, _ = fmt.Fprintf(warn, "retrying event %s at version %d returned: %s\n", res.RejectedEventID, res.RejectedVersion, res.RetryErr)
	}
	projected := "projected"
	if res.ProjectionErr != nil {
		projected = "projection returned an error"
	}
	switch res.Outcome {
	case networkengine.RejectOutcomeRejected:
		_, _ = fmt.Fprintf(out, "appended rejection %s at version %d to stream %s for event %s; %s\n",
			res.EventID, res.Version, res.Stream, res.RejectedEventID, projected)
	case networkengine.RejectOutcomeResumed:
		_, _ = fmt.Fprintf(out, "rejection %s at version %d for event %s was pending; %s; nothing was appended\n",
			res.EventID, res.Version, res.RejectedEventID, projected)
	case networkengine.RejectOutcomeAlreadyApplied:
		_, _ = fmt.Fprintf(out, "rejection %s at version %d for event %s is already projected; nothing was appended\n",
			res.EventID, res.Version, res.RejectedEventID)
	default:
		printReleaseWarning(warn, res.ReleaseErr)
		return fmt.Errorf("reject returned no error and outcome %q, which this command does not report", res.Outcome)
	}
	var notCurrent error
	if res.ProjectionErr != nil {
		notCurrent = warnRejectionProjection(warn, res)
	}
	printReleaseWarning(warn, res.ReleaseErr)
	return notCurrent
}

// warnRejectionProjection prints what Reject observed after a projection
// error. It returns an exit-3 error unless the store was observed current.
func warnRejectionProjection(warn io.Writer, res networkengine.RejectResult) error {
	head := fmt.Sprintf("warning: rejection %s at version %d is in the stream and its projection returned an error: %s.",
		res.EventID, res.Version, res.ProjectionErr)
	notCurrent := &exitCodeError{code: exitNotCurrent, err: fmt.Errorf(
		"rejection %s at version %d is in the stream and the store was not observed current; run tree reject-event again to project it",
		res.EventID, res.Version)}
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
	_, _ = fmt.Fprintf(warn, "%s %s\n", head, seen)
	if obs.Found && obs.Version >= res.Version {
		return nil
	}
	return notCurrent
}
