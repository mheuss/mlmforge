package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

// appendUnprojected appends one event to a tree's stream and projects nothing.
func appendUnprojected(t *testing.T, events platform.EventStore, tree string, expected int64, eventType string, payload any) platform.Event {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	stream := networkengine.TreeStreamName(tree)
	require.NoError(t, events.Append(t.Context(), stream, expected, []platform.NewEvent{{
		ID: uuid.NewString(), Type: eventType, Payload: data,
	}}))
	stored, err := events.ReadStream(t.Context(), stream, expected+1, 1)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	return stored[0]
}

// failingRemovalStore fails its first ProjectRemoval and passes every other
// call through.
type failingRemovalStore struct {
	networkengine.TreeStore
	removals int
}

func (s *failingRemovalStore) ProjectRemoval(ctx context.Context, treeID, userID, removalEventID string,
	eventVersion int64, moved []networkengine.Responsored) error {
	s.removals++
	if s.removals == 1 {
		return errors.New("failingRemovalStore: first ProjectRemoval refused")
	}
	return s.TreeStore.ProjectRemoval(ctx, treeID, userID, removalEventID, eventVersion, moved)
}

// streamLastVersion reads the version of a tree stream's last event, or 0 for
// an empty stream.
func streamLastVersion(t *testing.T, events platform.EventStore, tree string) int64 {
	t.Helper()
	last, err := events.ReadLastEvent(t.Context(), networkengine.TreeStreamName(tree))
	require.NoError(t, err)
	if last == nil {
		return 0
	}
	return last.Version
}

// failingAddEngine refuses every placement and passes every other call through.
type failingAddEngine struct {
	networkengine.TreeEngineChecker
}

func (failingAddEngine) AddNode(context.Context, string, string, string, string, int64, ...networkengine.AddNodeOption) error {
	return errors.New("failingAddEngine: add_node refused")
}

// activeSponsorsOf maps each active user in tree to the sponsor its row names.
func activeSponsorsOf(t *testing.T, store networkengine.TreeStore, tree string) map[string]string {
	t.Helper()
	rows, err := store.GetByTree(t.Context(), tree)
	require.NoError(t, err)
	sponsors := make(map[string]string, len(rows))
	for _, r := range rows {
		require.NotNil(t, r.SponsorID, "the active row for %s has no sponsor", r.UserID)
		sponsors[r.UserID] = *r.SponsorID
	}
	return sponsors
}

func TestTreeLoad_RedeliversAPlacementTheStoreDidNotProject(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, child := testTreeID(40), testUserID(1), testUserID(2)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root,
		"--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	events := platform.NewPostgresEventStore(pool)
	placed := appendUnprojected(t, events, tree, 1, networkengine.EventTypeNodePlaced, networkengine.NodePlacedPayload{
		TreeID: tree, UserID: child, ParentID: root, SponsorID: root,
		TreeType: "unilevel", EnrolledAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
	})

	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "redelivered event "+placed.ID+" at version 2\nloaded tree "+tree+" (2 nodes)\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
	store := networkengine.NewPostgresTreeStore(pool)
	row, err := store.GetNode(t.Context(), tree, child)
	require.NoError(t, err)
	require.NotNil(t, row, "the redelivered placement left no row for %s", child)
	version, _, err := store.ProjectedVersion(t.Context(), tree)
	require.NoError(t, err)
	require.Equal(t, int64(2), version)
}

// The engine applies the removal and the store write fails. A load then has
// to rebuild the engine from the store and redeliver the removal, which moves
// C onto A.
func TestTreeLoad_RedeliversARemovalWhoseStoreWriteFailed(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, a, b, c := testTreeID(41), testUserID(1), testUserID(2), testUserID(3), testUserID(4)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root,
		"--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	for _, p := range []struct{ user, sponsor string }{{a, root}, {b, a}, {c, b}} {
		out, err = runTreeCmd(t, append([]string{"place", "--user-id", p.user, "--parent-id", root,
			"--sponsor-id", p.sponsor}, conn...)...)
		require.NoError(t, err, out.stderr.String())
	}
	engine, err := networkengine.NewEngineClient(t.Context(), worker)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Stop() })
	store := networkengine.NewPostgresTreeStore(pool)
	failing := &failingRemovalStore{TreeStore: store}
	w := networkengine.NewTreeWriter(platform.NewPostgresEventStore(pool), failing, engine,
		networkengine.NewPostgresTreeLocker(pgContainer.DSN))
	res, err := w.Remove(t.Context(), networkengine.RemoveRequest{TreeID: tree, UserID: b,
		RemovedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	require.Error(t, res.ProjectionErr)
	require.Equal(t, 1, failing.removals, "the store double's failing call was never reached")
	require.Equal(t, &networkengine.ProjectionObservation{Version: 4, Found: true}, res.Observed)
	require.Equal(t, map[string]string{root: root, a: root, b: a, c: b}, activeSponsorsOf(t, store, tree))

	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "redelivered event "+res.EventID+" at version 5\nloaded tree "+tree+" (3 nodes)\n", out.stdout.String())
	require.Equal(t, map[string]string{root: root, a: root, c: a}, activeSponsorsOf(t, store, tree))
	removed, err := store.GetNodeIncludingRemoved(t.Context(), tree, b)
	require.NoError(t, err)
	require.NotNil(t, removed)
	require.NotNil(t, removed.RemovedAt, "B's row is not tombstoned")
	version, _, err := store.ProjectedVersion(t.Context(), tree)
	require.NoError(t, err)
	require.Equal(t, streamLastVersion(t, platform.NewPostgresEventStore(pool), tree), version)
}

func TestTreeLoad_PrintsTheSameLineWhenNothingIsBehind(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	_ = pgContainer.NewPool(t)
	tree, root, child := testTreeID(777), testUserID(1), testUserID(2)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root,
		"--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	out, err = runTreeCmd(t, append([]string{"place", "--user-id", child, "--parent-id", root,
		"--sponsor-id", root}, conn...)...)
	require.NoError(t, err, out.stderr.String())

	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)

	require.NoError(t, err)
	require.Equal(t, "loaded tree aaaaaaaa-aaaa-aaaa-aaaa-000000000777 (2 nodes)\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
}

func TestTreeLoad_RefusesATreeWithNoProjectionRow(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, child := testTreeID(43), testUserID(1), testUserID(2)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root,
		"--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	out, err = runTreeCmd(t, append([]string{"place", "--user-id", child, "--parent-id", root,
		"--sponsor-id", root}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	_, err = pool.Exec(t.Context(), `DELETE FROM tree_projections WHERE tree_id = $1`, tree)
	require.NoError(t, err)
	store := networkengine.NewPostgresTreeStore(pool)
	events := platform.NewPostgresEventStore(pool)
	before := activeSponsorsOf(t, store, tree)

	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)

	var missing *networkengine.ProjectionMissingError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, 1, exitCode(err))
	require.Empty(t, out.stdout.String())
	require.Contains(t, out.stderr.String(), "has no projection row and stream tree-"+tree+" ends at version 2")
	_, found, err := store.ProjectedVersion(t.Context(), tree)
	require.NoError(t, err)
	require.False(t, found, "the refused load wrote a projection row")
	require.Equal(t, before, activeSponsorsOf(t, store, tree))
	require.Equal(t, int64(2), streamLastVersion(t, events, tree))
}

func TestTreeLoad_RefusesAStreamTwoPastTheStore(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root := testTreeID(44), testUserID(1)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root,
		"--tree-type", "unilevel"}, conn...)...)
	require.NoError(t, err, out.stderr.String())
	events := platform.NewPostgresEventStore(pool)
	for i, user := range []string{testUserID(2), testUserID(3)} {
		appendUnprojected(t, events, tree, int64(i+1), networkengine.EventTypeNodePlaced, networkengine.NodePlacedPayload{
			TreeID: tree, UserID: user, ParentID: root, SponsorID: root,
			TreeType: "unilevel", EnrolledAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		})
	}

	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)

	var moved *networkengine.StreamMovedError
	require.ErrorAs(t, err, &moved)
	require.Equal(t, 1, exitCode(err))
	require.Empty(t, out.stdout.String())
	store := networkengine.NewPostgresTreeStore(pool)
	version, _, err := store.ProjectedVersion(t.Context(), tree)
	require.NoError(t, err)
	require.Equal(t, int64(1), version)
	require.Equal(t, map[string]string{root: root}, activeSponsorsOf(t, store, tree))
	require.Equal(t, int64(3), streamLastVersion(t, events, tree))
}

func TestTreeLoad_RefusesAStreamBehindTheStore(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root := testTreeID(47), testUserID(1)
	store := networkengine.NewPostgresTreeStore(pool)
	require.NoError(t, store.ProjectInsert(t.Context(), networkengine.TreeNodeRow{
		ID: uuid.NewString(), TreeID: tree, UserID: root, SponsorID: &root,
		EnrolledAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
	}, 1))

	out, err := runTreeCmd(t, "load", "--db-url", pgContainer.DSN, "--worker", worker,
		"--tree-id", tree, "--tree-type", "unilevel")

	var moved *networkengine.StreamMovedError
	require.ErrorAs(t, err, &moved)
	require.Equal(t, 1, exitCode(err))
	require.Empty(t, out.stdout.String())
	version, _, err := store.ProjectedVersion(t.Context(), tree)
	require.NoError(t, err)
	require.Equal(t, int64(1), version)
	require.Equal(t, map[string]string{root: root}, activeSponsorsOf(t, store, tree))
	require.Equal(t, int64(0), streamLastVersion(t, platform.NewPostgresEventStore(pool), tree))
}

func TestTreeLoad_PrintsTheSameLineForAnEmptyStream(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	_ = pgContainer.NewPool(t)

	out, err := runTreeCmd(t, "load", "--db-url", pgContainer.DSN, "--worker", worker,
		"--tree-id", "bbbbbbbb-bbbb-bbbb-bbbb-000000000777", "--tree-type", "unilevel")

	require.NoError(t, err)
	require.Equal(t, "tree bbbbbbbb-bbbb-bbbb-bbbb-000000000777 holds no rows; nothing was loaded\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
}

// A placement commits its row and version before its engine call. When the
// engine then fails, the write command has to exit 0 and say the store is
// current.
func TestTreePlace_ExitsZeroWhenTheEngineFailsAfterTheInsertCommits(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree, root, child := testTreeID(46), testUserID(1), testUserID(2)
	out, err := runTreeCmd(t, "add-root", "--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree,
		"--user-id", root, "--sponsor-id", root, "--tree-type", "unilevel")
	require.NoError(t, err, out.stderr.String())
	engine, err := networkengine.NewEngineClient(t.Context(), worker)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Stop() })
	w := networkengine.NewTreeWriter(platform.NewPostgresEventStore(pool), networkengine.NewPostgresTreeStore(pool),
		failingAddEngine{TreeEngineChecker: engine}, networkengine.NewPostgresTreeLocker(pgContainer.DSN))
	res, err := w.Place(t.Context(), networkengine.PlaceRequest{TreeID: tree, UserID: child, ParentID: root,
		SponsorID: root, EnrolledAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)})
	var stdout, stderr bytes.Buffer

	cliErr := reportWrite(t.Context(), &stdout, &stderr, res, err)

	require.Equal(t, 0, exitCode(cliErr))
	require.Contains(t, stderr.String(), "failingAddEngine: add_node refused")
	require.Contains(t, stderr.String(), "The tree's projected version is 2.")
}
