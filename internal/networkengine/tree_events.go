package networkengine

import (
	"fmt"
	"math"
	"time"
)

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
//
// TreeType names the tree's structure. MatrixWidth and MatrixSpillover are set
// exactly when TreeType is matrix.
type RootAddedPayload struct {
	TreeID          string    `json:"tree_id"`
	UserID          string    `json:"user_id"`
	SponsorID       string    `json:"sponsor_id"`
	EnrolledAt      time.Time `json:"enrolled_at"`
	TreeType        string    `json:"tree_type"`
	MatrixWidth     *int      `json:"matrix_width,omitempty"`
	MatrixSpillover *string   `json:"matrix_spillover,omitempty"`
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
// value, so storing one records a field the engine never honors. (The
// loader still tolerates legacy unilevel rows that carry one — HEU-563;
// this gate stops new ones at the door.) Placement is the producer's
// decision (ADR-020); the consumer never lets the engine invent one.
//
// The consumer trusts TreeType. It has no registry, and no cheap worker
// op, to verify the label against (HEU-554). A producer that mislabels a
// tree's type re-opens the divergence this contract exists to close.
type NodePlacedPayload struct {
	TreeID     string    `json:"tree_id"`
	UserID     string    `json:"user_id"`
	ParentID   string    `json:"parent_id"`
	SponsorID  string    `json:"sponsor_id"`
	Position   *int      `json:"position,omitempty"`
	TreeType   string    `json:"tree_type"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// checkNodePlacedShape refuses a node_placed payload whose tree type or
// position the engine cannot apply as written.
func checkNodePlacedShape(payload NodePlacedPayload) error {
	if !supportedTreeTypes[payload.TreeType] {
		return fmt.Errorf("node_placed for %s in tree %s has unsupported tree_type %q",
			payload.UserID, payload.TreeID, payload.TreeType)
	}
	if payload.Position != nil && *payload.Position < 0 {
		return fmt.Errorf("node_placed for %s in tree %s has negative position %d",
			payload.UserID, payload.TreeID, *payload.Position)
	}
	switch payload.TreeType {
	case treeTypeMatrix:
		// The width bound needs the tree's configured width, which nothing
		// persists yet (HEU-554). The engine still enforces it at runtime.
		// The u8 ceiling needs no width: no matrix can have a slot above
		// math.MaxUint8, so anything larger is rejected here.
		if payload.Position == nil {
			return fmt.Errorf("matrix node_placed for %s in tree %s has no position; matrix events must carry explicit placement",
				payload.UserID, payload.TreeID)
		}
		if *payload.Position > math.MaxUint8 {
			return fmt.Errorf("matrix node_placed for %s in tree %s has position %d above the %d slot ceiling",
				payload.UserID, payload.TreeID, *payload.Position, math.MaxUint8)
		}
	case treeTypeBinary:
		if payload.Position == nil || *payload.Position > 1 {
			return fmt.Errorf("binary node_placed for %s in tree %s needs position 0 or 1",
				payload.UserID, payload.TreeID)
		}
	case treeTypeUnilevel:
		if payload.Position != nil {
			return fmt.Errorf("unilevel node_placed for %s in tree %s carries position %d; unilevel trees have no slots",
				payload.UserID, payload.TreeID, *payload.Position)
		}
	default:
		// Unreachable while supportedTreeTypes has three entries, but that
		// map's comment says to expect a fourth. Mirror validateNodes: a new
		// type must fail here loudly until its position rule is decided,
		// not fall through and admit whatever the event carries.
		return fmt.Errorf("node_placed for %s in tree %s has type %q with no position rule (add one to checkNodePlacedShape)",
			payload.UserID, payload.TreeID, payload.TreeType)
	}
	return nil
}

// NodeRemovedPayload is the event payload when a leaf node is removed.
type NodeRemovedPayload struct {
	TreeID    string    `json:"tree_id"`
	UserID    string    `json:"user_id"`
	RemovedAt time.Time `json:"removed_at"`
}
