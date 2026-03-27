package networkengine

import (
	"context"
	"fmt"
)

// TreeLoader rebuilds the Rust engine's in-memory state from the
// adjacency table on startup.
type TreeLoader struct {
	store  TreeStore
	engine *EngineClient
}

func NewTreeLoader(store TreeStore, engine *EngineClient) *TreeLoader {
	return &TreeLoader{store: store, engine: engine}
}

// LoadTree reads all active nodes for a tree from the store (depth-ordered)
// and replays them into the engine via CreateTree + AddRoot/AddNode.
func (l *TreeLoader) LoadTree(ctx context.Context, treeID, treeType string) error {
	nodes, err := l.store.GetByTreeDepthOrdered(ctx, treeID)
	if err != nil {
		return fmt.Errorf("load tree %s: %w", treeID, err)
	}
	if len(nodes) == 0 {
		return nil
	}

	if err := l.engine.CreateTree(ctx, treeID, treeType); err != nil {
		return fmt.Errorf("create tree %s: %w", treeID, err)
	}

	for i, node := range nodes {
		if i == 0 {
			if err := l.engine.AddRoot(ctx, treeID, node.UserID, node.EnrolledAt.Unix()); err != nil {
				return fmt.Errorf("add root %s: %w", node.UserID, err)
			}
			continue
		}

		parentID := ""
		if node.ParentID != nil {
			parentID = *node.ParentID
		}
		sponsorID := ""
		if node.SponsorID != nil {
			sponsorID = *node.SponsorID
		}

		var opts []AddNodeOption
		if node.Position != nil {
			opts = append(opts, WithPosition(*node.Position))
		}

		if err := l.engine.AddNode(ctx, treeID, node.UserID, parentID, sponsorID, node.EnrolledAt.Unix(), opts...); err != nil {
			return fmt.Errorf("add node %s: %w", node.UserID, err)
		}
	}
	return nil
}
