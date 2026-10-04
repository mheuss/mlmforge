package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTreeRejectEventCmd_PassesItsFlagsToTheWriter(t *testing.T) {
	w := &recordingWriter{rejectResult: networkengine.RejectResult{Outcome: networkengine.RejectOutcomeAlreadyApplied}}

	_, err := runWriteCmd(t, w, "reject-event", "--tree-id", "t", "--event-id", "e", "--reason", "sponsor never enrolled")

	require.NoError(t, err)
	assert.Equal(t, networkengine.RejectRequest{TreeID: "t", EventID: "e", Reason: "sponsor never enrolled"}, w.reject)
}

func TestTreeRejectEventCmd_RequiresTheReason(t *testing.T) {
	w := &recordingWriter{}

	_, err := runWriteCmd(t, w, "reject-event", "--tree-id", "t", "--event-id", "e")

	require.ErrorContains(t, err, `required flag(s) "reason" not set`)
	assert.Equal(t, networkengine.RejectRequest{}, w.reject)
}

func TestTreeRejectEventCmd_ReportsEachOutcome(t *testing.T) {
	retry := errors.New("load tree t; nothing was appended: node u references sponsor s that is not in the tree")
	boom := errors.New("connection reset by peer")
	base := networkengine.RejectResult{
		Stream: "tree-t", RejectedEventID: "e", RejectedVersion: 2, EventID: "r", Version: 3,
	}
	with := func(mutate func(*networkengine.RejectResult)) networkengine.RejectResult {
		res := base
		mutate(&res)
		return res
	}
	retryLine := "retrying event e at version 2 returned: " + retry.Error() + "\n"
	cases := []struct {
		name   string
		result networkengine.RejectResult
		err    error
		exit   int
		stdout string
		stderr []string
	}{
		{
			name: "rejected and projected", exit: 0,
			result: with(func(r *networkengine.RejectResult) {
				r.Outcome, r.RetryErr = networkengine.RejectOutcomeRejected, retry
			}),
			stdout: "appended rejection r at version 3 to stream tree-t for event e; projected\n",
			stderr: []string{retryLine},
		},
		{
			name: "rejected with the store observed behind", exit: 3,
			result: with(func(r *networkengine.RejectResult) {
				r.Outcome, r.RetryErr, r.ProjectionErr = networkengine.RejectOutcomeRejected, retry, boom
				r.Observed = &networkengine.ProjectionObservation{Version: 2, Found: true}
			}),
			stdout: "appended rejection r at version 3 to stream tree-t for event e; projection returned an error\n",
			stderr: []string{retryLine,
				"warning: rejection r at version 3 is in the stream and its projection returned an error: connection reset by peer. The tree's projected version is 2.\n",
				"rejection r at version 3 is in the stream and the store was not observed current; run tree reject-event again to project it"},
		},
		{
			name: "rejected with the store observed current", exit: 0,
			result: with(func(r *networkengine.RejectResult) {
				r.Outcome, r.RetryErr, r.ProjectionErr = networkengine.RejectOutcomeRejected, retry, boom
				r.Observed = &networkengine.ProjectionObservation{Version: 3, Found: true}
			}),
			stdout: "appended rejection r at version 3 to stream tree-t for event e; projection returned an error\n",
			stderr: []string{"The tree's projected version is 3.\n"},
		},
		{
			name: "resumed and projected", exit: 0,
			result: with(func(r *networkengine.RejectResult) { r.Outcome = networkengine.RejectOutcomeResumed }),
			stdout: "rejection r at version 3 for event e was pending; projected; nothing was appended\n",
		},
		{
			name: "resumed with no projection row observed", exit: 3,
			result: with(func(r *networkengine.RejectResult) {
				r.Outcome, r.ProjectionErr = networkengine.RejectOutcomeResumed, boom
				r.Observed = &networkengine.ProjectionObservation{}
			}),
			stdout: "rejection r at version 3 for event e was pending; projection returned an error; nothing was appended\n",
			stderr: []string{"The tree has no projection row.\n"},
		},
		{
			name: "already applied", exit: 0,
			result: with(func(r *networkengine.RejectResult) { r.Outcome = networkengine.RejectOutcomeAlreadyApplied }),
			stdout: "rejection r at version 3 for event e is already projected; nothing was appended\n",
		},
		{
			name: "refused", exit: 1, result: networkengine.RejectResult{Stream: "tree-t"},
			err:    fmt.Errorf("retrying event e at version 2 in stream tree-t returned no error; nothing was appended"),
			stderr: []string{"retrying event e at version 2 in stream tree-t returned no error; nothing was appended"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := &recordingWriter{rejectResult: c.result, err: c.err}

			out, err := runWriteCmd(t, w, "reject-event", "--tree-id", "t", "--event-id", "e", "--reason", "r")

			assert.Equal(t, c.exit, exitCode(err), "exit code; error: %v", err)
			assert.Equal(t, c.stdout, out.stdout.String())
			for _, want := range c.stderr {
				assert.Contains(t, out.stderr.String(), want)
			}
			if len(c.stderr) == 0 {
				assert.Empty(t, out.stderr.String())
			}
			assert.NotContains(t, out.stdout.String()+out.stderr.String(), "could not apply")
		})
	}
}

func TestTreeRejectEventCmd_ARejectionItCannotResumeExitsOne(t *testing.T) {
	for _, err := range []error{
		&networkengine.StreamMovedError{TreeID: "t", LoadedVersion: 9, LastVersion: 3},
		&networkengine.ProjectionMissingError{TreeID: "t", LastVersion: 3},
	} {
		w := &recordingWriter{rejectResult: networkengine.RejectResult{Stream: "tree-t"}, err: err}

		_, cmdErr := runWriteCmd(t, w, "reject-event", "--tree-id", "t", "--event-id", "e", "--reason", "r")

		assert.Equal(t, 1, exitCode(cmdErr), "%T", err)
	}
}

func TestRunArgs_MapsARejectionLeftBehindToExitThree(t *testing.T) {
	w := &recordingWriter{rejectResult: networkengine.RejectResult{
		Stream: "tree-t", Outcome: networkengine.RejectOutcomeResumed, RejectedEventID: "e", RejectedVersion: 2,
		EventID: "r", Version: 3, ProjectionErr: errors.New("connection reset by peer"),
		Observed: &networkengine.ProjectionObservation{Version: 1, Found: true},
	}}
	var warn bytes.Buffer

	code := runArgs(rootOver(t, w, &warn), []string{"tree", "reject-event", "--tree-id", "t", "--event-id", "e",
		"--reason", "r", "--db-url", "postgres://x", "--worker", workerStub(t)})

	assert.Equal(t, 3, code)
	assert.Contains(t, warn.String(), "The tree's projected version is 1.")
}

// cmdRejectionFailingStore fails its first failures calls to ProjectRejection.
type cmdRejectionFailingStore struct {
	networkengine.TreeStore
	failures int
}

func (s *cmdRejectionFailingStore) ProjectRejection(ctx context.Context, treeID, rejectedEventID string, eventVersion int64) error {
	if s.failures > 0 {
		s.failures--
		return errors.New("connection reset by peer")
	}
	return s.TreeStore.ProjectRejection(ctx, treeID, rejectedEventID, eventVersion)
}

func TestTreeRejectEventCmd_ExitsThreeUntilARerunProjects(t *testing.T) {
	tree, root, child, absent := testTreeID(856), testUserID(1), testUserID(2), testUserID(9)
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	events := platform.NewMemoryEventStore()
	store := networkengine.NewMemoryTreeStore()
	rootAdded := appendUnprojected(t, events, tree, 0, networkengine.EventTypeRootAdded, networkengine.RootAddedPayload{
		TreeID: tree, UserID: root, SponsorID: root, TreeType: "unilevel", EnrolledAt: at,
	})
	require.NoError(t, store.ProjectInsert(t.Context(), networkengine.TreeNodeRow{
		ID: rootAdded.ID, TreeID: tree, UserID: root, SponsorID: &root, EnrolledAt: at,
	}, 1))
	stuck := appendUnprojected(t, events, tree, 1, networkengine.EventTypeNodePlaced, networkengine.NodePlacedPayload{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: absent, TreeType: "unilevel", EnrolledAt: at,
	})
	// The stuck placement's row, committed at version 2.
	require.NoError(t, store.ProjectInsert(t.Context(), networkengine.TreeNodeRow{
		ID: stuck.ID, TreeID: tree, UserID: child, ParentID: &root, SponsorID: &absent, Depth: 1, EnrolledAt: at,
	}, 2))
	w := networkengine.NewTreeWriter(events, &cmdRejectionFailingStore{TreeStore: store, failures: 2},
		createOnlyEngine{}, networkengine.NewMemoryTreeLocker())

	var exits []int
	for range 4 {
		_, err := runWriteCmd(t, w, "reject-event", "--tree-id", tree, "--event-id", stuck.ID, "--reason", "sponsor never enrolled")
		exits = append(exits, exitCode(err))
	}

	assert.Equal(t, []int{3, 3, 0, 0}, exits)
}

func TestTreeRejectEventCmd_ARejectionNamingTheWrongEventExitsOne(t *testing.T) {
	tree, root, child := testTreeID(857), testUserID(1), testUserID(2)
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	events := platform.NewMemoryEventStore()
	store := networkengine.NewMemoryTreeStore()
	rootAdded := appendUnprojected(t, events, tree, 0, networkengine.EventTypeRootAdded, networkengine.RootAddedPayload{
		TreeID: tree, UserID: root, SponsorID: root, TreeType: "unilevel", EnrolledAt: at,
	})
	require.NoError(t, store.ProjectInsert(t.Context(), networkengine.TreeNodeRow{
		ID: rootAdded.ID, TreeID: tree, UserID: root, SponsorID: &root, EnrolledAt: at,
	}, 1))
	placed := appendUnprojected(t, events, tree, 1, networkengine.EventTypeNodePlaced, networkengine.NodePlacedPayload{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: root, TreeType: "unilevel", EnrolledAt: at,
	})
	require.NoError(t, store.ProjectInsert(t.Context(), networkengine.TreeNodeRow{
		ID: placed.ID, TreeID: tree, UserID: child, ParentID: &root, SponsorID: &root, Depth: 1, EnrolledAt: at,
	}, 2))
	other := uuid.NewString()
	appendUnprojected(t, events, tree, 2, networkengine.EventTypeEventRejected, networkengine.EventRejectedPayload{
		TreeID: tree, RejectedEventID: other, RejectedVersion: 2,
		RejectedType: networkengine.EventTypeNodePlaced, Reason: "appended by the test",
	})
	w := networkengine.NewTreeWriter(events, store, createOnlyEngine{}, networkengine.NewMemoryTreeLocker())

	out, err := runWriteCmd(t, w, "reject-event", "--tree-id", tree, "--event-id", other, "--reason", "r")

	assert.Equal(t, 1, exitCode(err))
	require.ErrorContains(t, err, "names event "+other)
	assert.Empty(t, out.stdout.String())
	row, err := store.GetNode(t.Context(), tree, child)
	require.NoError(t, err)
	assert.NotNil(t, row)
}

func TestTreeRejectEventCmd_HelpStatesTheMeaningAndTheExitCodes(t *testing.T) {
	cmd, _, err := newTreeCmd().Find([]string{"reject-event"})
	require.NoError(t, err)

	for _, want := range []string{
		"A rejection undoes the event's change to the tree.",
		"Rejecting a removal leaves the user in the tree.",
		"The tree type and matrix shape that version 1 records still apply after its root is rejected.",
		"Exits 0 when the rejection was projected without error or was already projected, or when the store was then observed at or past it.",
		"Exits 3 when it appended or found a pending rejection, tried to project it, and did not observe the store at or past it; run the command again to project it.",
		"On a rerun that finds a rejection already in the stream, --reason is required but not recorded: nothing is appended, and the rejection keeps its own reason.",
		"Exits 1 when it refused and appended nothing, or when it could not confirm whether its append landed. The cause is on stderr.",
	} {
		assert.Contains(t, cmd.Long, want)
	}
}

func TestTreeLoadFailureMessage_NamesAPendingRejection(t *testing.T) {
	pending := &networkengine.RejectionPendingError{
		TreeID: "t", RejectionID: "r", Version: 3, RejectedEventID: "e", Projected: 2,
		LoadErr: fmt.Errorf("load tree t; nothing was appended: %w",
			&networkengine.TreeLoadRejectedError{TreeID: "t", Kind: networkengine.TreeLoadDataInvalid}),
	}

	assert.Equal(t, "load stopped: "+pending.Error(), treeLoadFailureMessage(pending))
}

func TestTreeRejectEventCmd_PrintsARetryErrorTheRefusalDoesNotCarry(t *testing.T) {
	retry := errors.New("load tree t; nothing was appended: node u references sponsor s that is not in the tree")
	res := networkengine.RejectResult{Stream: "tree-t", RejectedEventID: "e", RejectedVersion: 2, RetryErr: retry}
	for _, c := range []struct {
		name  string
		err   error
		lines int
	}{
		{"a refusal that does not wrap it", errors.New("read the active rows of tree t: connection reset; nothing was appended"), 1},
		{"a refusal that wraps it", fmt.Errorf("retrying event e returned an error that is not evidence; nothing was appended: %w", retry), 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := &recordingWriter{rejectResult: res, err: c.err}

			out, err := runWriteCmd(t, w, "reject-event", "--tree-id", "t", "--event-id", "e", "--reason", "r")

			assert.Equal(t, 1, exitCode(err))
			assert.Equal(t, c.lines, strings.Count(out.stderr.String(), "retrying event e at version 2 returned: "+retry.Error()))
		})
	}
}

func TestTreeRejectEventCmd_RefusesAnOutcomeItDoesNotReport(t *testing.T) {
	w := &recordingWriter{rejectResult: networkengine.RejectResult{Stream: "tree-t"}}

	out, err := runWriteCmd(t, w, "reject-event", "--tree-id", "t", "--event-id", "e", "--reason", "r")

	require.EqualError(t, err, `reject returned no error and outcome "", which this command does not report`)
	assert.Empty(t, out.stdout.String())
}
