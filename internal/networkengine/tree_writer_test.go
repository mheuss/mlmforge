package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unilevelRootRequest() AddRootRequest {
	return AddRootRequest{
		TreeID: writerTree, UserID: writerRoot, SponsorID: writerRoot,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	}
}

func TestTreeWriterAddRoot_AppendsAndProjectsTheRoot(t *testing.T) {
	env := newWriterEnv()
	w, engine := env.writer()

	res, err := w.AddRoot(context.Background(), unilevelRootRequest())

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	require.NoError(t, res.ReleaseErr)
	assert.Equal(t, TreeStreamName(writerTree), res.Stream)
	assert.Equal(t, int64(1), res.Version)

	stored := streamEvents(t, env.events, res.Stream)
	require.Len(t, stored, 1)
	assert.Equal(t, res.EventID, stored[0].ID)
	assert.Equal(t, EventTypeRootAdded, stored[0].Type)
	assert.JSONEq(t, `{"tree_id":"`+writerTree+`","user_id":"`+writerRoot+`","sponsor_id":"`+writerRoot+`",
		"enrolled_at":"2026-09-23T12:00:00Z","tree_type":"unilevel"}`, string(stored[0].Payload))

	row, err := env.store.GetNode(context.Background(), writerTree, writerRoot)
	require.NoError(t, err)
	require.NotNil(t, row, "the root must be projected into the store")
	assert.Equal(t, []Mutation{CheckAddRoot(writerRoot, writeTime.Unix())}, engine.checks)
}

func TestTreeWriterAddRoot_RecordsMatrixParameters(t *testing.T) {
	env := newWriterEnv()

	mustAddRoot(t, env, treeTypeMatrix)

	stored := streamEvents(t, env.events, TreeStreamName(writerTree))
	require.Len(t, stored, 1)
	var p RootAddedPayload
	require.NoError(t, json.Unmarshal(stored[0].Payload, &p))
	assert.Equal(t, treeTypeMatrix, p.TreeType)
	require.NotNil(t, p.MatrixWidth)
	assert.Equal(t, 3, *p.MatrixWidth)
	require.NotNil(t, p.MatrixSpillover)
	assert.Equal(t, "breadth_first", *p.MatrixSpillover)
}

func TestTreeWriterAddRoot_WritesEveryIDInCanonicalForm(t *testing.T) {
	env := newWriterEnv()
	w, _ := env.writer()
	req := unilevelRootRequest()
	req.TreeID, req.UserID, req.SponsorID = strings.ToUpper(writerTree), strings.ToUpper(writerRoot), strings.ToUpper(writerRoot)

	res, err := w.AddRoot(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, TreeStreamName(writerTree), res.Stream)
	stored := streamEvents(t, env.events, TreeStreamName(writerTree))
	require.Len(t, stored, 1)
	var p RootAddedPayload
	require.NoError(t, json.Unmarshal(stored[0].Payload, &p))
	assert.Equal(t, writerTree, p.TreeID)
	assert.Equal(t, writerRoot, p.UserID)
	assert.Equal(t, writerRoot, p.SponsorID)
	row, err := env.store.GetNode(context.Background(), writerTree, writerRoot)
	require.NoError(t, err)
	assert.NotNil(t, row)
}

func TestTreeWriterAddRoot_RefusesBeforeTheLock(t *testing.T) {
	width := 3
	cases := []struct {
		name    string
		setup   bool
		edit    func(*AddRootRequest)
		wantErr string
	}{
		{"a tree ID that is not a UUID", false,
			func(r *AddRootRequest) { r.TreeID = "not-a-uuid" },
			`tree_id "not-a-uuid" is not a UUID: `},
		{"matrix parameters on a unilevel tree", false,
			func(r *AddRootRequest) { r.MatrixWidth = &width },
			`add root to tree ` + writerTree + `: matrix width and spillover apply only to matrix trees, and the request names "unilevel"`},
		{"a type that differs from version 1", true,
			func(r *AddRootRequest) { r.TreeType = treeTypeBinary },
			"add root to tree " + writerTree + ": stream " + TreeStreamName(writerTree) +
				" records tree type unilevel at version 1, and the request names binary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newWriterEnv()
			if tc.setup {
				mustAddRoot(t, env, treeTypeUnilevel)
			}
			before := len(streamEvents(t, env.events, TreeStreamName(writerTree)))
			w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), refusingLocker{t})
			req := unilevelRootRequest()
			tc.edit(&req)

			_, err := w.AddRoot(context.Background(), req)

			require.ErrorContains(t, err, tc.wantErr)
			assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), before)
		})
	}
}

func TestTreeWriterAddRoot_TakesAnExistingTreesShapeFromVersion1(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeMatrix)
	w, engine := env.writer()
	engine.checkErr = &EngineError{Code: engineCodeRootAlreadyExists, Message: "tree already has a root node"}
	req := unilevelRootRequest()
	req.UserID, req.SponsorID, req.TreeType = writerOther, writerOther, treeTypeMatrix

	_, err := w.AddRoot(context.Background(), req)

	var engineErr *EngineError
	require.ErrorAs(t, err, &engineErr, "a request without matrix flags must reach the engine check")
	assert.Equal(t, engineCodeRootAlreadyExists, engineErr.Code)
	assert.Equal(t, []Mutation{CheckAddRoot(writerOther, writeTime.Unix())}, engine.checks)
	assert.Equal(t, []string{"create_matrix_tree 3 breadth_first", "check add_root"}, engine.calls)
}

func TestTreeWriterAddRoot_RefusesMatrixParametersThatDifferFromVersion1(t *testing.T) {
	width3, width5 := 3, 5
	breadth, depth := "breadth_first", "depth_first"
	prefix := "add root to tree " + writerTree + ": stream " + TreeStreamName(writerTree) + " records "
	cases := []struct {
		name      string
		recorded  string
		width     *int
		spillover *string
		wantErr   string
	}{
		{"a different width", treeTypeMatrix, &width5, nil,
			prefix + "matrix width 3 at version 1, and the request names 5"},
		{"a different spillover", treeTypeMatrix, nil, &depth,
			prefix + `matrix spillover "breadth_first" at version 1, and the request names "depth_first"`},
		{"a width on a unilevel tree", treeTypeUnilevel, &width3, nil,
			prefix + "no matrix width at version 1, and the request names 3"},
		{"a spillover on a unilevel tree", treeTypeUnilevel, nil, &breadth,
			prefix + `no matrix spillover at version 1, and the request names "breadth_first"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newWriterEnv()
			mustAddRoot(t, env, tc.recorded)
			w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), refusingLocker{t})
			req := unilevelRootRequest()
			req.UserID, req.SponsorID, req.TreeType = writerOther, writerOther, tc.recorded
			req.MatrixWidth, req.MatrixSpillover = tc.width, tc.spillover

			_, err := w.AddRoot(context.Background(), req)

			require.EqualError(t, err, tc.wantErr)
			assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 1)
		})
	}
}

func TestTreeWriterAddRoot_AcceptsMatrixParametersThatMatchVersion1(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeMatrix)
	w, engine := env.writer()
	engine.checkErr = &EngineError{Code: engineCodeRootAlreadyExists, Message: "tree already has a root node"}
	width, spillover := 3, "breadth_first"
	req := unilevelRootRequest()
	req.UserID, req.SponsorID, req.TreeType = writerOther, writerOther, treeTypeMatrix
	req.MatrixWidth, req.MatrixSpillover = &width, &spillover

	_, err := w.AddRoot(context.Background(), req)

	var engineErr *EngineError
	require.ErrorAs(t, err, &engineErr, "matching matrix flags must reach the engine check")
	assert.Equal(t, engineCodeRootAlreadyExists, engineErr.Code)
}

func TestTreeWriterAddRoot_LocksThenCreatesThenChecks(t *testing.T) {
	cases := []struct {
		name     string
		treeType string
		create   string
	}{
		{"unilevel", treeTypeUnilevel, "create_tree unilevel"},
		{"binary", treeTypeBinary, "create_tree binary"},
		{"matrix", treeTypeMatrix, "create_matrix_tree 3 breadth_first"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newWriterEnv()
			engine := newFakeWriterEngine()
			w := NewTreeWriter(env.events, env.store, engine, recordingLocker{inner: env.locker, engine: engine})
			req := unilevelRootRequest()
			req.TreeType = tc.treeType
			if tc.treeType == treeTypeMatrix {
				width, spillover := 3, "breadth_first"
				req.MatrixWidth, req.MatrixSpillover = &width, &spillover
			}

			_, err := w.AddRoot(context.Background(), req)

			require.NoError(t, err)
			assert.Equal(t, []string{"lock", tc.create, "check add_root"}, engine.calls)
		})
	}
}

func TestTreeWriterAddRoot_ReportsAProjectionFailureInTheResult(t *testing.T) {
	env := newWriterEnv()
	w, engine := env.writer()
	engine.failAdd[writerRoot] = errors.New("worker reply lost")

	res, err := w.AddRoot(context.Background(), unilevelRootRequest())

	require.NoError(t, err, "a confirmed append is a success")
	assert.Equal(t, int64(1), res.Version)
	require.Error(t, res.ProjectionErr)
	assert.Contains(t, res.ProjectionErr.Error(),
		"project event "+res.EventID+" at version 1 in stream "+res.Stream+": ")
	assert.Contains(t, res.ProjectionErr.Error(), "worker reply lost")
	assert.Len(t, streamEvents(t, env.events, res.Stream), 1)
}

func TestTreeWriterAddRoot_ReportsAReleaseFailureInTheResult(t *testing.T) {
	env := newWriterEnv()
	unlockErr := errors.New("pg_advisory_unlock for tree x returned false")
	w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), releaseFailingLocker{err: unlockErr})

	res, err := w.AddRoot(context.Background(), unilevelRootRequest())

	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Version)
	assert.Equal(t, unlockErr, res.ReleaseErr)
}

func TestTreeWriterAddRoot_ReportsAReleaseFailureAlongsideARefusal(t *testing.T) {
	env := newWriterEnv()
	unlockErr := errors.New("pg_advisory_unlock for tree x returned false")
	engine := newFakeWriterEngine()
	engine.checkErr = &EngineError{Code: engineCodeRootAlreadyExists, Message: "tree already has a root node"}
	w := NewTreeWriter(env.events, env.store, engine, releaseFailingLocker{err: unlockErr})

	res, err := w.AddRoot(context.Background(), unilevelRootRequest())

	require.ErrorContains(t, err, "check_mutation for add_root in tree "+writerTree+" returned: ")
	assert.Equal(t, unlockErr, res.ReleaseErr)
	assert.Empty(t, streamEvents(t, env.events, TreeStreamName(writerTree)))
}

func TestTreeWriterAddRoot_ReportsAnEngineThatAlreadyHoldsTheTree(t *testing.T) {
	t.Run("when the store holds the tree", func(t *testing.T) {
		env := newWriterEnv()
		mustAddRoot(t, env, treeTypeUnilevel)
		engine := newFakeWriterEngine()
		require.NoError(t, engine.CreateTree(context.Background(), writerTree, treeTypeUnilevel))
		w := NewTreeWriter(env.events, env.store, engine, env.locker)
		req := unilevelRootRequest()
		req.UserID = writerOther

		_, err := w.AddRoot(context.Background(), req)

		require.ErrorContains(t, err, "load tree "+writerTree+"; nothing was appended: ")
		require.ErrorContains(t, err, "TREE_EXISTS")
		assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 1)
	})
	t.Run("when the store is empty", func(t *testing.T) {
		env := newWriterEnv()
		engine := newFakeWriterEngine()
		require.NoError(t, engine.CreateTree(context.Background(), writerTree, treeTypeUnilevel))
		w := NewTreeWriter(env.events, env.store, engine, env.locker)

		_, err := w.AddRoot(context.Background(), unilevelRootRequest())

		require.ErrorContains(t, err, "create tree "+writerTree+" in the engine; nothing was appended: ")
		assert.Empty(t, streamEvents(t, env.events, TreeStreamName(writerTree)))
	})
}

func TestTreeWriterLock_TimeoutStatesTheTreeAndTheWait(t *testing.T) {
	env := newWriterEnv()
	unlock, err := env.locker.Lock(context.Background(), uuid.MustParse(writerTree))
	require.NoError(t, err)
	defer func() { _ = unlock() }()
	w, _ := env.writer(WithLockWait(50 * time.Millisecond))

	_, err = w.AddRoot(context.Background(), unilevelRootRequest())

	require.EqualError(t, err, "waited 50ms for the lock on tree "+writerTree+" and did not acquire it")
	var waitErr *TreeLockWaitError
	require.ErrorAs(t, err, &waitErr)
	assert.Empty(t, streamEvents(t, env.events, TreeStreamName(writerTree)))
}

func TestTreeWriterLock_ReportsTheCallersDeadline(t *testing.T) {
	env := newWriterEnv()
	unlock, err := env.locker.Lock(context.Background(), uuid.MustParse(writerTree))
	require.NoError(t, err)
	defer func() { _ = unlock() }()
	w, _ := env.writer(WithLockWait(5 * time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err = w.AddRoot(ctx, unilevelRootRequest())

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(),
		"the lock on tree "+writerTree+" was not acquired; the caller's context ended after ")
	var waitErr *TreeLockWaitError
	assert.False(t, errors.As(err, &waitErr), "got a *TreeLockWaitError; want the caller's context error")
}

func TestTreeWriterLock_IgnoresANonPositiveWait(t *testing.T) {
	env := newWriterEnv()
	w, _ := env.writer(WithLockWait(0))

	res, err := w.AddRoot(context.Background(), unilevelRootRequest())

	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Version)
}

func TestTreeWriterLock_ReportsTheCallersCancellation(t *testing.T) {
	env := newWriterEnv()
	unlock, err := env.locker.Lock(context.Background(), uuid.MustParse(writerTree))
	require.NoError(t, err)
	defer func() { _ = unlock() }()
	w, _ := env.writer(WithLockWait(5 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	_, err = w.AddRoot(ctx, unilevelRootRequest())

	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(),
		"the lock on tree "+writerTree+" was not acquired; the caller's context ended after ")
	var waitErr *TreeLockWaitError
	assert.False(t, errors.As(err, &waitErr), "a cancellation is not a lock-wait timeout")
}

func placeRequest(user string, position *int) PlaceRequest {
	return PlaceRequest{
		TreeID: writerTree, UserID: user, ParentID: writerRoot, SponsorID: writerRoot,
		Position: position, EnrolledAt: writeTime,
	}
}

func TestTreeWriterPlace_AppendsAndProjectsAPlacement(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	w, _ := env.writer()

	res, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, int64(2), res.Version)
	stored := streamEvents(t, env.events, res.Stream)
	require.Len(t, stored, 2)
	var p NodePlacedPayload
	require.NoError(t, json.Unmarshal(stored[1].Payload, &p))
	assert.Equal(t, treeTypeUnilevel, p.TreeType, "the type comes from version 1")
	row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.Depth)
}

func TestTreeWriterPlace_ChecksTheEngineCallTheConsumerMakes(t *testing.T) {
	one, two := 1, 2
	cases := []struct {
		treeType string
		position *int
		want     Mutation
	}{
		{treeTypeUnilevel, nil, CheckAddNode(writerChild, writerRoot, writerRoot, writeTime.Unix())},
		{treeTypeBinary, &one, CheckAddNode(writerChild, writerRoot, writerRoot, writeTime.Unix(), WithPosition(1))},
		{treeTypeMatrix, &two, CheckAddNodeAt(writerChild, writerRoot, writerRoot, 2, writeTime.Unix())},
	}
	for _, tc := range cases {
		t.Run(tc.treeType, func(t *testing.T) {
			env := newWriterEnv()
			mustAddRoot(t, env, tc.treeType)
			w, engine := env.writer()

			res, err := w.Place(context.Background(), placeRequest(writerChild, tc.position))

			require.NoError(t, err)
			require.NoError(t, res.ProjectionErr)
			assert.Equal(t, []Mutation{tc.want}, engine.checks)
			require.Len(t, engine.placements, 1, "placements: %v", engine.placements)
			assert.Equal(t, engine.checks, engine.placements)
		})
	}
}

func TestTreeWriterPlace_RefusesAnEmptyStreamBeforeTheLock(t *testing.T) {
	env := newWriterEnv()
	w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), refusingLocker{t})

	_, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.EqualError(t, err, "place "+writerChild+" in tree "+writerTree+": stream "+
		TreeStreamName(writerTree)+" has no events")
}

func TestTreeWriterRemove_RefusesAnEmptyStreamBeforeTheLock(t *testing.T) {
	env := newWriterEnv()
	w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), refusingLocker{t})

	_, err := w.Remove(context.Background(), RemoveRequest{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime})

	require.EqualError(t, err, "remove "+writerChild+" from tree "+writerTree+": stream "+
		TreeStreamName(writerTree)+" has no events")
}

func TestTreeWriterRemove_RefusesAMatrixTreeBeforeTheLock(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeMatrix)
	two := 2
	mustPlace(t, env, writerChild, &two)
	w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), refusingLocker{t})

	_, err := w.Remove(context.Background(), RemoveRequest{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime})

	require.EqualError(t, err, "remove "+writerChild+" from tree "+writerTree+": stream "+
		TreeStreamName(writerTree)+" records tree type matrix at version 1, and this writer does not remove from matrix trees")
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
}

func TestTreeWriterPlace_EnforcesThePositionRuleBeforeTheLock(t *testing.T) {
	zero := 0
	cases := []struct {
		treeType string
		position *int
		want     string
	}{
		{treeTypeMatrix, nil, "matrix node_placed for " + writerChild + " in tree " + writerTree +
			" has no position; matrix events must carry explicit placement"},
		{treeTypeUnilevel, &zero, "unilevel node_placed for " + writerChild + " in tree " + writerTree +
			" carries position 0; unilevel trees have no slots"},
	}
	for _, tc := range cases {
		t.Run(tc.treeType, func(t *testing.T) {
			env := newWriterEnv()
			mustAddRoot(t, env, tc.treeType)
			w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), refusingLocker{t})

			_, err := w.Place(context.Background(), placeRequest(writerChild, tc.position))

			require.EqualError(t, err, tc.want)
			assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 1)
		})
	}
}

func TestTreeWriterCheck_EveryRefusalCodeLeavesTheStreamUnchanged(t *testing.T) {
	zero := 0
	run := map[string]func(w *TreeWriter) (WriteResult, error){
		"add_root": func(w *TreeWriter) (WriteResult, error) {
			req := unilevelRootRequest()
			req.UserID, req.SponsorID = writerOther, writerOther
			return w.AddRoot(context.Background(), req)
		},
		"add_node": func(w *TreeWriter) (WriteResult, error) {
			return w.Place(context.Background(), placeRequest(writerOther, &zero))
		},
		"add_node_at": func(w *TreeWriter) (WriteResult, error) {
			return w.Place(context.Background(), placeRequest(writerOther, &zero))
		},
		"remove_node": func(w *TreeWriter) (WriteResult, error) {
			return w.Remove(context.Background(), RemoveRequest{TreeID: writerTree, UserID: writerRoot, RemovedAt: writeTime})
		},
	}
	treeTypeFor := map[string]string{
		"add_root": treeTypeUnilevel, "add_node": treeTypeBinary,
		"add_node_at": treeTypeMatrix, "remove_node": treeTypeUnilevel,
	}
	cases := []struct{ mutation, code string }{
		{"add_root", "ROOT_ALREADY_EXISTS"},
		{"add_root", "USER_ALREADY_EXISTS"},
		{"add_node", "USER_ALREADY_EXISTS"},
		{"add_node", "USER_NOT_FOUND"},
		{"add_node", "POSITION_OCCUPIED"},
		{"add_node", "INVALID_POSITION"},
		{"add_node_at", "TREE_EMPTY"},
		{"add_node_at", "INVALID_POSITION"},
		{"add_node_at", "USER_ALREADY_EXISTS"},
		{"add_node_at", "USER_NOT_FOUND"},
		{"add_node_at", "SPONSOR_NOT_FOUND"},
		{"add_node_at", "POSITION_OCCUPIED"},
		{"remove_node", "USER_NOT_FOUND"},
		{"remove_node", "HAS_CHILDREN"},
		{"remove_node", "SPONSORLESS_WITH_RECRUITS"},
		{"remove_node", "SPONSOR_CYCLE"},
	}
	for _, tc := range cases {
		t.Run(tc.mutation+" "+tc.code, func(t *testing.T) {
			env := newWriterEnv()
			mustAddRoot(t, env, treeTypeFor[tc.mutation])
			if tc.mutation == "remove_node" {
				mustPlace(t, env, writerChild, nil)
			}
			stream := TreeStreamName(writerTree)
			before := len(streamEvents(t, env.events, stream))
			w, engine := env.writer()
			engine.checkErr = &EngineError{Code: tc.code, Message: "scripted refusal"}

			_, err := run[tc.mutation](w)

			var engineErr *EngineError
			require.ErrorAs(t, err, &engineErr)
			assert.Equal(t, tc.code, engineErr.Code)
			assert.Equal(t, "scripted refusal", engineErr.Message)
			assert.Contains(t, err.Error(), "nothing was appended")
			require.Len(t, engine.checks, 1)
			assert.Equal(t, tc.mutation, engine.checks[0].op)
			assert.Len(t, streamEvents(t, env.events, stream), before)
		})
	}
}

func TestTreeWriterPlace_ReportsAProjectionFailureInTheResult(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	w, engine := env.writer()
	engine.failAdd[writerChild] = errors.New("worker reply lost")

	res, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.NoError(t, err, "a confirmed append is a success")
	assert.Equal(t, int64(2), res.Version)
	require.ErrorContains(t, res.ProjectionErr, "worker reply lost")
	assert.Len(t, streamEvents(t, env.events, res.Stream), 2)
}

func TestTreeWriterPlace_ProjectsTheEventAtItsOwnVersion(t *testing.T) {
	env := newWriterEnv()
	scripted := &scriptedEvents{MemoryEventStore: platform.NewMemoryEventStore()}
	env.events = scripted
	mustAddRoot(t, env, treeTypeUnilevel)
	scripted.afterAppend = func() {
		appendDirect(t, scripted.MemoryEventStore, EventTypeNodePlaced, NodePlacedPayload{
			TreeID: writerTree, UserID: writerOther, ParentID: writerRoot, SponsorID: writerRoot,
			TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
		})
	}
	w, _ := env.writer()

	res, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, int64(2), res.Version)
	own, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	assert.NotNil(t, own, "the writer's own event must be projected")
	later, err := env.store.GetNode(context.Background(), writerTree, writerOther)
	require.NoError(t, err)
	assert.Nil(t, later, "the event appended after it must not be projected")
}

func TestTreeWriterPlace_ReportsAnotherEventAtItsVersionWithoutProjecting(t *testing.T) {
	env := newWriterEnv()
	scripted := &scriptedEvents{MemoryEventStore: platform.NewMemoryEventStore()}
	env.events = scripted
	mustAddRoot(t, env, treeTypeUnilevel)
	stream := TreeStreamName(writerTree)
	scripted.readAsVersion = 2
	scripted.readAs = &platform.Event{
		ID: "dddddddd-dddd-dddd-dddd-000000000001", Stream: stream,
		Type: EventTypeNodePlaced, Version: 2, Payload: json.RawMessage(`{}`),
	}
	w, _ := env.writer()

	res, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.NoError(t, err)
	require.EqualError(t, res.ProjectionErr, "stream "+stream+
		" holds event dddddddd-dddd-dddd-dddd-000000000001 at version 2, where event "+res.EventID+
		" was appended; nothing was projected")
	row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	assert.Nil(t, row)
}

func TestTreeWriterRemove_AppendsAndProjectsARemoval(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	w, engine := env.writer()

	res, err := w.Remove(context.Background(), RemoveRequest{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime})

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, int64(3), res.Version)
	assert.Equal(t, []Mutation{CheckRemoveNode(writerChild)}, engine.checks)
	active, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	assert.Nil(t, active)
	stamped, err := env.store.GetNodeByRemovalEvent(context.Background(), writerTree, res.EventID)
	require.NoError(t, err)
	assert.NotNil(t, stamped)
}

func TestTreeWriterCatchUp_ProjectsAnUnprojectedLastEventBeforeAppending(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	unprojected := appendDirect(t, env.events, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: writerTree, UserID: writerChild, ParentID: writerRoot, SponsorID: writerRoot,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	log := &orderLog{}
	w := NewTreeWriter(&loggingEvents{EventStore: env.events, log: log},
		&loggingStore{TreeStore: env.store, log: log, name: "w"}, newFakeWriterEngine(), env.locker)

	res, err := w.Place(context.Background(), placeRequest(writerOther, nil))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, &CaughtUpEvent{EventID: unprojected.ID, Version: 2, Type: EventTypeNodePlaced}, res.CaughtUp)
	assert.Equal(t, int64(3), res.Version)
	insert, appendNew := log.indexOf("w insert "+writerChild), log.indexOf("append at 3")
	require.GreaterOrEqual(t, insert, 0, "the unprojected placement was never inserted: %v", log.snapshot())
	assert.Less(t, insert, appendNew, "the earlier event must project before the new one is appended: %v", log.snapshot())
}

func TestTreeWriterCatchUp_ProjectsTheLastEventBeforeTheCheck(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	appendDirect(t, env.events, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: writerTree, UserID: writerChild, ParentID: writerRoot, SponsorID: writerRoot,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	w, _ := env.writer()
	req := placeRequest(writerOther, nil)
	req.ParentID = writerChild

	res, err := w.Place(context.Background(), req)

	require.NoError(t, err, "parent %s exists only in the unprojected last event", writerChild)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, int64(3), res.Version)
	require.NotNil(t, res.CaughtUp)
	assert.Equal(t, int64(2), res.CaughtUp.Version)
}

func TestTreeWriterCatchUp_ConvergesOnAProjectedPlacement(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placed := mustPlace(t, env, writerChild, nil)
	w, _ := env.writer()

	res, err := w.Place(context.Background(), placeRequest(writerOther, nil))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, &CaughtUpEvent{EventID: placed.EventID, Version: 2, Type: EventTypeNodePlaced}, res.CaughtUp)
}

func TestTreeWriterCatchUp_ConvergesOnAProjectedRemoval(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	w, _ := env.writer()
	removed, err := w.Remove(context.Background(), RemoveRequest{TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime})
	require.NoError(t, err)
	require.NoError(t, removed.ProjectionErr)
	next, _ := env.writer()

	res, err := next.Place(context.Background(), placeRequest(writerOther, nil))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, &CaughtUpEvent{EventID: removed.EventID, Version: 3, Type: EventTypeNodeRemoved}, res.CaughtUp)
}

func TestTreeWriterCatchUp_RefusesALastEventOfAnotherType(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	foreign := appendDirect(t, env.events, "tree.renamed", map[string]string{"name": "x"})
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.EqualError(t, err, "stream "+TreeStreamName(writerTree)+" ends with event "+foreign.ID+
		` at version 2 of type "tree.renamed", which catch-up does not redeliver; nothing was appended`)
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
}

func TestTreeWriterCatchUp_ReportsAFailedRedeliveryAndAppendsNothing(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	orphan := appendDirect(t, env.events, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: writerTree, UserID: writerChild, ParentID: writerOther, SponsorID: writerRoot,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))

	require.EqualError(t, err, "redelivering event "+orphan.ID+" (tree.node_placed) at version 2 in stream "+
		TreeStreamName(writerTree)+" returned: parent node "+writerOther+" not found in tree "+writerTree+
		"; nothing was appended")
	var failed *CatchUpFailedError
	require.ErrorAs(t, err, &failed)
	assert.Equal(t, orphan.ID, failed.EventID)
	assert.Equal(t, int64(2), failed.Version)
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
}

// scriptedEnv is a writerEnv with a unilevel root added through a scripted
// event store.
func scriptedEnv(t *testing.T) (*writerEnv, *scriptedEvents) {
	t.Helper()
	env := newWriterEnv()
	scripted := &scriptedEvents{MemoryEventStore: platform.NewMemoryEventStore()}
	env.events = scripted
	mustAddRoot(t, env, treeTypeUnilevel)
	return env, scripted
}

func TestTreeWriterCatchUp_LeavesTheRowOfAnEventTheEngineRefused(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	refused := appendDirect(t, env.events, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: writerTree, UserID: writerChild, ParentID: writerRoot, SponsorID: writerRoot,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	w, engine := env.writer()
	engine.failAdd[writerChild] = fakeEngineError("SPONSOR_NOT_FOUND", "sponsor %s not found in tree", writerRoot)

	_, err := w.Place(context.Background(), placeRequest(writerOther, nil))

	var failed *CatchUpFailedError
	require.ErrorAs(t, err, &failed)
	assert.Equal(t, refused.ID, failed.EventID)
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
	row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	require.NotNil(t, row, "no active row for %s after the refused redelivery", writerChild)
	assert.Equal(t, refused.ID, row.ID)
	assert.Nil(t, row.RemovedAt)
}

func TestTreeWriterAppend_ConfirmsACommitWhoseReplyWasLost(t *testing.T) {
	env, scripted := scriptedEnv(t)
	scripted.appendErr, scripted.commitFirst = errors.New("connection reset by peer"), true
	w, _ := env.writer()

	res, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.NoError(t, err, "the read found the event, so it was appended")
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, int64(2), res.Version)
	row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	assert.NotNil(t, row)
}

func TestTreeWriterAppend_ReportsACommitThatDidNotLand(t *testing.T) {
	env, scripted := scriptedEnv(t)
	appendErr := errors.New("connection reset by peer")
	scripted.appendErr = appendErr
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.ErrorIs(t, err, appendErr)
	require.Regexp(t, " at version 2 returned: connection reset by peer; a read of that version found no event$", err.Error())
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 1)
}

func TestTreeWriterAppend_ReportsAnotherEventAtItsVersion(t *testing.T) {
	env, scripted := scriptedEnv(t)
	scripted.appendErr = errors.New("connection reset by peer")
	var interloper string
	scripted.afterAppend = func() {
		interloper = appendDirect(t, scripted.MemoryEventStore, EventTypeNodePlaced, NodePlacedPayload{
			TreeID: writerTree, UserID: writerOther, ParentID: writerRoot, SponsorID: writerRoot,
			TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
		}).ID
	}
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.Error(t, err)
	m := regexp.MustCompile("; a read of that version found event " + regexp.QuoteMeta(interloper) + ", so event ([0-9a-f-]{36}) was not appended$").
		FindStringSubmatch(err.Error())
	require.Len(t, m, 2, "error: %s", err)
	assert.NotEqual(t, interloper, m[1])
	assert.Contains(t, err.Error(), "append event "+m[1]+" to stream ")
	stored := streamEvents(t, env.events, TreeStreamName(writerTree))
	require.Len(t, stored, 2)
	assert.Equal(t, interloper, stored[1].ID)
}

func TestTreeWriterAppend_ConfirmsALostReplyAfterTheCallerCancels(t *testing.T) {
	env, scripted := scriptedEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scripted.appendErr, scripted.commitFirst = context.Canceled, true
	scripted.afterAppend = cancel
	w, _ := env.writer()

	res, err := w.Place(ctx, placeRequest(writerChild, nil))

	require.NoError(t, err, "the append committed before the caller's context ended")
	assert.Equal(t, int64(2), res.Version)
	require.ErrorIs(t, res.ProjectionErr, context.Canceled)
}

func TestTreeWriterAppend_ReportsAnUnknownOutcomeWhenTheReadFails(t *testing.T) {
	env, scripted := scriptedEnv(t)
	appendErr, readErr := errors.New("connection reset by peer"), errors.New("read timed out")
	scripted.appendErr, scripted.readErr = appendErr, readErr
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	var unknown *AppendOutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, TreeStreamName(writerTree), unknown.Stream)
	assert.Equal(t, int64(2), unknown.Version)
	require.EqualError(t, err, fmt.Sprintf(
		"append of event %s to stream %s at version 2 returned: connection reset by peer; "+
			"reading version 2 to confirm it returned: read timed out; whether the event was appended is unknown",
		unknown.EventID, TreeStreamName(writerTree)))
	assert.ErrorIs(t, err, appendErr)
	assert.ErrorIs(t, err, readErr)
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 1)
}

func TestTreeWriterAppend_StatesAConflictWithoutTheStoresVersion(t *testing.T) {
	env, scripted := scriptedEnv(t)
	stream := TreeStreamName(writerTree)
	scripted.appendErr = &platform.ConcurrencyError{Stream: stream, ExpectedVersion: 1, ActualVersion: 99}
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(writerChild, nil))

	require.EqualError(t, err, "append to stream "+stream+" at expected version 1 was refused with a concurrency conflict")
	var conflict *platform.ConcurrencyError
	assert.ErrorAs(t, err, &conflict)
}

func TestTreeWriterAddRoot_UsesTheTypeRecordedUnderTheLock(t *testing.T) {
	env := newWriterEnv()
	log := &orderLog{}
	locker := &hookLocker{inner: env.locker, before: func() {
		appendDirect(t, env.events, EventTypeRootAdded, RootAddedPayload{
			TreeID: writerTree, UserID: writerOther, SponsorID: writerOther,
			EnrolledAt: writeTime, TreeType: treeTypeBinary,
		})
	}}
	w := NewTreeWriter(env.events, &loggingStore{TreeStore: env.store, log: log, name: "w"},
		newFakeWriterEngine(), locker)

	_, err := w.AddRoot(context.Background(), unilevelRootRequest())

	require.EqualError(t, err, "add root to tree "+writerTree+": stream "+TreeStreamName(writerTree)+
		" records tree type binary at version 1, and the request names unilevel")
	assert.Equal(t, -1, log.indexOf("w load"), "the refusal must come before the load: %v", log.snapshot())
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 1)
}

func TestTreeWriterAddRoot_UsesTheMatrixShapeRecordedUnderTheLock(t *testing.T) {
	env := newWriterEnv()
	recordedWidth, spillover := 5, "breadth_first"
	locker := &hookLocker{inner: env.locker, before: func() {
		appendDirect(t, env.events, EventTypeRootAdded, RootAddedPayload{
			TreeID: writerTree, UserID: writerOther, SponsorID: writerOther,
			EnrolledAt: writeTime, TreeType: treeTypeMatrix,
			MatrixWidth: &recordedWidth, MatrixSpillover: &spillover,
		})
	}}
	w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), locker)
	width := 3
	req := unilevelRootRequest()
	req.TreeType, req.MatrixWidth, req.MatrixSpillover = treeTypeMatrix, &width, &spillover

	_, err := w.AddRoot(context.Background(), req)

	require.EqualError(t, err, "add root to tree "+writerTree+": stream "+TreeStreamName(writerTree)+
		" records matrix width 5 at version 1, and the request names 3")
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 1)
}
