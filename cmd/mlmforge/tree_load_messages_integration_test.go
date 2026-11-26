package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestTreeLoad_ReportsARealFailedRedelivery(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, child, absent := testTreeID(1115), testUserID(1), testUserID(2), testUserID(9)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root,
		"--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	stuck := appendUnprojected(t, platform.NewPostgresEventStore(pool), tree, 1, networkengine.EventTypeNodePlaced,
		networkengine.NodePlacedPayload{TreeID: tree, UserID: child, ParentID: root, SponsorID: absent,
			TreeType: "unilevel", EnrolledAt: rejectTime})

	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)

	require.Equal(t, 1, exitCode(err), "stdout: %s stderr: %s", out.stdout.String(), out.stderr.String())
	var failed *networkengine.CatchUpFailedError
	require.ErrorAs(t, err, &failed)
	require.Empty(t, out.stdout.String())
	stderr := out.stderr.String()
	require.Regexp(t, "^"+regexp.QuoteMeta("Error: load failed: redelivering event "+stuck.ID+
		" (tree.node_placed) at version 2 in stream tree-"+tree+" returned: "), stderr)
	require.True(t, strings.HasSuffix(stderr, "; nothing was appended\n"), "stderr: %s", stderr)
	require.Equal(t, 1, strings.Count(stderr, "\n"), "stderr: %s", stderr)
	require.Contains(t, stderr, "SPONSOR_NOT_FOUND")
}

func TestTreeLoad_RefusesATreeTypeTheStreamDoesNotRecord(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	_ = pgContainer.NewPool(t)
	tree := testTreeID(1116)
	seedTree(t, worker, tree, 0)

	out, err := runTreeCmd(t, "load", "--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree,
		"--tree-type", "binary")

	require.Equal(t, 1, exitCode(err), "stdout: %s stderr: %s", out.stdout.String(), out.stderr.String())
	require.Empty(t, out.stdout.String())
	require.Equal(t, "Error: load failed: load tree "+tree+": stream tree-"+tree+
		" records tree type unilevel at version 1, and the request names binary\n", out.stderr.String())
}

func addMatrixRoot(t *testing.T, worker, tree string) {
	t.Helper()
	root := testUserID(1)
	out, err := runTreeCmd(t, "add-root", "--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree,
		"--user-id", root, "--sponsor-id", root, "--tree-type", "matrix",
		"--matrix-width", "2", "--matrix-spillover", "breadth_first")
	require.NoError(t, err, out.stderr.String())
}

func TestTreeLoad_RefusesAMatrixWidthTheStreamDoesNotRecord(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	_ = pgContainer.NewPool(t)
	tree := testTreeID(1117)
	addMatrixRoot(t, worker, tree)

	out, err := runTreeCmd(t, "load", "--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree,
		"--tree-type", "matrix", "--matrix-width", "3")

	require.Equal(t, 1, exitCode(err), "stdout: %s stderr: %s", out.stdout.String(), out.stderr.String())
	require.Empty(t, out.stdout.String())
	require.Equal(t, "Error: load failed: load tree "+tree+": stream tree-"+tree+
		" records matrix width 2 at version 1, and the request names 3\n", out.stderr.String())
}

func TestTreeLoad_LoadsAMatrixTreeWithTheMatrixFlagsLeftOut(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	_ = pgContainer.NewPool(t)
	tree := testTreeID(1118)
	addMatrixRoot(t, worker, tree)

	out, err := runTreeCmd(t, "load", "--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree,
		"--tree-type", "matrix")

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "loaded tree "+tree+" (1 nodes)\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
}
