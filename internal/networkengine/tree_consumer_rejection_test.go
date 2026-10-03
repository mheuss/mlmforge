package networkengine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rejectionFixture holds a root projected at version 1 and a child at version
// 2, with a consumer over the store.
type rejectionFixture struct {
	store     *MemoryTreeStore
	transport *recordingTransport
	consumer  *TreeEventConsumer
	tree      string
	root      string
	child     string
	childRow  string
}

func newRejectionFixture(t *testing.T) rejectionFixture {
	t.Helper()
	f := rejectionFixture{
		store: NewMemoryTreeStore(), transport: newRecordingTransport(),
		tree: testTreeUUID(1), root: testUserUUID(1), child: testUserUUID(2), childRow: testNodeUUID(2),
	}
	f.consumer = NewTreeEventConsumer(f.store, newEngineClientWithTransport(f.transport))
	ctx := context.Background()
	require.NoError(t, f.store.ProjectInsert(ctx, makeUUIDNode(testNodeUUID(1), f.tree, f.root, 0, nil, ptr(f.root), nil), 1))
	require.NoError(t, f.store.ProjectInsert(ctx, makeUUIDNode(f.childRow, f.tree, f.child, 1, ptr(f.root), ptr(f.root), nil), 2))
	return f
}

// rejection builds a rejection at version 3 naming the child's placement, as
// mutate leaves it.
func (f rejectionFixture) rejection(t *testing.T, mutate func(*platform.Event, *EventRejectedPayload)) platform.Event {
	t.Helper()
	p := EventRejectedPayload{
		TreeID: f.tree, RejectedEventID: f.childRow, RejectedVersion: 2,
		RejectedType: EventTypeNodePlaced, Reason: "sponsor never enrolled",
	}
	event := platform.Event{ID: testNodeUUID(30), Stream: TreeStreamName(f.tree), Type: EventTypeEventRejected, Version: 3}
	if mutate != nil {
		mutate(&event, &p)
	}
	data, err := json.Marshal(p)
	require.NoError(t, err)
	event.Payload = data
	return event
}

func TestTreeConsumer_EventRejectedSoftDeletesTheRejectedRow(t *testing.T) {
	f := newRejectionFixture(t)

	require.NoError(t, f.consumer.HandleEvent(context.Background(), f.rejection(t, nil)))

	got := readProjectionState(t, f.store, f.tree, f.root, f.child)
	assert.Equal(t, int64(3), got.version)
	require.NotNil(t, got.rows[f.child])
	assert.Equal(t, f.childRow, got.rows[f.child].ID)
	assert.NotNil(t, got.rows[f.child].RemovedAt)
	assert.Nil(t, got.rows[f.child].RemovedByEventID)
	assert.Nil(t, got.rows[f.root].RemovedAt)
	assert.Empty(t, f.transport.calls)
}

func TestTreeConsumer_EventRejectedTwiceLeavesTheStoreAsOnce(t *testing.T) {
	f := newRejectionFixture(t)
	event := f.rejection(t, nil)
	require.NoError(t, f.consumer.HandleEvent(context.Background(), event))
	before := readProjectionState(t, f.store, f.tree, f.root, f.child)

	require.NoError(t, f.consumer.HandleEvent(context.Background(), event))

	assert.Equal(t, before, readProjectionState(t, f.store, f.tree, f.root, f.child))
}

func TestTreeConsumer_EventRejectedRefusals(t *testing.T) {
	id := testNodeUUID(30)
	cases := []struct {
		name   string
		mutate func(*platform.Event, *EventRejectedPayload)
		want   func(f rejectionFixture) string
	}{
		{"an empty tree_id", func(_ *platform.Event, p *EventRejectedPayload) { p.TreeID = "" },
			func(rejectionFixture) string { return "event_rejected " + id + " has an empty tree_id" }},
		{"an empty rejected_event_id", func(_ *platform.Event, p *EventRejectedPayload) { p.RejectedEventID = "" },
			func(rejectionFixture) string { return "event_rejected " + id + " has an empty rejected_event_id" }},
		{"an empty rejected_type", func(_ *platform.Event, p *EventRejectedPayload) { p.RejectedType = "" },
			func(rejectionFixture) string { return "event_rejected " + id + " has an empty rejected_type" }},
		{"a whitespace reason", func(_ *platform.Event, p *EventRejectedPayload) { p.Reason = "  " },
			func(rejectionFixture) string { return "event_rejected " + id + " has an empty reason" }},
		{"another tree's stream", func(e *platform.Event, _ *EventRejectedPayload) { e.Stream = "tree-other" },
			func(f rejectionFixture) string {
				return "event_rejected " + id + " for tree " + f.tree + ` arrived on stream "tree-other", want "` +
					TreeStreamName(f.tree) + `"`
			}},
		{"version 1", func(e *platform.Event, p *EventRejectedPayload) { e.Version, p.RejectedVersion = 1, 0 },
			func(rejectionFixture) string { return "event_rejected " + id + " is at version 1, below 2" }},
		{"a rejected version that is not the one before", func(_ *platform.Event, p *EventRejectedPayload) { p.RejectedVersion = 1 },
			func(rejectionFixture) string {
				return "event_rejected " + id + " at version 3 names rejected version 1, not 2"
			}},
		{"a rejected type that is a rejection", func(_ *platform.Event, p *EventRejectedPayload) { p.RejectedType = EventTypeEventRejected },
			func(rejectionFixture) string {
				return "event_rejected " + id + ` names rejected type "tree.event_rejected", which is not a rejectable tree event type`
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRejectionFixture(t)
			before := readProjectionState(t, f.store, f.tree, f.root, f.child)

			err := f.consumer.HandleEvent(context.Background(), f.rejection(t, c.mutate))

			require.EqualError(t, err, c.want(f))
			assert.Equal(t, before, readProjectionState(t, f.store, f.tree, f.root, f.child))
			assert.Empty(t, f.transport.calls)
		})
	}
}

func TestTreeConsumer_EventRejectedThatDoesNotUnmarshal(t *testing.T) {
	f := newRejectionFixture(t)
	event := f.rejection(t, nil)
	event.Payload = json.RawMessage(`{not json}`)

	err := f.consumer.HandleEvent(context.Background(), event)

	require.ErrorContains(t, err, "unmarshal event_rejected payload")
	assert.Equal(t, int64(2), readProjectionState(t, f.store, f.tree).version)
	assert.Empty(t, f.transport.calls)
}

func TestTreeConsumer_EventRejectedAtVersion2IsApplied(t *testing.T) {
	f := newRejectionFixture(t)

	err := f.consumer.HandleEvent(context.Background(), f.rejection(t, func(e *platform.Event, p *EventRejectedPayload) {
		e.Version, p.RejectedVersion, p.RejectedEventID, p.RejectedType = 2, 1, testNodeUUID(1), EventTypeRootAdded
	}))

	require.NoError(t, err)
	got := readProjectionState(t, f.store, f.tree, f.root)
	require.NotNil(t, got.rows[f.root])
	assert.NotNil(t, got.rows[f.root].RemovedAt, "a rejection at version 2 left its row active")
	assert.Empty(t, f.transport.calls)
}

func TestTreeConsumer_EventRejectedCanonicalisesTheRejectedEventID(t *testing.T) {
	f := newRejectionFixture(t)

	require.NoError(t, f.consumer.HandleEvent(context.Background(), f.rejection(t, func(_ *platform.Event, p *EventRejectedPayload) {
		p.RejectedEventID = strings.ToUpper(p.RejectedEventID)
	})))

	got := readProjectionState(t, f.store, f.tree, f.child)
	require.NotNil(t, got.rows[f.child])
	assert.NotNil(t, got.rows[f.child].RemovedAt, "an upper-case rejected_event_id left the row active")
}

func TestTreeConsumer_EventRejectedRefusesARejectedEventIDThatIsNotAUUID(t *testing.T) {
	f := newRejectionFixture(t)
	before := readProjectionState(t, f.store, f.tree, f.root, f.child)

	err := f.consumer.HandleEvent(context.Background(), f.rejection(t, func(_ *platform.Event, p *EventRejectedPayload) {
		p.RejectedEventID = "not-a-uuid"
	}))

	require.ErrorContains(t, err, "event_rejected "+testNodeUUID(30)+` names rejected_event_id "not-a-uuid", which is not a UUID: `)
	assert.Equal(t, before, readProjectionState(t, f.store, f.tree, f.root, f.child))
	assert.Empty(t, f.transport.calls)
}
