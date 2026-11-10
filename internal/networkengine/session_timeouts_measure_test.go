package networkengine

import (
	"context"
	"encoding/json"
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

// measureNodes reads the node count for one measurement run.
func measureNodes(t *testing.T) int {
	t.Helper()
	if os.Getenv("MLMFORGE_MEASURE_TIMEOUTS") != "1" {
		t.Skip("set MLMFORGE_MEASURE_TIMEOUTS=1 to run the session-timeout measurement")
	}
	n, err := strconv.Atoi(os.Getenv("MLMFORGE_MEASURE_NODES"))
	if err != nil || n < 2 {
		t.Fatalf("MLMFORGE_MEASURE_NODES must be an integer of at least 2, got %q", os.Getenv("MLMFORGE_MEASURE_NODES"))
	}
	return n
}

// measuredTree is one generated unilevel tree, ten wide per level.
type measuredTree struct {
	id       string
	stream   string
	n        int
	eventIDs []string
	version  int64
}

func measureUser(i int) string { return testUserUUID(i + 1) }

func measureParent(i int) int { return (i - 1) / 10 }

// generateMeasuredTree builds the engine tree, the event stream, the rows and
// the projection row for n nodes.
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
	return tree
}

// timing is one query's measured durations.
type timing struct {
	name    string
	cold    time.Duration
	warm    []time.Duration
	explain string
}

func (m timing) worst() time.Duration {
	return slices.Max(append([]time.Duration{m.cold}, m.warm...))
}

// measure runs fn once cold and three times warm.
func measure(t *testing.T, name, explain string, fn func(run int) error) timing {
	t.Helper()
	m := timing{name: name, explain: explain}
	for run := 0; run < 4; run++ {
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
	var results []timing

	results = append(results,
		measure(t, "EventStore.ReadStream (first 500)", readStreamLimitExplain(tree.stream, 1), func(int) error {
			_, err := es.ReadStream(ctx, tree.stream, 1, 500)
			return err
		}),
		measure(t, "EventStore.ReadStream (last 500)", readStreamLimitExplain(tree.stream, tree.version-499), func(int) error {
			_, err := es.ReadStream(ctx, tree.stream, tree.version-499, 500)
			return err
		}),
		measure(t, "EventStore.ReadLastEvent", "", func(int) error {
			_, err := es.ReadLastEvent(ctx, tree.stream)
			return err
		}),
		measure(t, "TreeStore.ProjectedVersion", "", func(int) error {
			_, _, err := store.ProjectedVersion(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.ProjectedRows", getByTreeDepthOrderedSQL, func(int) error {
			_, _, _, err := store.ProjectedRows(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.GetByTree", getByTreeSQL, func(int) error {
			_, err := store.GetByTree(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.GetByTreeDepthOrdered", getByTreeDepthOrderedSQL, func(int) error {
			_, err := store.GetByTreeDepthOrdered(ctx, tree.id)
			return err
		}),
		measure(t, "TreeStore.GetNode", "", func(int) error {
			_, err := store.GetNode(ctx, tree.id, leaf)
			return err
		}),
		measure(t, "TreeStore.GetNodeIncludingRemoved", "", func(int) error {
			_, err := store.GetNodeIncludingRemoved(ctx, tree.id, leaf)
			return err
		}),
		measure(t, "TreeStore.GetNodeByRemovalEvent", "", func(int) error {
			_, err := store.GetNodeByRemovalEvent(ctx, tree.id, nextEventUUID())
			return err
		}),
		measure(t, "SnapshotStore.Put", "", func(int) error {
			_, err := snaps.Put(ctx, Snapshot{TreeID: tree.id, Version: next(), TreeType: treeTypeUnilevel, Data: snapData})
			return err
		}),
		measure(t, "SnapshotStore.Get", "", func(int) error {
			_, _, err := snaps.Get(ctx, tree.id)
			return err
		}),
		measure(t, "SnapshotStore.RecordAttempt", "", func(int) error {
			return snaps.RecordAttempt(ctx, SnapshotAttempt{TreeID: tree.id, Version: next(), Stored: true})
		}),
		measure(t, "SnapshotStore.LastAttempt", "", func(int) error {
			_, _, err := snaps.LastAttempt(ctx, tree.id)
			return err
		}),
		measure(t, "EventStore.Append (one event)", "", func(int) error {
			data, _ := json.Marshal(NodePlacedPayload{TreeID: tree.id, UserID: nextEventUUID(), TreeType: treeTypeUnilevel})
			err := es.Append(ctx, tree.stream, tree.version, []platform.NewEvent{{ID: nextEventUUID(), Type: EventTypeNodePlaced, Payload: data}})
			tree.version++
			return err
		}),
		measure(t, "TreeStore.ProjectInsert (leaf)", "", func(run int) error {
			p := measureUser(0)
			return store.ProjectInsert(ctx, TreeNodeRow{ID: nextEventUUID(), TreeID: tree.id,
				UserID: testUserUUID(n + 10 + run), ParentID: &p, SponsorID: &p, Depth: 1,
				EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, next())
		}),
		// Each run removes a different depth-1 node, each with ten children re-sponsored to the root.
		measure(t, "TreeStore.ProjectRemoval (ten children)", "", func(run int) error {
			removed := run + 1
			var res []Responsored
			for c := removed*10 + 1; c <= removed*10+10 && c < n; c++ {
				res = append(res, Responsored{UserID: measureUser(c), NewSponsorID: measureUser(0)})
			}
			return store.ProjectRemoval(ctx, tree.id, RemovalProjection{Users: []string{measureUser(removed)}, Responsored: res},
				nextEventUUID(), next())
		}),
		measure(t, "TreeStore.ProjectRejection (one leaf)", "", func(run int) error {
			return store.ProjectRejection(ctx, tree.id, tree.eventIDs[n-1-run], next())
		}),
		measure(t, "SnapshotStore.Delete", "", func(int) error {
			return snaps.Delete(ctx, tree.id, version)
		}),
	)

	for _, m := range results {
		warm := make([]string, len(m.warm))
		for i, d := range m.warm {
			warm[i] = ms(d)
		}
		t.Logf("query %-42s cold %8s ms  warm %s ms", m.name, ms(m.cold), strings.Join(warm, " / "))
	}

	slices.SortFunc(results, func(a, b timing) int { return int(b.worst() - a.worst()) })
	for _, m := range results[:3] {
		if m.explain == "" {
			t.Logf("plan for %s: not captured; it runs more than one statement or a write", m.name)
			continue
		}
		args := []any{tree.id}
		if strings.HasPrefix(m.explain, "STREAM:") {
			parts := strings.SplitN(strings.TrimPrefix(m.explain, "STREAM:"), "|", 2)
			from, _ := strconv.ParseInt(parts[0], 10, 64)
			args = []any{tree.stream, from, int64(500)}
			m.explain = parts[1]
		}
		rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+m.explain, args...)
		require.NoError(t, err)
		var plan []string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			plan = append(plan, line)
		}
		rows.Close()
		t.Logf("plan for %s:\n%s", m.name, strings.Join(plan, "\n"))
	}
}

// readStreamLimitExplain encodes a stream page read and its start version for EXPLAIN.
func readStreamLimitExplain(_ string, from int64) string {
	return fmt.Sprintf("STREAM:%d|%s", from,
		"SELECT * FROM events WHERE stream = $1 AND version >= $2 ORDER BY version LIMIT $3")
}
