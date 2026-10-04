package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

// appendCorrectRejection appends a correct rejection of stuck and projects
// nothing.
func appendCorrectRejection(t *testing.T, events platform.EventStore, tree string, stuck platform.Event) platform.Event {
	t.Helper()
	return appendUnprojected(t, events, tree, stuck.Version, networkengine.EventTypeEventRejected, networkengine.EventRejectedPayload{
		TreeID: tree, RejectedEventID: stuck.ID, RejectedVersion: stuck.Version,
		RejectedType: stuck.Type, Reason: "appended by the test",
	})
}

// requirePendingRefusal requires a write and tree load on a tree with a pending
// rejection to refuse and name reject-event. loadErr is the load error text the
// refusal carries, or empty when it carries none.
func requirePendingRefusal(t *testing.T, events platform.EventStore, store networkengine.TreeStore, tree, stuckID string,
	projected int64, loadErr string, conn, write []string) {
	t.Helper()
	requireProjectedVersion(t, store, tree, projected)
	want := "Run mlmforge tree reject-event --tree-id " + tree + " --event-id " + stuckID + " --reason <text> again to project it"
	before := readTreeStream(t, events, tree)
	var pending *networkengine.RejectionPendingError

	out, err := runTreeCmd(t, write...)
	require.ErrorAs(t, err, &pending, "the write: %s", out.stderr.String())
	require.ErrorContains(t, err, want)
	require.Equal(t, 1, exitCode(err), "the write's exit code")
	require.Contains(t, out.stderr.String(), want, "the write did not print the reject-event instruction")
	require.Empty(t, out.stdout.String())
	requirePendingLoadErr(t, pending, loadErr, "the write")

	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)
	require.ErrorAs(t, err, &pending, "tree load: %s", out.stderr.String())
	require.True(t, strings.HasPrefix(err.Error(), "load stopped: "), err.Error())
	requirePendingLoadErr(t, pending, loadErr, "tree load")
	require.ErrorContains(t, err, want)
	require.Equal(t, 1, exitCode(err), "tree load's exit code")
	require.Contains(t, out.stderr.String(), want, "tree load did not print the reject-event instruction")
	require.Empty(t, out.stdout.String())
	require.Equal(t, before, readTreeStream(t, events, tree), "a refused command changed the stream")
}

// requirePendingLoadErr requires pending to carry a load error containing want,
// or none when want is empty.
func requirePendingLoadErr(t *testing.T, pending *networkengine.RejectionPendingError, want, cmd string) {
	t.Helper()
	if want == "" {
		require.NoError(t, pending.LoadErr, "%s's pending error carries a load error", cmd)
		return
	}
	require.ErrorContains(t, pending.LoadErr, want, "%s's pending error", cmd)
}

// requireResumed runs reject-event over a pending rejection and checks it
// projected without appending.
func requireResumed(t *testing.T, events platform.EventStore, store networkengine.TreeStore, tree string, stuck, rejection platform.Event, conn []string) {
	t.Helper()
	before := readTreeStream(t, events, tree)
	out, err := runTreeCmd(t, append([]string{"reject-event", "--event-id", stuck.ID, "--reason", "resume"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "rejection "+rejection.ID+" at version "+strconv.FormatInt(rejection.Version, 10)+" for event "+stuck.ID+
		" was pending; projected; nothing was appended\n", out.stdout.String())
	require.Equal(t, before, readTreeStream(t, events, tree))
	requireProjectedVersion(t, store, tree, rejection.Version)
}

func TestTreeRejectEvent_APendingPlacementRejectionNamesTheCommand(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, child, next, absent := testTreeID(853), testUserID(1), testUserID(2), testUserID(3), testUserID(9)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root, "--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	events := platform.NewPostgresEventStore(pool)
	stuck := appendUnprojected(t, events, tree, 1, networkengine.EventTypeNodePlaced, networkengine.NodePlacedPayload{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: absent, TreeType: "unilevel", EnrolledAt: rejectTime,
	})
	place := append([]string{"place", "--user-id", next, "--parent-id", root, "--sponsor-id", root}, conn...)
	_, err = runTreeCmd(t, place...)
	require.ErrorContains(t, err, "USER_NOT_FOUND")
	rejection := appendCorrectRejection(t, events, tree, stuck)

	store := networkengine.NewPostgresTreeStore(pool)
	requirePendingRefusal(t, events, store, tree, stuck.ID, 2, "references sponsor "+absent+" that is not in the tree", conn, place)
	requireResumed(t, events, store, tree, stuck, rejection, conn)

	out, err = runTreeCmd(t, place...)
	require.NoError(t, err, out.stderr.String())
}

func TestTreeRejectEvent_APendingRootRejectionNamesTheCommand(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, second := testTreeID(854), testUserID(1), testUserID(2)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	events := platform.NewPostgresEventStore(pool)
	store := networkengine.NewPostgresTreeStore(pool)
	rootAdded := appendUnprojected(t, events, tree, 0, networkengine.EventTypeRootAdded, networkengine.RootAddedPayload{
		TreeID: tree, UserID: root, SponsorID: root, TreeType: "unilevel", EnrolledAt: rejectTime,
	})
	require.NoError(t, store.ProjectInsert(t.Context(), networkengine.TreeNodeRow{
		ID: rootAdded.ID, TreeID: tree, UserID: root, SponsorID: &root, Depth: 0, EnrolledAt: rejectTime,
	}, 1))
	require.NoError(t, store.UndoRootProjection(t.Context(), tree, root, 1))
	rejection := appendCorrectRejection(t, events, tree, rootAdded)
	addRoot := append([]string{"add-root", "--user-id", second, "--sponsor-id", second, "--tree-type", "unilevel"}, conn...)

	requirePendingRefusal(t, events, store, tree, rootAdded.ID, 0, "", conn, addRoot)
	requireResumed(t, events, store, tree, rootAdded, rejection, conn)

	out, err := runTreeCmd(t, addRoot...)
	require.NoError(t, err, out.stderr.String())
}

func TestTreeRejectEvent_APendingRemovalRejectionNamesTheCommand(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, child, grandchild, next := testTreeID(855), testUserID(1), testUserID(2), testUserID(3), testUserID(4)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	for _, args := range [][]string{
		{"add-root", "--user-id", root, "--sponsor-id", root, "--tree-type", "unilevel"},
		{"place", "--user-id", child, "--parent-id", root, "--sponsor-id", root},
		{"place", "--user-id", grandchild, "--parent-id", child, "--sponsor-id", child},
	} {
		out, err := runTreeCmd(t, append(args, conn...)...)
		require.NoError(t, err, out.stderr.String())
	}
	events := platform.NewPostgresEventStore(pool)
	removal := appendUnprojected(t, events, tree, 3, networkengine.EventTypeNodeRemoved, networkengine.NodeRemovedPayload{
		TreeID: tree, UserID: child, RemovedAt: rejectTime,
	})
	place := append([]string{"place", "--user-id", next, "--parent-id", root, "--sponsor-id", root}, conn...)
	_, err := runTreeCmd(t, place...)
	require.ErrorContains(t, err, "HAS_CHILDREN")
	rejection := appendCorrectRejection(t, events, tree, removal)

	store := networkengine.NewPostgresTreeStore(pool)
	requirePendingRefusal(t, events, store, tree, removal.ID, 3, "", conn, place)
	requireResumed(t, events, store, tree, removal, rejection, conn)

	out, err := runTreeCmd(t, place...)
	require.NoError(t, err, out.stderr.String())
}
