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
