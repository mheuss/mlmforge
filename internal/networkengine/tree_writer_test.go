package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
	require.Len(t, engine.checks, 1)
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
