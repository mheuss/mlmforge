package networkengine

import "time"

// Tree event type constants for the EventStore type field.
const (
	EventTypeRootAdded   = "tree.root_added"
	EventTypeNodePlaced  = "tree.node_placed"
	EventTypeNodeRemoved = "tree.node_removed"
)

// TreeStreamName returns the EventStore stream name for a tree.
// Format: "tree-{treeID}" following the EventStore stream naming convention.
func TreeStreamName(treeID string) string {
	return "tree-" + treeID
}

// RootAddedPayload is the event payload when the first node is placed in a tree.
type RootAddedPayload struct {
	TreeID     string    `json:"tree_id"`
	UserID     string    `json:"user_id"`
	SponsorID  string    `json:"sponsor_id"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// NodePlacedPayload is the event payload when a node is placed under a parent.
type NodePlacedPayload struct {
	TreeID     string    `json:"tree_id"`
	UserID     string    `json:"user_id"`
	ParentID   string    `json:"parent_id"`
	SponsorID  string    `json:"sponsor_id"`
	Position   *int      `json:"position,omitempty"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// NodeRemovedPayload is the event payload when a leaf node is removed.
type NodeRemovedPayload struct {
	TreeID    string    `json:"tree_id"`
	UserID    string    `json:"user_id"`
	RemovedAt time.Time `json:"removed_at"`
}
