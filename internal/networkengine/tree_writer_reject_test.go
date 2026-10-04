package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stuckPlacement appends a placement of writerChild naming a sponsor the tree
// does not hold, and runs one write whose engine refuses it.
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

// unprojectedRemoval places writerChild, then appends its removal without
// projecting it.
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

// pendingMessage is the pending-rejection message the tests expect, with no
// load error.
func pendingMessage(rejection, stuck platform.Event, projected int64) string {
	return fmt.Sprintf("stream %s ends with rejection %s at version %d of event %s, and tree %s has projected version %d; "+
		"nothing was appended. Run mlmforge tree reject-event --tree-id %s --event-id %s --reason <text> again to project it",
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

func TestTreeWriter_AStoreThreeBehindAStreamEndingRejectionMeetsTheFence(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	third := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerOther))
	appendRejection(t, env, third)
	want := StreamMovedError{TreeID: writerTree, LoadedVersion: 1, LastVersion: 4}

	w, engine := env.writer()
	_, writeErr := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))
	l, _ := env.writer()
	_, loadErr := l.Load(context.Background(), loadRequest())

	var moved *StreamMovedError
	require.ErrorAs(t, writeErr, &moved)
	assert.Equal(t, want, *moved)
	require.ErrorAs(t, loadErr, &moved)
	assert.Equal(t, want, *moved)
	assert.Empty(t, engine.checks)
}

func TestTreeWriter_ARejectionAtVersion3WithNoProjectionRowMeetsTheFence(t *testing.T) {
	env := newWriterEnv()
	appendDirect(t, env.events, EventTypeRootAdded, rootAddedPayload())
	placed := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	appendRejection(t, env, placed)
	want := ProjectionMissingError{TreeID: writerTree, LastVersion: 3}

	w, engine := env.writer()
	_, writeErr := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))
	l, _ := env.writer()
	_, loadErr := l.Load(context.Background(), loadRequest())

	var missing *ProjectionMissingError
	require.ErrorAs(t, writeErr, &missing)
	assert.Equal(t, want, *missing)
	require.ErrorAs(t, loadErr, &missing)
	assert.Equal(t, want, *missing)
	assert.Empty(t, engine.checks)
}

func TestTreeWriter_APendingRejectionWhosePayloadDoesNotDecodeIsRefused(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, env *writerEnv)
		loadKind bool
	}{
		{"after a successful load", func(t *testing.T, env *writerEnv) {
			mustAddRoot(t, env, treeTypeUnilevel)
			mustPlace(t, env, writerChild, nil)
		}, false},
		{"after a failed load", func(t *testing.T, env *writerEnv) { stuckPlacement(t, env) }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newWriterEnv()
			c.setup(t, env)
			bad := appendDirect(t, env.events, EventTypeEventRejected, json.RawMessage(`"not an object"`))
			w, _ := env.writer()

			_, err := w.Place(context.Background(), placeRequest(testUserUUID(4), nil))

			require.ErrorContains(t, err, "ends with rejection "+bad.ID+" at version 3, above projected version 2; nothing was appended: unmarshal event_rejected payload of event "+bad.ID)
			var pending *RejectionPendingError
			assert.False(t, errors.As(err, &pending))
			var rejected *TreeLoadRejectedError
			assert.Equal(t, c.loadKind, errors.As(err, &rejected), "the load error's presence in the chain")
		})
	}
}

func TestTreeWriter_RefusesAnAppliedRejectionWithATamperedVersion(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	stream := TreeStreamName(writerTree)
	placed := streamEvents(t, env.events, stream)[1]
	bad := appendDirect(t, env.events, EventTypeEventRejected, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: placed.ID, RejectedVersion: 1,
		RejectedType: EventTypeNodePlaced, Reason: "appended by the test",
	})
	require.NoError(t, env.store.ProjectRejection(context.Background(), writerTree, testNodeUUID(77), 3))
	want := fmt.Sprintf("rejection %s at version 3 of stream %s names rejected version 1, not 2; nothing was appended", bad.ID, stream)

	w, _ := env.writer()
	_, writeErr := w.Place(context.Background(), placeRequest(writerOther, nil))
	l, _ := env.writer()
	_, loadErr := l.Load(context.Background(), loadRequest())

	require.EqualError(t, writeErr, want)
	require.EqualError(t, loadErr, want)
}

func TestTreeWriterCheckRejectionTarget_ReportsWhatTheReadReturned(t *testing.T) {
	env, scripted := scriptedEnv(t)
	mustPlace(t, env, writerChild, nil)
	stream := TreeStreamName(writerTree)
	placed := streamEvents(t, env.events, stream)[1]
	rejection := appendRejection(t, env, placed)
	w, _ := env.writer()

	moved := placed
	moved.Version = 7
	scripted.readAs, scripted.readAsVersion = &moved, 2
	wrongVersion := w.checkRejectionTarget(context.Background(), stream, rejection)
	scripted.readAs = nil
	far := rejection
	far.Version = 9
	none := w.checkRejectionTarget(context.Background(), stream, far)

	require.EqualError(t, wrongVersion, fmt.Sprintf("a read of version 2 of stream %s, before rejection %s, returned event %s at version 7",
		stream, rejection.ID, placed.ID))
	require.EqualError(t, none, fmt.Sprintf("a read of version 8 of stream %s, before rejection %s, returned no event",
		stream, rejection.ID))
}

// rejectRequest asks to reject eventID in writerTree.
func rejectRequest(eventID string) RejectRequest {
	return RejectRequest{TreeID: writerTree, EventID: eventID, Reason: "sponsor never enrolled"}
}

// rejectionIn decodes the last event of writerTree's stream as a rejection.
func rejectionIn(t *testing.T, env *writerEnv) (platform.Event, EventRejectedPayload) {
	t.Helper()
	events := streamEvents(t, env.events, TreeStreamName(writerTree))
	last := events[len(events)-1]
	require.Equal(t, EventTypeEventRejected, last.Type)
	p, err := rejectionPayload(last)
	require.NoError(t, err)
	return last, p
}

func TestTreeWriterReject_RejectsAPlacementWhoseRowStopsTheLoad(t *testing.T) {
	env := newWriterEnv()
	stuck := stuckPlacement(t, env)
	w, engine := env.writer()

	res, err := w.Reject(context.Background(), rejectRequest(stuck.ID))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, RejectOutcomeRejected, res.Outcome)
	assert.Equal(t, stuck.ID, res.RejectedEventID)
	assert.Equal(t, int64(2), res.RejectedVersion)
	assert.Equal(t, int64(3), res.Version)
	var rejected *TreeLoadRejectedError
	require.ErrorAs(t, res.RetryErr, &rejected)
	assert.Empty(t, engine.checks)
	rejection, p := rejectionIn(t, env)
	assert.Equal(t, res.EventID, rejection.ID)
	assert.Equal(t, EventRejectedPayload{
		TreeID: writerTree, RejectedEventID: stuck.ID, RejectedVersion: 2,
		RejectedType: EventTypeNodePlaced, Reason: "sponsor never enrolled",
	}, p)
	row, err := env.store.GetNodeIncludingRemoved(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, stuck.ID, row.ID)
	assert.NotNil(t, row.RemovedAt)
	assert.Nil(t, row.RemovedByEventID)

	next, _ := env.writer()
	placed, err := next.Place(context.Background(), placeRequest(testUserUUID(4), nil))
	require.NoError(t, err)
	assert.Equal(t, &CaughtUpEvent{EventID: rejection.ID, Version: 3, Type: EventTypeEventRejected}, placed.CaughtUp)
	assert.Equal(t, int64(4), placed.Version)
}

func TestTreeWriterReject_RejectsAPlacementTheEngineRefusesOnItsFirstDelivery(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	stuck := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	w, engine := env.writer()
	engine.failAdd[writerChild] = fakeEngineError("SPONSOR_NOT_FOUND", "sponsor not found in tree")

	res, err := w.Reject(context.Background(), rejectRequest(stuck.ID))

	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeRejected, res.Outcome)
	var engineErr *EngineError
	require.ErrorAs(t, res.RetryErr, &engineErr)
	assert.Equal(t, "SPONSOR_NOT_FOUND", engineErr.Code)
	row, err := env.store.GetNodeIncludingRemoved(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.NotNil(t, row.RemovedAt)
}

func TestTreeWriterReject_RejectsACompensatedRoot(t *testing.T) {
	env := newWriterEnv()
	root := appendDirect(t, env.events, EventTypeRootAdded, rootAddedPayload())
	trigger, engine := env.writer()
	engine.failAdd[writerRoot] = fakeEngineError(engineCodeRootAlreadyExists, "tree already has a root node")
	second := testUserUUID(5)
	addSecond := AddRootRequest{TreeID: writerTree, UserID: second, SponsorID: second, TreeType: treeTypeUnilevel, EnrolledAt: writeTime}
	_, err := trigger.AddRoot(context.Background(), addSecond)
	var failed *CatchUpFailedError
	require.ErrorAs(t, err, &failed, "the trigger write did not run the compensation")
	w, _ := env.writer()

	res, err := w.Reject(context.Background(), rejectRequest(root.ID))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	require.ErrorIs(t, res.RetryErr, ErrReplayedPlacement)
	version, _, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.Equal(t, int64(2), version)

	binary, _ := env.writer()
	addBinary := addSecond
	addBinary.TreeType = treeTypeBinary
	_, err = binary.AddRoot(context.Background(), addBinary)
	require.EqualError(t, err, "add root to tree "+writerTree+": stream "+TreeStreamName(writerTree)+
		" records tree type unilevel at version 1, and the request names binary")

	again, _ := env.writer()
	added, err := again.AddRoot(context.Background(), addSecond)
	require.NoError(t, err)
	require.NoError(t, added.ProjectionErr)
	assert.Equal(t, int64(3), added.Version)
}

// refusingRemovalEngine refuses every removal with HAS_CHILDREN.
type refusingRemovalEngine struct{ *fakeWriterEngine }

func (refusingRemovalEngine) RemoveNode(context.Context, string, string) ([]Responsored, error) {
	return nil, fakeEngineError("HAS_CHILDREN", "user has children")
}

func TestReject_RemovalLeavesUserActive(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placed := mustPlace(t, env, writerChild, nil)
	removal := appendDirect(t, env.events, EventTypeNodeRemoved, NodeRemovedPayload{
		TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime,
	})
	w := NewTreeWriter(env.events, env.store, refusingRemovalEngine{newFakeWriterEngine()}, env.locker)

	res, err := w.Reject(context.Background(), rejectRequest(removal.ID))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, int64(4), res.Version)
	row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	require.NotNil(t, row, "rejecting the removal took the user out of the tree")
	assert.Equal(t, placed.EventID, row.ID)
	stamped, err := env.store.GetNodeByRemovalEvent(context.Background(), writerTree, removal.ID)
	require.NoError(t, err)
	assert.Nil(t, stamped)

	next, _ := env.writer()
	_, err = next.Place(context.Background(), PlaceRequest{
		TreeID: writerTree, UserID: writerOther, ParentID: writerChild, SponsorID: writerChild, EnrolledAt: writeTime,
	})
	require.NoError(t, err)
	loader, _ := env.writer()
	loaded, err := loader.Load(context.Background(), loadRequest())
	require.NoError(t, err)
	assert.Equal(t, 3, loaded.Nodes)
}

func TestTreeWriterReject_RefusesWhenTheRetrySucceeds(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	pending := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
	w, _ := env.writer()

	res, err := w.Reject(context.Background(), rejectRequest(pending.ID))

	require.EqualError(t, err, "retrying event "+pending.ID+" at version 2 in stream "+TreeStreamName(writerTree)+
		" returned no error; nothing was appended")
	assert.Empty(t, res.Outcome)
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
	version, _, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.Equal(t, int64(2), version, "projected version after the refused Reject")
}

// insertFailingStore fails every ProjectInsert.
type insertFailingStore struct{ TreeStore }

func (insertFailingStore) ProjectInsert(context.Context, TreeNodeRow, int64) error {
	return errors.New("connection reset by peer")
}

func TestTreeWriterReject_RefusesARetryErrorThatIsNotEvidence(t *testing.T) {
	cases := []struct {
		name  string
		build func(env *writerEnv) *TreeWriter
	}{
		{"an engine INTERNAL_ERROR", func(env *writerEnv) *TreeWriter {
			w, engine := env.writer()
			engine.failAdd[writerChild] = fakeEngineError("INTERNAL_ERROR", "handler panicked")
			return w
		}},
		{"an engine USER_ALREADY_EXISTS", func(env *writerEnv) *TreeWriter {
			w, engine := env.writer()
			engine.failAdd[writerChild] = fakeEngineError(engineCodeUserAlreadyExists, "user already exists in tree")
			return w
		}},
		{"a transport error", func(env *writerEnv) *TreeWriter {
			w, engine := env.writer()
			engine.failAdd[writerChild] = errors.New("write |1: broken pipe")
			return w
		}},
		{"a cancelled engine call", func(env *writerEnv) *TreeWriter {
			w, engine := env.writer()
			engine.failAdd[writerChild] = fmt.Errorf("add_node: %w", context.Canceled)
			return w
		}},
		{"an engine call past its deadline", func(env *writerEnv) *TreeWriter {
			w, engine := env.writer()
			engine.failAdd[writerChild] = context.DeadlineExceeded
			return w
		}},
		{"a store error", func(env *writerEnv) *TreeWriter {
			return NewTreeWriter(env.events, insertFailingStore{env.store}, newFakeWriterEngine(), env.locker)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newWriterEnv()
			mustAddRoot(t, env, treeTypeUnilevel)
			pending := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))

			res, err := c.build(env).Reject(context.Background(), rejectRequest(pending.ID))

			require.ErrorContains(t, err, "retrying event "+pending.ID+" at version 2 in stream "+TreeStreamName(writerTree)+
				" returned an error that is not one reject-event accepts as evidence; nothing was appended: ")
			assert.Empty(t, res.Outcome)
			assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
		})
	}
}

func TestTreeWriterReject_RefusesALoadFailureTheTieRuleDoesNotAccept(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, env *writerEnv) (*TreeWriter, platform.Event)
	}{
		{"a load whose store read failed", func(t *testing.T, env *writerEnv) (*TreeWriter, platform.Event) {
			stuck := stuckPlacement(t, env)
			return NewTreeWriter(env.events, depthReadFailingStore{env.store}, newFakeWriterEngine(), env.locker), stuck
		}},
		{"no row carries the event's ID", func(t *testing.T, env *writerEnv) (*TreeWriter, platform.Event) {
			mustAddRoot(t, env, treeTypeUnilevel)
			mustPlace(t, env, writerChild, nil)
			require.NoError(t, env.store.InsertNode(context.Background(), orphanRow()))
			removal := appendDirect(t, env.events, EventTypeNodeRemoved, NodeRemovedPayload{
				TreeID: writerTree, UserID: writerChild, RemovedAt: writeTime,
			})
			w, _ := env.writer()
			return w, removal
		}},
		{"the load still fails without the event's row", func(t *testing.T, env *writerEnv) (*TreeWriter, platform.Event) {
			mustAddRoot(t, env, treeTypeUnilevel)
			mustPlace(t, env, writerChild, nil)
			placed := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
			require.NoError(t, env.store.InsertNode(context.Background(), orphanRow()))
			w, _ := env.writer()
			return w, placed
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newWriterEnv()
			w, last := c.setup(t, env)
			before := streamEvents(t, env.events, TreeStreamName(writerTree))

			res, err := w.Reject(context.Background(), rejectRequest(last.ID))

			require.ErrorContains(t, err, "retrying event "+last.ID+" at version "+fmt.Sprint(last.Version)+" in stream "+
				TreeStreamName(writerTree)+" returned an error that is not one reject-event accepts as evidence; nothing was appended: ")
			assert.Empty(t, res.Outcome)
			assert.Equal(t, before, streamEvents(t, env.events, TreeStreamName(writerTree)))
		})
	}
}

func TestTreeWriterReject_AUnilevelFirstDeliveryRefusesThenTheLoadRuleRejects(t *testing.T) {
	env := newWriterEnv()
	ctx := context.Background()
	mustAddRoot(t, env, treeTypeUnilevel)
	stuck := appendDirect(t, env.events, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: writerTree, UserID: writerChild, ParentID: writerRoot, SponsorID: writerOther,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	first, engine := env.writer()
	engine.failAdd[writerChild] = fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", writerOther)

	_, err := first.Reject(ctx, rejectRequest(stuck.ID))

	require.ErrorContains(t, err, "returned an error that is not one reject-event accepts as evidence; nothing was appended")
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
	row, err := env.store.GetNode(ctx, writerTree, writerChild)
	require.NoError(t, err)
	require.NotNil(t, row, "the refused first run's retry did not commit the row")
	assert.Equal(t, stuck.ID, row.ID)

	second, _ := env.writer()
	res, err := second.Reject(ctx, rejectRequest(stuck.ID))

	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeRejected, res.Outcome)
	var rejected *TreeLoadRejectedError
	require.ErrorAs(t, res.RetryErr, &rejected)
}

func TestTreeWriterReject_StoresATrimmedReasonAndCanonicalIDs(t *testing.T) {
	env := newWriterEnv()
	stuck := stuckPlacement(t, env)
	w, _ := env.writer()

	res, err := w.Reject(context.Background(), RejectRequest{
		TreeID: strings.ToUpper(writerTree), EventID: strings.ToUpper(stuck.ID), Reason: "  sponsor never enrolled \n",
	})

	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeRejected, res.Outcome)
	assert.Equal(t, TreeStreamName(writerTree), res.Stream)
	_, p := rejectionIn(t, env)
	assert.Equal(t, "sponsor never enrolled", p.Reason)
	assert.Equal(t, writerTree, p.TreeID)
	assert.Equal(t, stuck.ID, p.RejectedEventID)
}

func TestTreeWriterReject_RefusesBeforeTheLock(t *testing.T) {
	event := testNodeUUID(5)
	cases := []struct {
		name string
		req  RejectRequest
		want string
	}{
		{"a tree ID that is not a UUID", RejectRequest{TreeID: "tree-9", EventID: event, Reason: "r"}, `tree_id "tree-9" is not a UUID`},
		{"an event ID that is not a UUID", RejectRequest{TreeID: writerTree, EventID: "e-9", Reason: "r"}, `event_id "e-9" is not a UUID`},
		{"a whitespace reason", RejectRequest{TreeID: writerTree, EventID: event, Reason: " \t "},
			"reject event " + event + " in tree " + writerTree + ": the reason is empty after trimming whitespace; nothing was appended"},
		{"an empty stream", RejectRequest{TreeID: writerTree, EventID: event, Reason: "r"},
			"reject event " + event + " in tree " + writerTree + ": stream " + TreeStreamName(writerTree) + " has no events"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newWriterEnv()
			w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), refusingLocker{t})

			_, err := w.Reject(context.Background(), c.req)

			require.ErrorContains(t, err, c.want)
		})
	}
}

func TestTreeWriterReject_RefusesAnEventThatIsNotTheLast(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	placed := mustPlace(t, env, writerChild, nil)
	first := streamEvents(t, env.events, TreeStreamName(writerTree))[0]
	w, _ := env.writer()

	_, err := w.Reject(context.Background(), rejectRequest(first.ID))

	require.EqualError(t, err, "stream "+TreeStreamName(writerTree)+" ends with event "+placed.EventID+
		" at version 2, not event "+first.ID+"; nothing was appended")
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
}

func TestTreeWriterReject_RefusesAnEventOfAnotherType(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	foreign := appendDirect(t, env.events, "tree.renamed", map[string]string{"name": "x"})
	w, _ := env.writer()

	_, err := w.Reject(context.Background(), rejectRequest(foreign.ID))

	require.EqualError(t, err, "stream "+TreeStreamName(writerTree)+" ends with event "+foreign.ID+
		` at version 2 of type "tree.renamed", which reject-event does not reject; nothing was appended`)
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
}

func TestTreeWriterReject_RefusesWhenTheLockIsNotAcquired(t *testing.T) {
	env := newWriterEnv()
	stuck := stuckPlacement(t, env)
	w := NewTreeWriter(env.events, env.store, newFakeWriterEngine(), immediateErrLocker{err: errors.New("lock refused")})

	_, err := w.Reject(context.Background(), rejectRequest(stuck.ID))

	require.ErrorContains(t, err, "lock refused")
	assert.True(t, strings.HasPrefix(err.Error(), "lock tree "+writerTree), err.Error())
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
}

func TestTreeWriterReject_AFailedAppendLeavesTheOutcomeEmpty(t *testing.T) {
	env, scripted := scriptedEnv(t)
	stuck := appendDirect(t, env.events, EventTypeNodePlaced, NodePlacedPayload{
		TreeID: writerTree, UserID: writerChild, ParentID: writerRoot, SponsorID: writerOther,
		TreeType: treeTypeUnilevel, EnrolledAt: writeTime,
	})
	trigger, engine := env.writer()
	engine.failAdd[writerChild] = fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", writerOther)
	_, err := trigger.Place(context.Background(), placeRequest(testUserUUID(4), nil))
	var failed *CatchUpFailedError
	require.ErrorAs(t, err, &failed)
	stream := TreeStreamName(writerTree)
	scripted.appendErr = &platform.ConcurrencyError{Stream: stream, ExpectedVersion: 2, ActualVersion: 3}
	w, _ := env.writer()

	res, err := w.Reject(context.Background(), rejectRequest(stuck.ID))

	var conflict *appendConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Empty(t, res.Outcome)
	assert.Empty(t, res.EventID)
	assert.Zero(t, res.Version)
	assert.Error(t, res.RetryErr)
	assert.Equal(t, stuck.ID, res.RejectedEventID)
	assert.Len(t, streamEvents(t, env.events, stream), 2)
}

// secondLastEventRead replaces the answer to the second ReadLastEvent.
type secondLastEventRead struct {
	*platform.MemoryEventStore
	replace func(real *platform.Event) *platform.Event
	reads   int
}

func (e *secondLastEventRead) ReadLastEvent(ctx context.Context, stream string) (*platform.Event, error) {
	real, err := e.MemoryEventStore.ReadLastEvent(ctx, stream)
	e.reads++
	if err != nil || e.reads != 2 {
		return real, err
	}
	return e.replace(real), nil
}

func TestTreeWriterReject_RefusesWhenTheRetryFindsAnotherLastEvent(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, env *writerEnv) platform.Event
		replace func(real *platform.Event) *platform.Event
		want    func(stuck platform.Event) string
	}{
		{"another event", func(t *testing.T, env *writerEnv) platform.Event { return unprojectedRemoval(t, env) },
			func(real *platform.Event) *platform.Event {
				other := *real
				other.ID = testNodeUUID(88)
				return &other
			},
			func(stuck platform.Event) string {
				return "for the retry returned last event " + testNodeUUID(88) + ", where event " + stuck.ID + " was expected"
			}},
		{"no event", func(t *testing.T, env *writerEnv) platform.Event {
			return appendDirect(t, env.events, EventTypeRootAdded, rootAddedPayload())
		},
			func(*platform.Event) *platform.Event { return nil },
			func(stuck platform.Event) string {
				return "for the retry returned no last event, where event " + stuck.ID + " was expected"
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newWriterEnv()
			stuck := c.setup(t, env)
			stream := TreeStreamName(writerTree)
			before := streamEvents(t, env.events, stream)
			events := &secondLastEventRead{MemoryEventStore: env.events.(*platform.MemoryEventStore), replace: c.replace}
			w := NewTreeWriter(events, env.store, newFakeWriterEngine(), env.locker)

			res, err := w.Reject(context.Background(), rejectRequest(stuck.ID))

			require.ErrorContains(t, err, "returned an error that is not one reject-event accepts as evidence; nothing was appended")
			require.ErrorContains(t, res.RetryErr, c.want(stuck))
			assert.Empty(t, res.Outcome)
			assert.Equal(t, before, streamEvents(t, env.events, stream))
		})
	}
}

// rejectionFailingStore fails its first failures calls to ProjectRejection.
type rejectionFailingStore struct {
	TreeStore
	failures int
}

func (s *rejectionFailingStore) ProjectRejection(ctx context.Context, treeID, rejectedEventID string, eventVersion int64) error {
	if s.failures > 0 {
		s.failures--
		return errors.New("connection reset by peer")
	}
	return s.TreeStore.ProjectRejection(ctx, treeID, rejectedEventID, eventVersion)
}

func TestTreeWriterReject_ARerunProjectsAPendingRejectionAndAppendsNothing(t *testing.T) {
	env := newWriterEnv()
	stuck := stuckPlacement(t, env)
	store := &rejectionFailingStore{TreeStore: env.store, failures: 2}
	w := NewTreeWriter(env.events, store, newFakeWriterEngine(), env.locker)
	ctx := context.Background()
	stream := TreeStreamName(writerTree)

	first, err := w.Reject(ctx, rejectRequest(stuck.ID))
	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeRejected, first.Outcome)
	assert.NotEmpty(t, first.EventID)
	assert.Equal(t, int64(3), first.Version)
	assert.Equal(t, stuck.ID, first.RejectedEventID)
	require.ErrorContains(t, first.ProjectionErr, "connection reset by peer")
	assert.Equal(t, &ProjectionObservation{Version: 2, Found: true}, first.Observed)

	second, err := w.Reject(ctx, rejectRequest(stuck.ID))
	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeResumed, second.Outcome)
	require.ErrorContains(t, second.ProjectionErr, "connection reset by peer")
	assert.Equal(t, &ProjectionObservation{Version: 2, Found: true}, second.Observed)

	third, err := w.Reject(ctx, rejectRequest(stuck.ID))
	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeResumed, third.Outcome)
	require.NoError(t, third.ProjectionErr)

	fourth, err := w.Reject(ctx, rejectRequest(stuck.ID))
	require.NoError(t, err)
	assert.Equal(t, RejectOutcomeAlreadyApplied, fourth.Outcome)

	for _, res := range []RejectResult{second, third, fourth} {
		assert.Equal(t, first.EventID, res.EventID)
		assert.Equal(t, int64(3), res.Version)
		assert.Equal(t, stuck.ID, res.RejectedEventID)
		assert.Equal(t, int64(2), res.RejectedVersion)
	}
	assert.Len(t, streamEvents(t, env.events, stream), 3)
	version, _, err := env.store.ProjectedVersion(ctx, writerTree)
	require.NoError(t, err)
	assert.Equal(t, int64(3), version)
	row, err := env.store.GetNode(ctx, writerTree, writerChild)
	require.NoError(t, err)
	assert.Nil(t, row, "the rejected placement's row is still active")
}

func TestTreeWriterReject_ResumesARejectionOnATreeWithNoProjectionRow(t *testing.T) {
	env := newWriterEnv()
	root := appendDirect(t, env.events, EventTypeRootAdded, rootAddedPayload())
	rejection := appendRejection(t, env, root)
	w, _ := env.writer()

	res, err := w.Reject(context.Background(), rejectRequest(root.ID))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, RejectOutcomeResumed, res.Outcome)
	version, found, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(2), version)
	assert.Equal(t, rejection.ID, res.EventID)
	assert.Equal(t, int64(2), res.Version)
	assert.Equal(t, root.ID, res.RejectedEventID)
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 2)
}

func TestTreeWriterReject_RefusesARejectionOfAnotherEvent(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	events := streamEvents(t, env.events, TreeStreamName(writerTree))
	rejection := appendRejection(t, env, events[1])
	w, _ := env.writer()

	_, err := w.Reject(context.Background(), rejectRequest(events[0].ID))

	require.EqualError(t, err, "stream "+TreeStreamName(writerTree)+" ends with rejection "+rejection.ID+
		" of event "+events[1].ID+", not event "+events[0].ID+"; nothing was appended")
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 3)
}

func TestTreeWriterReject_RefusesAResumedRejectionThatNamesTheWrongEvent(t *testing.T) {
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
			stream := TreeStreamName(writerTree)
			placed := streamEvents(t, env.events, stream)[1]
			p := EventRejectedPayload{TreeID: writerTree, RejectedEventID: placed.ID, RejectedVersion: 2,
				RejectedType: EventTypeNodePlaced, Reason: "appended by the test"}
			c.mutate(&p)
			bad := appendDirect(t, env.events, EventTypeEventRejected, p)
			w, _ := env.writer()

			res, err := w.Reject(context.Background(), rejectRequest(p.RejectedEventID))

			require.EqualError(t, err, fmt.Sprintf("rejection %s at version 3 of stream %s names event %s (%s), "+
				"and version 2 holds event %s (%s); nothing was appended or projected",
				bad.ID, stream, p.RejectedEventID, p.RejectedType, placed.ID, EventTypeNodePlaced))
			assert.Empty(t, res.Outcome)
			row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
			require.NoError(t, err)
			assert.NotNil(t, row, "a rejection naming the wrong event soft-deleted a row")
			assert.Len(t, streamEvents(t, env.events, stream), 3)
			version, _, err := env.store.ProjectedVersion(context.Background(), writerTree)
			require.NoError(t, err)
			assert.Equal(t, int64(2), version)
		})
	}
}

func TestTreeWriterReject_RefusesToRejectARejection(t *testing.T) {
	env := newWriterEnv()
	mustAddRoot(t, env, treeTypeUnilevel)
	mustPlace(t, env, writerChild, nil)
	placed := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
	rejection := appendRejection(t, env, placed)
	w, _ := env.writer()

	res, err := w.Reject(context.Background(), rejectRequest(rejection.ID))

	require.EqualError(t, err, "stream "+TreeStreamName(writerTree)+" ends with rejection "+rejection.ID+
		" of event "+placed.ID+", not event "+rejection.ID+"; nothing was appended")
	assert.Empty(t, res.Outcome)
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 3)
}

func TestTreeWriterReject_RefusesAPendingRejectionTheStoreCannotBeBehindBy(t *testing.T) {
	t.Run("two events behind the rejected one", func(t *testing.T) {
		env := newWriterEnv()
		mustAddRoot(t, env, treeTypeUnilevel)
		appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
		third := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerOther))
		appendRejection(t, env, third)
		w, _ := env.writer()

		_, err := w.Reject(context.Background(), rejectRequest(third.ID))

		var moved *StreamMovedError
		require.ErrorAs(t, err, &moved)
		assert.Equal(t, StreamMovedError{TreeID: writerTree, LoadedVersion: 1, LastVersion: 4, NoLoad: true}, *moved)
		assert.EqualError(t, err, "tree "+writerTree+" has projected version 1, and stream "+TreeStreamName(writerTree)+
			" ends at version 4; nothing was appended")
	})
	t.Run("no projection row past version 2", func(t *testing.T) {
		env := newWriterEnv()
		appendDirect(t, env.events, EventTypeRootAdded, rootAddedPayload())
		placed := appendDirect(t, env.events, EventTypeNodePlaced, childPlacedPayload(writerChild))
		appendRejection(t, env, placed)
		w, _ := env.writer()

		_, err := w.Reject(context.Background(), rejectRequest(placed.ID))

		var missing *ProjectionMissingError
		require.ErrorAs(t, err, &missing)
		assert.Equal(t, ProjectionMissingError{TreeID: writerTree, LastVersion: 3}, *missing)
	})
	t.Run("a store ahead of the rejection", func(t *testing.T) {
		env := newWriterEnv()
		mustAddRoot(t, env, treeTypeUnilevel)
		placed := mustPlace(t, env, writerChild, nil)
		placedEvent := streamEvents(t, env.events, TreeStreamName(writerTree))[1]
		appendRejection(t, env, placedEvent)
		require.NoError(t, env.store.ProjectInsert(context.Background(),
			makeUUIDNode(testNodeUUID(60), writerTree, testUserUUID(6), 1, ptr(writerRoot), ptr(writerRoot), nil), 9))
		w, _ := env.writer()

		_, err := w.Reject(context.Background(), rejectRequest(placed.EventID))

		var moved *StreamMovedError
		require.ErrorAs(t, err, &moved)
		assert.Equal(t, StreamMovedError{TreeID: writerTree, LoadedVersion: 9, LastVersion: 3, NoLoad: true}, *moved)
	})
}

func TestTreeWriterReject_ResumesARejectionTwoVersionsAheadOfTheStore(t *testing.T) {
	env := newWriterEnv()
	removal := unprojectedRemoval(t, env)
	rejection := appendRejection(t, env, removal)
	w, _ := env.writer()

	res, err := w.Reject(context.Background(), rejectRequest(removal.ID))

	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	assert.Equal(t, RejectOutcomeResumed, res.Outcome)
	assert.Equal(t, rejection.ID, res.EventID)
	version, found, err := env.store.ProjectedVersion(context.Background(), writerTree)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(4), version)
	row, err := env.store.GetNode(context.Background(), writerTree, writerChild)
	require.NoError(t, err)
	assert.NotNil(t, row, "resuming a rejected removal took the user out of the tree")
	assert.Len(t, streamEvents(t, env.events, TreeStreamName(writerTree)), 4)
}
