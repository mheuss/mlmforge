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

// Tree structure types, as carried in tree events and checked by
// supportedTreeTypes. Loader and consumer share these; raw string
// literals invite wrong-case drift.
const (
	treeTypeUnilevel = "unilevel"
	treeTypeBinary   = "binary"
	treeTypeMatrix   = "matrix"
)

// NodePlacedPayload is the event payload when a node is placed under a parent.
//
// TreeType is required and names the structure the tree was created with
// (unilevel, binary, matrix). The consumer dispatches on it: matrix
// placements project through add_node_at, everything else through add_node.
// An event with a missing or unknown type is rejected before projection.
//
// For matrix and binary trees, Position is required. Unilevel events must
// omit Position: unilevel trees have no slots, and the engine ignores the
// value, so storing one records a field the engine never honors. Placement
// is the producer's decision (ADR-020); the consumer never lets the engine
// invent one.
//
// The consumer trusts TreeType. It has no registry or worker op to verify
// the label against (HEU-554). A producer that mislabels a tree's type
// re-opens the divergence this contract exists to close.
type NodePlacedPayload struct {
	TreeID     string    `json:"tree_id"`
	UserID     string    `json:"user_id"`
	ParentID   string    `json:"parent_id"`
	SponsorID  string    `json:"sponsor_id"`
	Position   *int      `json:"position,omitempty"`
	TreeType   string    `json:"tree_type"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// NodeRemovedPayload is the event payload when a leaf node is removed.
type NodeRemovedPayload struct {
	TreeID    string    `json:"tree_id"`
	UserID    string    `json:"user_id"`
	RemovedAt time.Time `json:"removed_at"`
}
