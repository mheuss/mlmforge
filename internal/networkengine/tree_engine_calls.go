package networkengine

import (
	"context"
	"fmt"
)

const engineCodeTreeExists = "TREE_EXISTS"

// engineOutcome is what an engine call returned beyond its error.
type engineOutcome struct {
	responsored []Responsored
}

// engineCall is the engine mutation one tree event makes.
type engineCall struct {
	op     string
	userID string
	apply  func(ctx context.Context, engine TreeMutator) (engineOutcome, error)
}

// rootAddedCall returns the engine call a root_added event makes.
func rootAddedCall(p RootAddedPayload) engineCall {
	return engineCall{op: "add_root", userID: p.UserID,
		apply: func(ctx context.Context, engine TreeMutator) (engineOutcome, error) {
			return engineOutcome{}, engine.AddRoot(ctx, p.TreeID, p.UserID, p.EnrolledAt.Unix())
		}}
}

// nodePlacedCall returns the engine call a node_placed event makes.
func nodePlacedCall(p NodePlacedPayload) engineCall {
	// add_node would place a matrix node by spillover, not where the event
	// recorded it.
	if p.TreeType == treeTypeMatrix {
		return engineCall{op: "add_node_at", userID: p.UserID,
			apply: func(ctx context.Context, engine TreeMutator) (engineOutcome, error) {
				return engineOutcome{}, engine.AddNodeAt(ctx, p.TreeID, p.UserID,
					p.ParentID, p.SponsorID, *p.Position, p.EnrolledAt.Unix())
			}}
	}
	return engineCall{op: "add_node", userID: p.UserID,
		apply: func(ctx context.Context, engine TreeMutator) (engineOutcome, error) {
			var opts []AddNodeOption
			if p.Position != nil {
				opts = append(opts, WithPosition(*p.Position))
			}
			return engineOutcome{}, engine.AddNode(ctx, p.TreeID, p.UserID,
				p.ParentID, p.SponsorID, p.EnrolledAt.Unix(), opts...)
		}}
}

// nodeRemovedCall returns the engine call a node_removed event makes in a tree
// of treeType. An empty treeType means the caller does not know it.
func nodeRemovedCall(p NodeRemovedPayload, treeType string) (engineCall, error) {
	if treeType == treeTypeMatrix {
		return engineCall{}, fmt.Errorf(
			"node_removed for %s in tree %s: the caller names the tree matrix, and the payload carries no pruning mode",
			p.UserID, p.TreeID)
	}
	return engineCall{op: "remove_node", userID: p.UserID,
		apply: func(ctx context.Context, engine TreeMutator) (engineOutcome, error) {
			moved, err := engine.RemoveNode(ctx, p.TreeID, p.UserID)
			return engineOutcome{responsored: moved}, err
		}}, nil
}
