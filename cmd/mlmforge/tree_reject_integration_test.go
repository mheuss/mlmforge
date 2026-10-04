package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

// rejectTime is the enrolment and removal time these tests write.
var rejectTime = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

// readTreeStream reads a tree's whole stream.
func readTreeStream(t *testing.T, events platform.EventStore, tree string) []platform.Event {
	t.Helper()
	got, err := events.ReadStream(t.Context(), networkengine.TreeStreamName(tree), 1, 0)
	require.NoError(t, err)
	return got
}

// requireGrewByTheRejection checks that after is before with one rejection of
// rejected appended, and returns that rejection.
func requireGrewByTheRejection(t *testing.T, before, after []platform.Event, rejected string) platform.Event {
	t.Helper()
	require.Len(t, after, len(before)+1, "the stream did not grow by exactly one event")
	require.Equal(t, before, after[:len(before)], "an earlier event changed")
	last := after[len(after)-1]
	require.Equal(t, networkengine.EventTypeEventRejected, last.Type)
	var p networkengine.EventRejectedPayload
	require.NoError(t, json.Unmarshal(last.Payload, &p))
	require.Equal(t, rejected, p.RejectedEventID)
	return last
}

func requireProjectedVersion(t *testing.T, store networkengine.TreeStore, tree string, want int64) {
	t.Helper()
	got, found, err := store.ProjectedVersion(t.Context(), tree)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, want, got)
}

func TestTreeRejectEvent_RecoversARefusedPlacement(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, child, next, absent := testTreeID(850), testUserID(1), testUserID(2), testUserID(3), testUserID(9)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root, "--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	events := platform.NewPostgresEventStore(pool)
	store := networkengine.NewPostgresTreeStore(pool)
	stuck := appendUnprojected(t, events, tree, 1, networkengine.EventTypeNodePlaced, networkengine.NodePlacedPayload{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: absent, TreeType: "unilevel", EnrolledAt: rejectTime,
	})
	place := append([]string{"place", "--user-id", next, "--parent-id", root, "--sponsor-id", root}, conn...)
	_, err = runTreeCmd(t, place...)
	var failed *networkengine.CatchUpFailedError
	require.ErrorAs(t, err, &failed, "the first write after the append")
	require.ErrorContains(t, err, "USER_NOT_FOUND")
	_, err = runTreeCmd(t, place...)
	require.ErrorContains(t, err, "references sponsor "+absent+" that is not in the tree", "the second write")
	before := readTreeStream(t, events, tree)

	out, err = runTreeCmd(t, append([]string{"reject-event", "--event-id", stuck.ID, "--reason", "sponsor never enrolled"}, conn...)...)

	require.NoError(t, err, out.stderr.String())
	rejection := requireGrewByTheRejection(t, before, readTreeStream(t, events, tree), stuck.ID)
	var p networkengine.EventRejectedPayload
	require.NoError(t, json.Unmarshal(rejection.Payload, &p))
	require.Equal(t, networkengine.EventRejectedPayload{
		TreeID: tree, RejectedEventID: stuck.ID, RejectedVersion: 2,
		RejectedType: networkengine.EventTypeNodePlaced, Reason: "sponsor never enrolled",
	}, p)
	require.Equal(t, "appended rejection "+rejection.ID+" at version 3 to stream "+networkengine.TreeStreamName(tree)+
		" for event "+stuck.ID+"; projected\n", out.stdout.String())
	require.Contains(t, out.stderr.String(), "retrying event "+stuck.ID+" at version 2 returned: load tree "+tree)
	row, err := store.GetNodeIncludingRemoved(t.Context(), tree, child)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, stuck.ID, row.ID)
	require.NotNil(t, row.RemovedAt, "the stuck placement's row is still active")
	require.Nil(t, row.RemovedByEventID)
	requireProjectedVersion(t, store, tree, 3)

	out, err = runTreeCmd(t, place...)
	require.NoError(t, err, out.stderr.String())
	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "loaded tree "+tree+" (2 nodes)\n", out.stdout.String())
}

func TestTreeRejectEvent_RecoversACompensatedRoot(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, second := testTreeID(851), testUserID(1), testUserID(2)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	events := platform.NewPostgresEventStore(pool)
	store := networkengine.NewPostgresTreeStore(pool)
	rootAdded := appendUnprojected(t, events, tree, 0, networkengine.EventTypeRootAdded, networkengine.RootAddedPayload{
		TreeID: tree, UserID: root, SponsorID: root, TreeType: "unilevel", EnrolledAt: rejectTime,
	})
	// The compensated state: the root's row inserted, then undone.
	require.NoError(t, store.ProjectInsert(t.Context(), networkengine.TreeNodeRow{
		ID: rootAdded.ID, TreeID: tree, UserID: root, SponsorID: &root, Depth: 0, EnrolledAt: rejectTime,
	}, 1))
	require.NoError(t, store.UndoRootProjection(t.Context(), tree, root, 1))
	before := readTreeStream(t, events, tree)

	out, err := runTreeCmd(t, append([]string{"reject-event", "--event-id", rootAdded.ID, "--reason", "engine refused the root"}, conn...)...)

	require.NoError(t, err, out.stderr.String())
	rejection := requireGrewByTheRejection(t, before, readTreeStream(t, events, tree), rootAdded.ID)
	require.Contains(t, out.stderr.String(), "retrying event "+rootAdded.ID+" at version 1 returned: redelivering event "+rootAdded.ID)
	requireProjectedVersion(t, store, tree, 2)

	_, err = runTreeCmd(t, append([]string{"add-root", "--user-id", second, "--sponsor-id", second, "--tree-type", "binary"}, conn...)...)
	require.ErrorContains(t, err, "records tree type unilevel at version 1, and the request names binary")
	out, err = runTreeCmd(t, append([]string{"add-root", "--user-id", second, "--sponsor-id", second, "--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	require.Contains(t, out.stdout.String(), "redelivered event "+rejection.ID+" at version 2\nappended event ")
	active, err := store.GetNode(t.Context(), tree, second)
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, 0, active.Depth)
}

func TestTreeRejectEvent_RecoversARefusedRemoval(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, child, grandchild, next := testTreeID(852), testUserID(1), testUserID(2), testUserID(3), testUserID(4)
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
	store := networkengine.NewPostgresTreeStore(pool)
	childRow, err := store.GetNode(t.Context(), tree, child)
	require.NoError(t, err)
	require.NotNil(t, childRow)
	removal := appendUnprojected(t, events, tree, 3, networkengine.EventTypeNodeRemoved, networkengine.NodeRemovedPayload{
		TreeID: tree, UserID: child, RemovedAt: rejectTime,
	})
	_, err = runTreeCmd(t, append([]string{"place", "--user-id", next, "--parent-id", root, "--sponsor-id", root}, conn...)...)
	var failed *networkengine.CatchUpFailedError
	require.ErrorAs(t, err, &failed)
	require.ErrorContains(t, err, "HAS_CHILDREN")
	before := readTreeStream(t, events, tree)

	out, err := runTreeCmd(t, append([]string{"reject-event", "--event-id", removal.ID, "--reason", "the user still has a recruit"}, conn...)...)

	require.NoError(t, err, out.stderr.String())
	requireGrewByTheRejection(t, before, readTreeStream(t, events, tree), removal.ID)
	require.Contains(t, out.stderr.String(), "retrying event "+removal.ID+" at version 4 returned: redelivering event "+removal.ID)
	row, err := store.GetNode(t.Context(), tree, child)
	require.NoError(t, err)
	require.NotNil(t, row, "rejecting the removal took the user out of the tree")
	require.Equal(t, childRow.ID, row.ID)
	requireProjectedVersion(t, store, tree, 5)

	out, err = runTreeCmd(t, append([]string{"place", "--user-id", next, "--parent-id", child, "--sponsor-id", child}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "loaded tree "+tree+" (4 nodes)\n", out.stdout.String())
}
