package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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
			TreeID: "tree1", UserID: "user-child", ParentID: "user-root", SponsorID: "user-root",
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
			mustJSON(t, RootAddedPayload{TreeID: "tree1", UserID: "user-root", SponsorID: "user-root"})},
		{"node_placed on another tree's stream", EventTypeNodePlaced, "tree-other", placed(func(*NodePlacedPayload) {})},
		{"node_removed on another tree's stream", EventTypeNodeRemoved, "tree-other",
			mustJSON(t, NodeRemovedPayload{TreeID: "tree1", UserID: "user-child"})},
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

			err := consumer.HandleEvent(context.Background(), event)

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
		TreeID: "tree1", UserID: "user-child", ParentID: "user-root", SponsorID: "user-root",
		TreeType: treeTypeUnilevel, EnrolledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})

	err := consumer.HandleEvent(context.Background(), event)

	require.EqualError(t, err, "get parent node: connection reset by peer")
	assert.NotErrorIs(t, err, ErrUnprojectableEvent)
}
