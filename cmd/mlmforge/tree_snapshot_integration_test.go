package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

// snapshotVersion is the version this test drives the stream to.
const snapshotVersion = 1000

var snapshotEnrolledAt = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

// seedProjectedPlacements appends and projects one placement under root at
// each version from through to.
func seedProjectedPlacements(t *testing.T, pool *pgxpool.Pool, tree, root string, from, to int) {
	t.Helper()
	events := platform.NewPostgresEventStore(pool)
	store := networkengine.NewPostgresTreeStore(pool)
	stream := networkengine.TreeStreamName(tree)
	for v := from; v <= to; v++ {
		user := testUserID(v)
		data, err := json.Marshal(networkengine.NodePlacedPayload{TreeID: tree, UserID: user, ParentID: root,
			SponsorID: root, TreeType: "unilevel", EnrolledAt: snapshotEnrolledAt})
		require.NoError(t, err)
		id := uuid.NewString()
		require.NoError(t, events.Append(t.Context(), stream, int64(v-1), []platform.NewEvent{{
			ID: id, Type: networkengine.EventTypeNodePlaced, Payload: data,
		}}))
		parent := root
		require.NoError(t, store.ProjectInsert(t.Context(), networkengine.TreeNodeRow{
			ID: id, TreeID: tree, UserID: user, ParentID: &parent, SponsorID: &parent, Depth: 1,
			EnrolledAt: snapshotEnrolledAt,
		}, int64(v)))
	}
}

// snapshotAtTheInterval builds a unilevel tree whose CLI write at
// snapshotVersion stores a snapshot, and returns the tree's connection flags.
func snapshotAtTheInterval(t *testing.T, pool *pgxpool.Pool, worker, tree string) []string {
	t.Helper()
	root := testUserID(1)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root,
		"--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	seedProjectedPlacements(t, pool, tree, root, 2, snapshotVersion-1)

	out, err = runTreeCmd(t, append([]string{"place", "--user-id", testUserID(snapshotVersion), "--parent-id", root,
		"--sponsor-id", root}, conn...)...)

	require.NoError(t, err, out.stderr.String())
	require.Empty(t, out.stderr.String())
	var version int64
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT version FROM tree_snapshots WHERE tree_id = $1`, tree).
		Scan(&version), "reading tree %s's tree_snapshots row after the CLI write at version %d", tree, snapshotVersion)
	require.Equal(t, int64(snapshotVersion), version)
	return conn
}

func TestTreeLoad_RestoresAStoredSnapshot(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, absent := testTreeID(1109), testUserID(1), testUserID(5000)
	conn := snapshotAtTheInterval(t, pool, worker, tree)
	stream := networkengine.TreeStreamName(tree)
	second, err := platform.NewPostgresEventStore(pool).ReadStream(t.Context(), stream, 2, 1)
	require.NoError(t, err)
	require.Len(t, second, 1)
	// The overwritten placement names a sponsor the tree never held.
	data, err := json.Marshal(networkengine.NodePlacedPayload{TreeID: tree, UserID: testUserID(2), ParentID: root,
		SponsorID: absent, TreeType: "unilevel", EnrolledAt: snapshotEnrolledAt})
	require.NoError(t, err)
	tag, err := pool.Exec(t.Context(), `UPDATE events SET payload = $1::jsonb WHERE stream = $2 AND version = 2`,
		string(data), stream)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())
	load := append([]string{"load", "--tree-type", "unilevel"}, conn...)

	out, err := runTreeCmd(t, load...)

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "loaded tree "+tree+" (1000 nodes)\n", out.stdout.String())
	require.Empty(t, out.stderr.String())

	tag, err = pool.Exec(t.Context(), `DELETE FROM tree_snapshots WHERE tree_id = $1`, tree)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())

	out, err = runTreeCmd(t, load...)

	require.Equal(t, 1, exitCode(err), "stdout: %s stderr: %s", out.stdout.String(), out.stderr.String())
	require.Empty(t, out.stdout.String())
	require.Contains(t, out.stderr.String(),
		"load stopped at the replay stage while building the tree from its stream: build tree "+tree+" at stage replay: ")
	require.Contains(t, out.stderr.String(), "; event "+second[0].ID+" at version 2 of type tree.node_placed failed: ")
	require.Contains(t, out.stderr.String(), "SPONSOR_NOT_FOUND")
}

func TestTreeLoad_FallsBackFromASnapshotTheWorkerRefuses(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree := testTreeID(1110)
	conn := snapshotAtTheInterval(t, pool, worker, tree)
	tag, err := pool.Exec(t.Context(), `UPDATE tree_snapshots SET data = convert_to('{}', 'UTF8') WHERE tree_id = $1`, tree)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())

	out, err := runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "loaded tree "+tree+" (1000 nodes)\n", out.stdout.String())
	stderr := out.stderr.String()
	require.Contains(t, stderr, "warning: the snapshot of tree "+tree+" at version 1000 could not be used (build tree "+
		tree+" at stage restore: ")
	require.Contains(t, stderr, "engine error [INVALID_PARAMS]: failed to deserialize unilevel snapshot")
	require.Contains(t, stderr, "); dropped the structure; deleted the snapshot\n")
	require.Equal(t, 1, strings.Count(stderr, "warning: "), "stderr: %s", stderr)
	var left int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM tree_snapshots WHERE tree_id = $1`, tree).Scan(&left))
	require.Zero(t, left)
}
