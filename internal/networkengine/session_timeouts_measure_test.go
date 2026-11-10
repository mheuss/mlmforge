package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

// measureMinNodes is the smallest node count the harness accepts.
const measureMinNodes = 51

// measureNodes reads the node count for one measurement run.
func measureNodes(t *testing.T) int {
	t.Helper()
	if os.Getenv("MLMFORGE_MEASURE_TIMEOUTS") != "1" {
		t.Skip("set MLMFORGE_MEASURE_TIMEOUTS=1 to run the session-timeout measurement")
	}
	n, err := strconv.Atoi(os.Getenv("MLMFORGE_MEASURE_NODES"))
	if err != nil || n < measureMinNodes {
		t.Fatalf("MLMFORGE_MEASURE_NODES must be an integer of at least %d, got %q",
			measureMinNodes, os.Getenv("MLMFORGE_MEASURE_NODES"))
	}
	return n
}

// measuredTree is one generated unilevel tree.
type measuredTree struct {
	id       string
	stream   string
	n        int
	eventIDs []string
	version  int64
}

func measureUser(i int) string { return testUserUUID(i + 1) }

// measureParent gives every node ten children.
func measureParent(i int) int { return (i - 1) / 10 }

// generateMeasuredTree builds the engine tree, the event stream, the rows and
// the projection row for n nodes, then analyzes the loaded tables.
func generateMeasuredTree(t *testing.T, es *platform.PostgresEventStore, engine *EngineClient, pool *pgxpool.Pool, n int) measuredTree {
	t.Helper()
	ctx := context.Background()
	tree := measuredTree{id: testTreeUUID(801), n: n, eventIDs: make([]string, n)}
	tree.stream = TreeStreamName(tree.id)
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, engine.CreateTree(ctx, tree.id, treeTypeUnilevel))
	require.NoError(t, engine.AddRoot(ctx, tree.id, measureUser(0), enrolled.Unix()))
	for i := 1; i < n; i++ {
		p := measureUser(measureParent(i))
		require.NoError(t, engine.AddNode(ctx, tree.id, measureUser(i), p, p, enrolled.Unix()))
	}

	var batch []platform.NewEvent
	flush := func() {
		require.NoError(t, es.Append(ctx, tree.stream, tree.version, batch))
		tree.version += int64(len(batch))
		batch = batch[:0]
	}
	for i := 0; i < n; i++ {
		var typ string
		var payload any
		if i == 0 {
			typ = EventTypeRootAdded
			payload = RootAddedPayload{TreeID: tree.id, UserID: measureUser(0), SponsorID: measureUser(0),
				EnrolledAt: enrolled, TreeType: treeTypeUnilevel}
		} else {
			p := measureUser(measureParent(i))
			typ = EventTypeNodePlaced
			payload = NodePlacedPayload{TreeID: tree.id, UserID: measureUser(i), ParentID: p, SponsorID: p,
				TreeType: treeTypeUnilevel, EnrolledAt: enrolled}
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		tree.eventIDs[i] = nextEventUUID()
		batch = append(batch, platform.NewEvent{ID: tree.eventIDs[i], Type: typ, Payload: data})
		if len(batch) == 1000 {
			flush()
		}
	}
	if len(batch) > 0 {
		flush()
	}

	depth := make([]int, n)
	for i := 1; i < n; i++ {
		depth[i] = depth[measureParent(i)] + 1
	}
	cols := []string{"id", "tree_id", "user_id", "parent_id", "sponsor_id", "position", "depth", "enrolled_at"}
	_, err := pool.CopyFrom(ctx, pgx.Identifier{"tree_nodes"}, cols, pgx.CopyFromSlice(n, func(i int) ([]any, error) {
		var parent any
		sponsor := measureUser(0)
		if i > 0 {
			parent = measureUser(measureParent(i))
			sponsor = measureUser(measureParent(i))
		}
		return []any{tree.eventIDs[i], tree.id, measureUser(i), parent, sponsor, nil, depth[i], enrolled}, nil
	}))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "INSERT INTO tree_projections (tree_id, projected_version) VALUES ($1, $2)", tree.id, tree.version)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "ANALYZE tree_nodes, events, tree_projections")
	require.NoError(t, err)
	return tree
}

// explainable is one statement the harness can run under EXPLAIN, with its
// arguments, after an optional preparing statement in the same transaction.
type explainable struct {
	sql      string
	args     func() []any
	prep     string
	prepArgs func() []any
}

// timing is one query's measured durations.
type timing struct {
	name    string
	cold    time.Duration
	warm    []time.Duration
	explain *explainable
}

// warmMax is zero for a query timed cold only.
func (m timing) warmMax() time.Duration {
	if len(m.warm) == 0 {
		return 0
	}
	return slices.Max(m.warm)
}

// measure runs setup untimed, then fn timed, once cold and three times warm.
func measure(t *testing.T, name string, explain *explainable, setup func(run int), fn func(run int) error) timing {
	t.Helper()
	m := timing{name: name, explain: explain}
	for run := 0; run < 4; run++ {
		if setup != nil {
			setup(run)
		}
		start := time.Now()
		require.NoError(t, fn(run), "query %s run %d", name, run)
		d := time.Since(start)
		if run == 0 {
			m.cold = d
		} else {
			m.warm = append(m.warm, d)
		}
	}
	return m
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }

// streamPageSQL is a stream page read for EXPLAIN.
const streamPageSQL = "SELECT global_position, id, stream, type, version, payload, metadata, timestamp " +
	"FROM events WHERE stream = $1 AND version >= $2 ORDER BY version LIMIT $3"

func TestMeasureSessionTimeouts(t *testing.T) {
	n := measureNodes(t)
	es, store, engine, pool := newIntegrationDeps(t)
	snaps := NewPostgresSnapshotStore(pool)
	ctx := context.Background()

	start := time.Now()
	tree := generateMeasuredTree(t, es, engine, pool, n)
	t.Logf("generation: %d nodes in %s", n, time.Since(start))

	taken, err := engine.TakeSnapshot(ctx, tree.id)
	require.NoError(t, err)
	snapData := []byte(taken.Data)
	t.Logf("snapshot data: %d bytes", len(snapData))

	leaf := measureUser(n - 1)
	version := tree.version
	next := func() int64 { version++; return version }
	var putVersion int64
	removalIDs := make([]string, 4)
	treeArg := func() []any { return []any{tree.id} }
	var results []timing

	results = append(results,
		measure(t, "EventStore.ReadStream (first 500)",
			&explainable{sql: streamPageSQL, args: func() []any { return []any{tree.stream, int64(1), int64(500)} }}, nil,
			func(int) error {
				_, err := es.ReadStream(ctx, tree.stream, 1, 500)
				return err
			}),
		measure(t, "EventStore.ReadStream (last 500)",
			&explainable{sql: streamPageSQL, args: func() []any { return []any{tree.stream, tree.version - 499, int64(500)} }}, nil,
			func(int) error {
				_, err := es.ReadStream(ctx, tree.stream, tree.version-499, 500)
				return err
			}),
		measure(t, "EventStore.ReadLastEvent", nil, nil, func(int) error {
			_, err := es.ReadLastEvent(ctx, tree.stream)
			return err
		}),
		measure(t, "TreeStore.ProjectedVersion", nil, nil, func(int) error {
			_, _, err := store.ProjectedVersion(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.ProjectedRows", &explainable{sql: getByTreeDepthOrderedSQL, args: treeArg}, nil, func(int) error {
			_, _, _, err := store.ProjectedRows(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.GetByTree", &explainable{sql: getByTreeSQL, args: treeArg}, nil, func(int) error {
			_, err := store.GetByTree(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.GetByTreeDepthOrdered", &explainable{sql: getByTreeDepthOrderedSQL, args: treeArg}, nil, func(int) error {
			_, err := store.GetByTreeDepthOrdered(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.GetNode", nil, nil, func(int) error {
			row, err := store.GetNode(ctx, tree.id, leaf)
			if err == nil && row == nil {
				return errors.New("GetNode returned no row for the generated leaf")
			}
			return err
		}),
		measure(t, "TreeStore.GetNodeIncludingRemoved", nil, nil, func(int) error {
			row, err := store.GetNodeIncludingRemoved(ctx, tree.id, leaf)
			if err == nil && row == nil {
				return errors.New("GetNodeIncludingRemoved returned no row for the generated leaf")
			}
			return err
		}),
		measure(t, "SnapshotStore.Put",
			&explainable{
				sql: putSnapshotSQL,
				args: func() []any {
					return []any{tree.id, version + 1000, treeTypeUnilevel, snapData}
				},
				// Explain the conflict branch, not the plain insert.
				prep: putSnapshotSQL,
				prepArgs: func() []any {
					return []any{tree.id, version + 999, treeTypeUnilevel, snapData}
				},
			}, nil,
			func(int) error {
				putVersion = next()
				stored, err := snaps.Put(ctx, Snapshot{TreeID: tree.id, Version: putVersion, TreeType: treeTypeUnilevel, Data: snapData})
				if err == nil && !stored {
					return fmt.Errorf("Put at version %d stored nothing", putVersion)
				}
				return err
			}),
		measure(t, "SnapshotStore.Get", nil, nil, func(int) error {
			_, found, err := snaps.Get(ctx, tree.id)
			if err == nil && !found {
				return errors.New("Get found no snapshot after the puts")
			}
			return err
		}),
		measure(t, "SnapshotStore.RecordAttempt", nil, nil, func(int) error {
			return snaps.RecordAttempt(ctx, SnapshotAttempt{TreeID: tree.id, Version: next(), Stored: true})
		}),
		measure(t, "SnapshotStore.LastAttempt", nil, nil, func(int) error {
			_, _, err := snaps.LastAttempt(ctx, tree.id)
			return err
		}),
		measure(t, "EventStore.Append (one event)", nil, nil, func(int) error {
			data, err := json.Marshal(NodePlacedPayload{TreeID: tree.id, UserID: nextEventUUID(), ParentID: measureUser(0),
				SponsorID: measureUser(0), TreeType: treeTypeUnilevel, EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
			if err != nil {
				return err
			}
			err = es.Append(ctx, tree.stream, tree.version, []platform.NewEvent{{ID: nextEventUUID(), Type: EventTypeNodePlaced, Payload: data}})
			tree.version++
			return err
		}),
		measure(t, "TreeStore.ProjectInsert (leaf)", nil, nil, func(run int) error {
			p := measureUser(0)
			return store.ProjectInsert(ctx, TreeNodeRow{ID: nextEventUUID(), TreeID: tree.id,
				UserID: testUserUUID(n + 10 + run), ParentID: &p, SponsorID: &p, Depth: 1,
				EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, next())
		}),
		// Run r removes node r+1 and re-sponsors its ten children to the root.
		measure(t, "TreeStore.ProjectRemoval (ten children)", nil, nil, func(run int) error {
			removed := run + 1
			var res []Responsored
			for c := removed*10 + 1; c <= removed*10+10; c++ {
				res = append(res, Responsored{UserID: measureUser(c), NewSponsorID: measureUser(0)})
			}
			removalIDs[run] = nextEventUUID()
			return store.ProjectRemoval(ctx, tree.id, RemovalProjection{Users: []string{measureUser(removed)}, Responsored: res},
				removalIDs[run], next())
		}),
		measure(t, "TreeStore.GetNodeByRemovalEvent (stamped)", nil, nil, func(run int) error {
			row, err := store.GetNodeByRemovalEvent(ctx, tree.id, removalIDs[run])
			if err == nil && row == nil {
				return fmt.Errorf("GetNodeByRemovalEvent found no row for removal %s", removalIDs[run])
			}
			return err
		}),
		measure(t, "TreeStore.ProjectRejection (one leaf)", nil, nil, func(run int) error {
			return store.ProjectRejection(ctx, tree.id, tree.eventIDs[n-1-run], next())
		}),
		measure(t, "SnapshotStore.Delete (stored row)", nil,
			func(run int) {
				if run > 0 {
					_, found, err := snaps.Get(ctx, tree.id)
					require.NoError(t, err)
					require.False(t, found, "a snapshot was still stored after the previous Delete")
				}
				putVersion = next()
				stored, err := snaps.Put(ctx, Snapshot{TreeID: tree.id, Version: putVersion, TreeType: treeTypeUnilevel, Data: snapData})
				require.NoError(t, err)
				require.True(t, stored, "the untimed Put before Delete stored nothing")
			},
			func(int) error {
				return snaps.Delete(ctx, tree.id, putVersion)
			}),
	)
	_, found, err := snaps.Get(ctx, tree.id)
	require.NoError(t, err)
	require.False(t, found, "a snapshot was still stored after the last Delete")

	rootTree, root := testTreeUUID(802), testUserUUID(1)
	require.NoError(t, store.ProjectInsert(ctx, TreeNodeRow{ID: nextEventUUID(), TreeID: rootTree, UserID: root,
		SponsorID: &root, Depth: 0, EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, 1))
	undoStart := time.Now()
	require.NoError(t, store.UndoRootProjection(ctx, rootTree, root, 1))
	results = append(results, timing{name: "TreeStore.UndoRootProjection", cold: time.Since(undoStart)})

	for _, m := range results {
		warm := make([]string, len(m.warm))
		for i, d := range m.warm {
			warm[i] = ms(d)
		}
		if len(warm) == 0 {
			t.Logf("query %-42s cold %8s ms  (cold only)", m.name, ms(m.cold))
			continue
		}
		t.Logf("query %-42s cold %8s ms  warm %s ms", m.name, ms(m.cold), strings.Join(warm, " / "))
	}

	slices.SortFunc(results, func(a, b timing) int { return int(b.warmMax() - a.warmMax()) })
	for _, m := range results[:3] {
		if m.explain == nil {
			t.Logf("plan for %s: not captured; the harness holds no statement to explain for it", m.name)
			continue
		}
		plan := explainRolledBack(t, ctx, pool, m.explain)
		t.Logf("plan for %s (rolled back):\n%s", m.name, strings.Join(plan, "\n"))
	}
}

// explainRolledBack runs e under EXPLAIN (ANALYZE, BUFFERS) in a transaction it rolls back.
func explainRolledBack(t *testing.T, ctx context.Context, pool *pgxpool.Pool, e *explainable) []string {
	t.Helper()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	if e.prep != "" {
		_, err := tx.Exec(ctx, e.prep, e.prepArgs()...)
		require.NoError(t, err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+e.sql, e.args()...)
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan = append(plan, line)
	}
	require.NoError(t, rows.Err())
	return plan
}

// matrixShape is a breadth-first matrix of one width, filled in node order.
type matrixShape struct{ width int }

// parent is node i's parent.
func (m matrixShape) parent(i int) int { return (i - 1) / m.width }

// position is node i's slot under its parent.
func (m matrixShape) position(i int) int { return (i - 1) % m.width }

// depth is node i's depth.
func (m matrixShape) depth(i int) int {
	d := 0
	for i > 0 {
		i = m.parent(i)
		d++
	}
	return d
}

// subtreeSize counts node i and its descendants among n nodes.
func (m matrixShape) subtreeSize(i, n int) int {
	size := 0
	for lo, hi := i, i; lo < n; lo, hi = lo*m.width+1, hi*m.width+m.width {
		size += min(hi, n-1) - lo + 1
	}
	return size
}

// measureMatrixWidth reads the matrix width for one measurement run.
func measureMatrixWidth(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("MLMFORGE_MEASURE_MATRIX_WIDTH")
	if raw == "" {
		return 3
	}
	w, err := strconv.Atoi(raw)
	if err != nil || w < 2 {
		t.Fatalf("MLMFORGE_MEASURE_MATRIX_WIDTH must be an integer of at least 2, got %q", raw)
	}
	return w
}

// generateMatrixTree builds a breadth-first matrix tree of n nodes in the
// engine, the event stream, the rows and the projection row, then analyzes
// the loaded tables.
func generateMatrixTree(t *testing.T, es *platform.PostgresEventStore, engine *EngineClient, pool *pgxpool.Pool, m matrixShape, n int) measuredTree {
	t.Helper()
	ctx := context.Background()
	tree := measuredTree{id: testTreeUUID(803), n: n, eventIDs: make([]string, n)}
	tree.stream = TreeStreamName(tree.id)
	enrolled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	width, spillover := m.width, "breadth_first"

	require.NoError(t, engine.CreateMatrixTree(ctx, tree.id, width, spillover))
	require.NoError(t, engine.AddRoot(ctx, tree.id, measureUser(0), enrolled.Unix()))
	for i := 1; i < n; i++ {
		p := measureUser(m.parent(i))
		require.NoError(t, engine.AddNodeAt(ctx, tree.id, measureUser(i), p, p, m.position(i), enrolled.Unix()))
	}

	var batch []platform.NewEvent
	flush := func() {
		require.NoError(t, es.Append(ctx, tree.stream, tree.version, batch))
		tree.version += int64(len(batch))
		batch = batch[:0]
	}
	for i := 0; i < n; i++ {
		var typ string
		var payload any
		if i == 0 {
			typ = EventTypeRootAdded
			payload = RootAddedPayload{TreeID: tree.id, UserID: measureUser(0), SponsorID: measureUser(0),
				EnrolledAt: enrolled, TreeType: treeTypeMatrix, MatrixWidth: &width, MatrixSpillover: &spillover}
		} else {
			p := measureUser(m.parent(i))
			pos := m.position(i)
			typ = EventTypeNodePlaced
			payload = NodePlacedPayload{TreeID: tree.id, UserID: measureUser(i), ParentID: p, SponsorID: p,
				Position: &pos, TreeType: treeTypeMatrix, EnrolledAt: enrolled}
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		tree.eventIDs[i] = nextEventUUID()
		batch = append(batch, platform.NewEvent{ID: tree.eventIDs[i], Type: typ, Payload: data})
		if len(batch) == 1000 {
			flush()
		}
	}
	if len(batch) > 0 {
		flush()
	}

	depth := make([]int, n)
	for i := 1; i < n; i++ {
		depth[i] = depth[m.parent(i)] + 1
	}
	cols := []string{"id", "tree_id", "user_id", "parent_id", "sponsor_id", "position", "depth", "enrolled_at"}
	_, err := pool.CopyFrom(ctx, pgx.Identifier{"tree_nodes"}, cols, pgx.CopyFromSlice(n, func(i int) ([]any, error) {
		var parent, position any
		sponsor := measureUser(0)
		if i > 0 {
			parent = measureUser(m.parent(i))
			sponsor = measureUser(m.parent(i))
			position = m.position(i)
		}
		return []any{tree.eventIDs[i], tree.id, measureUser(i), parent, sponsor, position, depth[i], enrolled}, nil
	}))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "INSERT INTO tree_projections (tree_id, projected_version) VALUES ($1, $2)", tree.id, tree.version)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "ANALYZE tree_nodes, events, tree_projections")
	require.NoError(t, err)
	return tree
}

func TestMeasureMatrixRemovals(t *testing.T) {
	n := measureNodes(t)
	m := matrixShape{width: measureMatrixWidth(t)}
	es, store, engine, pool := newIntegrationDeps(t)
	ctx := context.Background()

	start := time.Now()
	tree := generateMatrixTree(t, es, engine, pool, m, n)
	t.Logf("generation: %d-node width-%d breadth-first matrix, depth %d, in %s",
		n, m.width, m.depth(n-1), time.Since(start))

	removals := []struct {
		user int
		mode PruningMode
	}{
		{1, PruningPromoteEarliest},
		{2, PruningHoldingTank},
	}
	var replay *explainable
	for _, r := range removals {
		call, err := nodeRemovedCall(NodeRemovedPayload{TreeID: tree.id, UserID: measureUser(r.user), PruningMode: r.mode}, treeTypeMatrix)
		require.NoError(t, err)
		outcome, err := call.apply(ctx, engine)
		require.NoError(t, err)
		projection := RemovalProjection{Users: outcome.removed, Responsored: outcome.responsored, Relocated: outcome.relocated}
		tree.version++
		begin := time.Now()
		require.NoError(t, store.ProjectRemoval(ctx, tree.id, projection, nextEventUUID(), tree.version))
		elapsed := time.Since(begin)
		t.Logf("query TreeStore.ProjectRemoval (%s of node %d, generated subtree %d) first run, cache not controlled %8s ms: %d tombstoned, %d re-sponsored, %d relocated",
			r.mode, r.user, m.subtreeSize(r.user, n), ms(elapsed), len(projection.Users), len(projection.Responsored), len(projection.Relocated))
		if len(projection.Relocated) > 0 {
			replay = relocationReplay(tree.id, projection.Relocated)
		}
	}
	require.NotNil(t, replay, "no removal returned a relocation to replay")
	plan := explainRolledBack(t, ctx, pool, replay)
	t.Logf("plan for the relocation UPDATE, replayed after the timed runs and rolled back:\n%s", strings.Join(plan, "\n"))
}

// relocationReplay is the relocation UPDATE with the given relocations as its arguments.
func relocationReplay(treeID string, relocated []Relocation) *explainable {
	users := make([]string, len(relocated))
	parents := make([]string, len(relocated))
	positions := make([]int, len(relocated))
	depths := make([]int, len(relocated))
	for i, r := range relocated {
		users[i], parents[i], positions[i], depths[i] = r.UserID, r.ParentID, r.Position, r.Depth
	}
	return &explainable{sql: relocateSurvivorsSQL, args: func() []any { return []any{treeID, users, parents, positions, depths} }}
}
