package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustJSON marshals v or fails the test.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func TestTreeConsumer_DeterministicRefusalsCarryErrUnprojectableEvent(t *testing.T) {
	badJSON := json.RawMessage(`{not json}`)
	placed := func(mutate func(*NodePlacedPayload)) json.RawMessage {
		p := NodePlacedPayload{
			TreeID: "tree1", UserID: userChild, ParentID: userRoot, SponsorID: userRoot,
			TreeType: treeTypeUnilevel, EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		}
		mutate(&p)
		return mustJSON(t, p)
	}
	cases := []struct {
		name      string
		eventType string
		stream    string
		payload   json.RawMessage
	}{
		{"root_added that does not unmarshal", EventTypeRootAdded, "tree-tree1", badJSON},
		{"node_placed that does not unmarshal", EventTypeNodePlaced, "tree-tree1", badJSON},
		{"node_removed that does not unmarshal", EventTypeNodeRemoved, "tree-tree1", badJSON},
		{"root_added on another tree's stream", EventTypeRootAdded, "tree-other",
			mustJSON(t, RootAddedPayload{TreeID: "tree1", UserID: userRoot, SponsorID: userRoot})},
		{"node_placed on another tree's stream", EventTypeNodePlaced, "tree-other", placed(func(*NodePlacedPayload) {})},
		{"node_removed on another tree's stream", EventTypeNodeRemoved, "tree-other",
			mustJSON(t, NodeRemovedPayload{TreeID: "tree1", UserID: userChild})},
		{"node_placed with a shape the engine cannot apply", EventTypeNodePlaced, "tree-tree1",
			placed(func(p *NodePlacedPayload) { p.TreeType = "ring" })},
		{"node_placed whose parent has no row", EventTypeNodePlaced, "tree-tree1", placed(func(*NodePlacedPayload) {})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			transport := newRecordingTransport()
			consumer := NewTreeEventConsumer(NewMemoryTreeStore(), newEngineClientWithTransport(transport))
			event := platform.Event{
				ID: "00000000-0000-0000-0000-000000000001", Stream: c.stream, Type: c.eventType,
				Version: 1, Payload: c.payload,
			}

			err := deliver(context.Background(), consumer, event)

			require.ErrorIs(t, err, ErrUnprojectableEvent)
			assert.Empty(t, transport.calls)
		})
	}
}

// getNodeFailingStore fails every GetNode.
type getNodeFailingStore struct{ TreeStore }

func (getNodeFailingStore) GetNode(context.Context, string, string) (*TreeNodeRow, error) {
	return nil, errors.New("connection reset by peer")
}

func TestTreeConsumer_AFailedParentReadIsNotUnprojectable(t *testing.T) {
	transport := newRecordingTransport()
	consumer := NewTreeEventConsumer(getNodeFailingStore{NewMemoryTreeStore()}, newEngineClientWithTransport(transport))
	event := makeEvent(EventTypeNodePlaced, NodePlacedPayload{
		TreeID: "tree1", UserID: userChild, ParentID: userRoot, SponsorID: userRoot,
		TreeType: treeTypeUnilevel, EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})

	err := deliver(context.Background(), consumer, event)

	require.EqualError(t, err, "get parent node: connection reset by peer")
	assert.NotErrorIs(t, err, ErrUnprojectableEvent)
}

func TestTreeConsumer_RefusesATreeTypeTheStreamDoesNotRecord(t *testing.T) {
	unilevel := treeShape{treeType: treeTypeUnilevel}
	matrix := treeShape{treeType: treeTypeMatrix, width: 2, spillover: "breadth_first"}
	binary := treeShape{treeType: treeTypeBinary}
	cases := []struct {
		name      string
		eventType string
		stream    treeShape
		payload   any
		want      string
	}{
		{"a removal labelled binary on a unilevel stream", EventTypeNodeRemoved, unilevel,
			NodeRemovedPayload{TreeID: "tree1", UserID: userChild, TreeType: treeTypeBinary},
			`node_removed for ` + userChild + ` in tree tree1 has tree_type "binary", and the stream records tree type unilevel`},
		{"a removal labelled matrix on a unilevel stream", EventTypeNodeRemoved, unilevel,
			NodeRemovedPayload{TreeID: "tree1", UserID: userChild, TreeType: treeTypeMatrix, PruningMode: PruningPromoteEarliest},
			`node_removed for ` + userChild + ` in tree tree1 has tree_type "matrix", and the stream records tree type unilevel`},
		{"a removal with no tree type on a matrix stream", EventTypeNodeRemoved, matrix,
			NodeRemovedPayload{TreeID: "tree1", UserID: userChild},
			`node_removed for ` + userChild + ` in tree tree1 has no tree_type, and the stream records tree type matrix`},
		{"a placement labelled binary on a unilevel stream", EventTypeNodePlaced, unilevel,
			NodePlacedPayload{TreeID: "tree1", UserID: userNew, ParentID: userRoot, SponsorID: userRoot,
				Position: intPtr(0), TreeType: treeTypeBinary},
			`node_placed for ` + userNew + ` in tree tree1 has tree_type "binary", and the stream records tree type unilevel`},
		{"a placement labelled matrix on a unilevel stream", EventTypeNodePlaced, unilevel,
			NodePlacedPayload{TreeID: "tree1", UserID: userNew, ParentID: userRoot, SponsorID: userRoot,
				Position: intPtr(0), TreeType: treeTypeMatrix},
			`node_placed for ` + userNew + ` in tree tree1 has tree_type "matrix", and the stream records tree type unilevel`},
		{"a placement labelled unilevel on a binary stream", EventTypeNodePlaced, binary,
			NodePlacedPayload{TreeID: "tree1", UserID: userNew, ParentID: userRoot, SponsorID: userRoot,
				TreeType: treeTypeUnilevel},
			`node_placed for ` + userNew + ` in tree tree1 has tree_type "unilevel", and the stream records tree type binary`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			inner := NewMemoryTreeStore()
			require.NoError(t, inner.insertNode(ctx, makeNode("tree1", userRoot, 0, nil, ptr(userRoot), nil)))
			require.NoError(t, inner.insertNode(ctx, makeNode("tree1", userChild, 1, ptr(userRoot), ptr(userRoot), nil)))
			store := &writeSpyStore{MemoryTreeStore: inner}
			transport := newRecordingTransport()
			consumer := NewTreeEventConsumer(getNodeFailingStore{store}, newEngineClientWithTransport(transport))

			err := consumer.HandleEvent(ctx, c.stream, makeEvent(c.eventType, c.payload))

			require.ErrorIs(t, err, ErrUnprojectableEvent)
			assert.EqualError(t, err, c.want)
			assert.Empty(t, transport.calls)
			assert.Empty(t, store.writes)
		})
	}
}

func TestTreeConsumer_ProjectsARemovalWithNoTreeTypeOnAStreamThatIsNotMatrix(t *testing.T) {
	for _, streamType := range []string{treeTypeUnilevel, treeTypeBinary} {
		t.Run(streamType, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryTreeStore()
			require.NoError(t, store.insertNode(ctx, makeNode("tree1", userRoot, 0, nil, ptr(userRoot), nil)))
			require.NoError(t, store.insertNode(ctx, makeNode("tree1", userChild, 1, ptr(userRoot), ptr(userRoot), nil)))
			transport := newRecordingTransport()
			transport.response = json.RawMessage(`{"removed":true,"responsored":[]}`)
			consumer := NewTreeEventConsumer(store, newEngineClientWithTransport(transport))

			err := consumer.HandleEvent(ctx, treeShape{treeType: streamType},
				makeEvent(EventTypeNodeRemoved, NodeRemovedPayload{TreeID: "tree1", UserID: userChild}))

			require.NoError(t, err)
			require.Len(t, transport.calls, 1)
			assert.Equal(t, "remove_node", transport.calls[0].op)
			row, err := store.GetNodeIncludingRemoved(ctx, "tree1", userChild)
			require.NoError(t, err)
			require.NotNil(t, row)
			assert.NotNil(t, row.RemovedAt)
		})
	}
}

func TestTreeConsumer_RefusesAnIDThatIsNotCanonical(t *testing.T) {
	nilID := uuid.Nil.String()
	root := func(user, sponsor string) RootAddedPayload {
		return RootAddedPayload{TreeID: "tree1", UserID: user, SponsorID: sponsor, TreeType: treeTypeUnilevel}
	}
	placed := func(user, parent, sponsor string) NodePlacedPayload {
		return NodePlacedPayload{TreeID: "tree1", UserID: user, ParentID: parent, SponsorID: sponsor, TreeType: treeTypeUnilevel}
	}
	removed := func(user string) NodeRemovedPayload {
		return NodeRemovedPayload{TreeID: "tree1", UserID: user, TreeType: treeTypeUnilevel}
	}
	cases := []struct {
		name      string
		eventType string
		payload   any
		want      string
	}{
		{"a root whose user is not a UUID", EventTypeRootAdded, root("nope", userRoot),
			`root_added in tree tree1 has user_id "nope", which is not a canonical UUID`},
		{"a root whose user is the nil UUID", EventTypeRootAdded, root(nilID, userRoot),
			"root_added in tree tree1 has the nil UUID as user_id"},
		{"a root whose sponsor is empty", EventTypeRootAdded, root(userRoot, ""),
			`root_added in tree tree1 has sponsor_id "", which is not a canonical UUID`},
		{"a root whose sponsor is uppercase", EventTypeRootAdded, root(userRoot, "AAAAAAAA-AAAA-AAAA-AAAA-000000000001"),
			`root_added in tree tree1 has sponsor_id "AAAAAAAA-AAAA-AAAA-AAAA-000000000001", which is not a canonical UUID`},
		{"a placement whose user is a urn", EventTypeNodePlaced, placed("urn:uuid:"+userNew, userRoot, userRoot),
			`node_placed in tree tree1 has user_id "urn:uuid:` + userNew + `", which is not a canonical UUID`},
		{"a placement whose parent is blank", EventTypeNodePlaced, placed(userNew, " ", userRoot),
			`node_placed in tree tree1 has parent_id " ", which is not a canonical UUID`},
		{"a placement whose parent is the nil UUID", EventTypeNodePlaced, placed(userNew, nilID, userRoot),
			"node_placed in tree tree1 has the nil UUID as parent_id"},
		{"a placement whose sponsor is braced", EventTypeNodePlaced, placed(userNew, userRoot, "{"+userRoot+"}"),
			`node_placed in tree tree1 has sponsor_id "{` + userRoot + `}", which is not a canonical UUID`},
		{"a placement whose sponsor is the nil UUID", EventTypeNodePlaced, placed(userNew, userRoot, nilID),
			"node_placed in tree tree1 has the nil UUID as sponsor_id"},
		{"a removal whose user is empty", EventTypeNodeRemoved, removed(""),
			`node_removed in tree tree1 has user_id "", which is not a canonical UUID`},
		{"a removal whose user is not a UUID", EventTypeNodeRemoved, removed("nope"),
			`node_removed in tree tree1 has user_id "nope", which is not a canonical UUID`},
		{"a removal whose user is the nil UUID", EventTypeNodeRemoved, removed(nilID),
			"node_removed in tree tree1 has the nil UUID as user_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			inner := NewMemoryTreeStore()
			require.NoError(t, inner.insertNode(ctx, makeNode("tree1", userRoot, 0, nil, ptr(userRoot), nil)))
			store := &writeSpyStore{MemoryTreeStore: inner}
			transport := newRecordingTransport()
			consumer := NewTreeEventConsumer(getNodeFailingStore{store}, newEngineClientWithTransport(transport))

			err := consumer.HandleEvent(ctx, treeShape{treeType: treeTypeUnilevel}, makeEvent(c.eventType, c.payload))

			require.ErrorIs(t, err, ErrUnprojectableEvent)
			assert.EqualError(t, err, c.want)
			assert.Empty(t, transport.calls)
			assert.Empty(t, store.writes)
		})
	}
}

func TestTreeConsumer_ProjectsARootWhoseSponsorIsTheNilUUID(t *testing.T) {
	store := NewMemoryTreeStore()
	transport := newRecordingTransport()
	consumer := NewTreeEventConsumer(store, newEngineClientWithTransport(transport))
	nilID := uuid.Nil.String()

	err := deliver(context.Background(), consumer, makeEvent(EventTypeRootAdded, RootAddedPayload{
		TreeID: "tree1", UserID: userRoot, SponsorID: nilID, TreeType: treeTypeUnilevel,
		EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}))

	require.NoError(t, err)
	row, err := store.GetNode(context.Background(), "tree1", userRoot)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, nilID, *row.SponsorID)
	require.Len(t, transport.calls, 1)
	assert.Equal(t, "add_root", transport.calls[0].op)
}
