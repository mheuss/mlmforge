package main

import (
	"testing"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestTreeRejectEvent_RefusesAHealthyLastEvent(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree := testTreeID(1113)
	seedTree(t, worker, tree, 1)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	events := platform.NewPostgresEventStore(pool)
	store := networkengine.NewPostgresTreeStore(pool)
	before := readTreeStream(t, events, tree)
	require.Len(t, before, 2)
	placed := before[1]
	require.Equal(t, networkengine.EventTypeNodePlaced, placed.Type)

	out, err := runTreeCmd(t, append([]string{"reject-event", "--event-id", placed.ID,
		"--reason", "the placement is healthy"}, conn...)...)

	require.Equal(t, 1, exitCode(err), "stdout: %s stderr: %s", out.stdout.String(), out.stderr.String())
	require.Empty(t, out.stdout.String())
	require.Contains(t, out.stderr.String(), "retrying event "+placed.ID+" at version 2 in stream tree-"+tree+
		" returned no error; nothing was appended")
	require.Equal(t, before, readTreeStream(t, events, tree))
	requireProjectedVersion(t, store, tree, 2)
}

func TestTreeRejectEvent_ARerunAfterARejectionIsAlreadyProjected(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, child, absent := testTreeID(1114), testUserID(1), testUserID(2), testUserID(9)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root,
		"--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	events := platform.NewPostgresEventStore(pool)
	store := networkengine.NewPostgresTreeStore(pool)
	stuck := appendUnprojected(t, events, tree, 1, networkengine.EventTypeNodePlaced, networkengine.NodePlacedPayload{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: absent, TreeType: "unilevel", EnrolledAt: rejectTime,
	})
	reject := append([]string{"reject-event", "--event-id", stuck.ID, "--reason", "sponsor never enrolled"}, conn...)
	unrejected := readTreeStream(t, events, tree)
	out, err = runTreeCmd(t, reject...)
	require.NoError(t, err, out.stderr.String())
	before := readTreeStream(t, events, tree)
	rejection := requireGrewByTheRejection(t, unrejected, before, stuck.ID)
	require.Equal(t, "appended rejection "+rejection.ID+" at version 3 to stream tree-"+tree+" for event "+stuck.ID+
		"; projected\n", out.stdout.String())

	out, err = runTreeCmd(t, reject...)

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "rejection "+rejection.ID+" at version 3 for event "+stuck.ID+
		" is already projected; nothing was appended\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
	require.Equal(t, before, readTreeStream(t, events, tree))
	requireProjectedVersion(t, store, tree, 3)
}
