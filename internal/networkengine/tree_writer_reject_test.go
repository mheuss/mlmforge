package networkengine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stuckPlacement appends a placement of writerChild naming a sponsor the tree
// does not hold, and runs one write whose engine refuses it, leaving the row
// at version 2.
func stuckPlacement(t *testing.T, env *writerEnv) platform.Event {
	t.Helper()
	mustAddRoot(t, env, treeTypeUnilevel)
	stuck := appendDirect(t, env.events, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: writerTree, UserID: writerChild, ParentID: writerRoot, SponsorID: writerOther,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	w, engine := env.writer()
	engine.failAdd[writerChild] = fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", writerOther)
	_, err := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))
	var failed *CatchUpFailedError
	require.ErrorAs(t, err, &failed)
	return stuck
}

// unprojectedRemoval appends a removal of writerChild at version 3 and
// projects nothing, leaving the store at version 2.
func unprojectedRemoval(t *testing.T, env *writerEnv) platform.Event {
	t.Helper()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	return appendDirect(t, env.events, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime,
	})
}

// appendRejection appends a rejection naming stuck, and projects nothing.
func appendRejection(t *testing.T, env *writerEnv, stuck platform.Event) platform.Event {
	t.Helper()
	return appendDirect(t, env.events, EventTypeEventRejected, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: stuck.ID, RejectedVersion: stuck.Version,
		RejectedType: stuck.Type, Reason: "appended by the test",
	})
}

// pendingMessage is RejectionPendingError's text with no load error.
func pendingMessage(rejection, stuck platform.Event, projected int64) string {
	return fmt.Sprintf("stream %s ends with rejection %s at version %d of event %s, and tree %s has projected version %d; "+
		"nothing was appended. Run mlmforge tree reject-event --tree-id %s --event-id %s again to project it",
		TreeStreamName(writerTree), rejection.ID, rejection.Version, stuck.ID, writerTree, projected, writerTree, stuck.ID)
}

func TestTreeWriter_AWriteRefusesAPendingRejectionBeforeCheckingTheMutation(t *testing.T) {
	env := newWriterEnv()
	stuck := unprojectedRemoval(t, env)
	rejection := appendRejection(t, env, stuck)
	w, engine := env.writer()

	_, err := w.Place(context.Background(), placeRequest(writerOther, nil))

	require.EqualError(t, err, pendingMessage(rejection, stuck, 2))
	var pending *RejectionPendingError
	require.ErrorAs(t, err, &pending)
	assert.Empty(t, engine.checks, "check_mutation ran against an engine loaded with the rejected event's state")
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 4)
	row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	assert.NotNil(t, row)
}

func TestTreeWriter_LoadRefusesAPendingRejection(t *testing.T) {
	env := newWriterEnv()
	stuck := unprojectedRemoval(t, env)
	rejection := appendRejection(t, env, stuck)
	w, _ := env.writer()

	res, err := w.Load(context.Background(), loadRequest())

	require.EqualError(t, err, pendingMessage(rejection, stuck, 2))
	assert.Nil(t, res.CaughtUp)
	assert.Zero(t, res.Nodes)
}

func TestTreeWriter_APendingRejectionCarriesTheLoadError(t *testing.T) {
	env := newWriterEnv()
	stuck := stuckPlacement(t, env)
	rejection := appendRejection(t, env, stuck)
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))

	var pending *RejectionPendingError
	require.ErrorAs(t, err, &pending)
	var rejected *TreeLoadRejectedError
	require.ErrorAs(t, err, &rejected, "the load error is not in the chain")
	assert.Equal(t, TreeLoadDataInvalid, rejected.Kind)
	require.ErrorContains(t, err, pendingMessage(rejection, stuck, 2)+". The load before this check returned: load tree "+
		writerTree+"; nothing was appended: node "+writerChild+" in tree "+writerTree+" references sponsor "+writerOther)
}

func TestTreeWriter_ALoadErrorStandsWhenNoRejectionIsPending(t *testing.T) {
	env := newWriterEnv()
	stuckPlacement(t, env)
	w, _ := env.writer()

	_, err := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))

	require.ErrorContains(t, err, "load tree "+writerTree+"; nothing was appended: node "+writerChild)
	var pending *RejectionPendingError
	assert.False(t, errors.As(err, &pending))
}

func TestTreeWriter_AFailedReadAfterAFailedLoadLeavesTheLoadError(t *testing.T) {
	env := newWriterEnv()
	stuckPlacement(t, env)
	events := &lastEventFailingEvents{MemoryEventStore: env.events.(*platform.MemoryEventStore),
		err: errors.New("connection reset")}
	w := NewTreeWriter(events, env.store, newFakeWriterEngine(), env.locker)

	_, err := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))

	require.ErrorContains(t, err, "load tree "+writerTree+"; nothing was appended: node "+writerChild)
	assert.NotContains(t, err.Error(), "connection reset")
	var pending *RejectionPendingError
	assert.False(t, errors.As(err, &pending))
}

func TestTreeWriter_AStoreAheadOfAStreamEndingRejectionMeetsTheFence(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	placed := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
	appendRejection(t, env, placed)
	require.NoError(t, env.store.ProjectInsert(context.Background(),
		makeUUIDNode(testNodeUUID(60), writerTree, testUserUUID(6), 1, ptr(writerRoot), ptr(writerRoot), nil), 9))
	want := StreamMovedError{TreeID: writerTree, LoadedVersion: 9, LastVersion: 3}

	w, engine := env.writer()
	_, writeErr := w.Place(context.Background(), placeRequest(writerOther, nil))
	l, _ := env.writer()
	_, loadErr := l.Load(context.Background(), loadRequest())

	var moved *StreamMovedError
	require.ErrorAs(t, writeErr, &moved)
	assert.Equal(t, want, *moved)
	require.ErrorAs(t, loadErr, &moved)
	assert.Equal(t, want, *moved)
	assert.Empty(t, engine.checks)
}

func TestTreeWriter_APendingRejectionOnATreeWithNoProjectionRowIsRefused(t *testing.T) {
	env := newWriterEnv()
	root := appendDirect(t, env.events, EventTypeRootAdded, rootAddedPayload())
	rejection := appendRejection(t, env, root)
	w, _ := env.writer()

	_, err := w.Load(context.Background(), loadRequest())

	require.EqualError(t, err, pendingMessage(rejection, root, 0))
}

func TestTreeWriterCatchUp_RefusesAPendingRejectionWithoutHandlingIt(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	placed := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
	rejection := appendRejection(t, env, placed)
	w, _ := env.writer()

	_, err := w.catchUp(context.Background(), writerTree, TreeStreamName(writerTree), rejection, 2)

	require.EqualError(t, err, pendingMessage(rejection, placed, 2))
	row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	assert.NotNil(t, row, "catch-up applied the pending rejection")
	version, _, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.Equal(t, int64(2), version)
}

func TestTreeWriterCatchUp_RedeliversAnAppliedRejection(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	placed := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
	rejection := appendRejection(t, env, placed)
	require.NoError(t, env.store.ProjectRejection(context.Background(), writerTree, placed.ID, 3))
	w, engine := env.writer()

	res, err := w.Place(context.Background(), placeRequest(writerOther, nil))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, &CaughtUpEvent{EventID: rejection.ID, Version: 3, Type: EventTypeEventRejected}, res.CaughtUp)
	assert.Equal(t, int64(4), res.Version)
	assert.Len(t, engine.checks, 1)
}

func TestTreeWriter_RefusesAnAppliedRejectionThatNamesTheWrongEvent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(p *EventRejectedPayload)
	}{
		{"another event's ID", func(p *EventRejectedPayload) { p.RejectedEventID = uuid.NewString() }},
		{"another type", func(p *EventRejectedPayload) { p.RejectedType = EventTypeNodeRemoved }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newWriterEnv()
			mustAddRoot(t, env, treeTypeUnilevel)
			mustPlace(t, env, writerChild, nil)
			placed := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
			p := EventRejectedPayload{TreeID: writerTree, RejectedEventID: placed.ID, RejectedVersion: 2,
				RejectedType: EventTypeNodePlaced, Reason: "appended by the test"}
			c.mutate(&p)
			bad := appendDirect(t, env.events, EventTypeEventRejected, p)
			require.NoError(t, env.store.ProjectRejection(context.Background(), writerTree, testNodeUUID(77), 3))
			want := fmt.Sprintf("rejection %s at version 3 of stream %s names event %s (%s), and version 2 holds event %s (%s); nothing was appended",
				bad.ID, TreeStreamName(writerTree), p.RejectedEventID, p.RejectedType, placed.ID, EventTypeNodePlaced)

			w, _ := env.writer()
			_, writeErr := w.Place(context.Background(), placeRequest(writerOther, nil))
			l, _ := env.writer()
			_, loadErr := l.Load(context.Background(), loadRequest())

			require.EqualError(t, writeErr, want)
			require.EqualError(t, loadErr, want)
			row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
			require.NoError(t, err)
			assert.NotNil(t, row)
		})
	}
}

func TestTreeWriterProject_RefusesARejectionThatNamesTheWrongEvent(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	placed := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
	other := uuid.NewString()
	bad := appendDirect(t, env.events, EventTypeEventRejected, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: other, RejectedVersion: 2,
		RejectedType: EventTypeNodePlaced, Reason: "appended by the test",
	})
	w, _ := env.writer()

	err := w.project(context.Background(), TreeStreamName(writerTree), bad.ID, 3)

	require.EqualError(t, err, fmt.Sprintf("project event %s at version 3 in stream %s: rejection %s at version 3 of stream %s "+
		"names event %s (%s), and version 2 holds event %s (%s)",
		bad.ID, TreeStreamName(writerTree), bad.ID, TreeStreamName(writerTree), other, EventTypeNodePlaced, placed.ID, EventTypeNodePlaced))
	version, _, verr := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, verr)
	assert.Equal(t, int64(2), version)
}

func TestTreeWriterCatchUp_RefusesAnAppliedRejectionThatNamesTheWrongEvent(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	stream := TreeStreamName(writerTree)
	placed := streamEvents(t, env.events, stream)[1]
	other := uuid.NewString()
	bad := appendDirect(t, env.events, EventTypeEventRejected, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: other, RejectedVersion: 2,
		RejectedType: EventTypeNodePlaced, Reason: "appended by the test",
	})
	require.NoError(t, env.store.ProjectRejection(context.Background(), writerTree, testNodeUUID(77), 3))
	w, _ := env.writer()

	_, err := w.catchUp(context.Background(), writerTree, stream, bad, 3)

	require.EqualError(t, err, fmt.Sprintf("rejection %s at version 3 of stream %s names event %s (%s), and version 2 holds event %s (%s); nothing was appended",
		bad.ID, stream, other, EventTypeNodePlaced, placed.ID, EventTypeNodePlaced))
}
