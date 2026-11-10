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

// measureMinNodes is the smallest tree on which every timed query does its full work.
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

// explainable is one statement the harness can run under EXPLAIN, with its arguments.
type explainable struct {
	sql  string
	args func() []any
}

// timing is one query's measured durations.
type timing struct {
	name    string
	cold    time.Duration
	warm    []time.Duration
	explain *explainable
}

func (m timing) warmMax() time.Duration { return slices.Max(m.warm) }

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
const streamPageSQL = "SELECT * FROM events WHERE stream = $1 AND version >= $2 ORDER BY version LIMIT $3"

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
			&explainable{streamPageSQL, func() []any { return []any{tree.stream, int64(1), int64(500)} }}, nil,
			func(int) error {
				_, err := es.ReadStream(ctx, tree.stream, 1, 500)
				return err
			}),
		measure(t, "EventStore.ReadStream (last 500)",
			&explainable{streamPageSQL, func() []any { return []any{tree.stream, tree.version - 499, int64(500)} }}, nil,
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
		measure(t, "TreeStore.ProjectedRows", &explainable{getByTreeDepthOrderedSQL, treeArg}, nil, func(int) error {
			_, _, _, err := store.ProjectedRows(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.GetByTree", &explainable{getByTreeSQL, treeArg}, nil, func(int) error {
			_, err := store.GetByTree(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.GetByTreeDepthOrdered", &explainable{getByTreeDepthOrderedSQL, treeArg}, nil, func(int) error {
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
			&explainable{putSnapshotSQL, func() []any {
				return []any{tree.id, version + 1000, treeTypeUnilevel, snapData}
			}}, nil,
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

	for _, m := range results {
		warm := make([]string, len(m.warm))
		for i, d := range m.warm {
			warm[i] = ms(d)
		}
		t.Logf("query %-42s cold %8s ms  warm %s ms", m.name, ms(m.cold), strings.Join(warm, " / "))
	}

	slices.SortFunc(results, func(a, b timing) int { return int(b.warmMax() - a.warmMax()) })
	for _, m := range results[:3] {
		if m.explain == nil {
			t.Logf("plan for %s: not captured; the harness holds no statement to explain for it", m.name)
			continue
		}
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+m.explain.sql, m.explain.args()...)
		require.NoError(t, err)
		var plan []string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			plan = append(plan, line)
		}
		rows.Close()
		require.NoError(t, rows.Err())
		require.NoError(t, tx.Rollback(ctx))
		t.Logf("plan for %s (rolled back):\n%s", m.name, strings.Join(plan, "\n"))
	}
}
