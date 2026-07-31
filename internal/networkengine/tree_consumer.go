package networkengine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
)

// TreeEventConsumer projects tree events into the adjacency table and
// Rust engine. It is called synchronously by the placement service
// after appending the event to the EventStore.
type TreeEventConsumer struct {
	store      TreeStore
	engine     TreeMutator
	maxRetries int
	retryDelay time.Duration
}

// NewTreeEventConsumer creates a consumer that projects events into
// the given store and engine. Default retry count is 2.
func NewTreeEventConsumer(store TreeStore, engine TreeMutator) *TreeEventConsumer {
	return &TreeEventConsumer{
		store:      store,
		engine:     engine,
		maxRetries: 2,
		retryDelay: 50 * time.Millisecond,
	}
}

// HandleEvent processes a single tree event. The store projection happens
// first. If it fails, the error is returned immediately (the event exists
// in the EventStore for replay). The engine projection retries on failure.
func (c *TreeEventConsumer) HandleEvent(ctx context.Context, event platform.Event) error {
	switch event.Type {
	case EventTypeRootAdded:
		return c.handleRootAdded(ctx, event)
	case EventTypeNodePlaced:
		return c.handleNodePlaced(ctx, event)
	case EventTypeNodeRemoved:
		return c.handleNodeRemoved(ctx, event)
	default:
		return nil
	}
}

func (c *TreeEventConsumer) handleRootAdded(ctx context.Context, event platform.Event) error {
	var payload RootAddedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal root_added payload: %w", err)
	}

	node := TreeNodeRow{
		ID:         event.ID,
		TreeID:     payload.TreeID,
		UserID:     payload.UserID,
		SponsorID:  &payload.SponsorID,
		Depth:      0,
		EnrolledAt: payload.EnrolledAt,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := c.store.InsertNode(ctx, node); err != nil {
		return fmt.Errorf("store root node: %w", err)
	}

	return c.withRetry(ctx, "add_root", payload.TreeID, payload.UserID, func() error {
		return c.engine.AddRoot(ctx, payload.TreeID, payload.UserID, payload.EnrolledAt.Unix())
	})
}

func (c *TreeEventConsumer) handleNodePlaced(ctx context.Context, event platform.Event) error {
	var payload NodePlacedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal node_placed payload: %w", err)
	}

	// Reject malformed payloads before either projection. A node_placed that
	// cannot be applied faithfully must not land anywhere: a stored row the
	// engine never honored is the divergence this consumer exists to prevent,
	// and LoadTree's validation refuses rows these checks would admit, so
	// storing one can make the whole tree unreloadable.
	if event.Stream != TreeStreamName(payload.TreeID) {
		return fmt.Errorf("node_placed for %s in tree %s arrived on stream %q, want %q",
			payload.UserID, payload.TreeID, event.Stream, TreeStreamName(payload.TreeID))
	}
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
		// The upper bound needs the tree's configured width, which nothing
		// persists yet (HEU-554). The engine still enforces it at runtime.
		if payload.Position == nil {
			return fmt.Errorf("matrix node_placed for %s in tree %s has no position; matrix events must carry explicit placement",
				payload.UserID, payload.TreeID)
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
	}

	// Look up parent depth to derive child depth.
	parent, err := c.store.GetNode(ctx, payload.TreeID, payload.ParentID)
	if err != nil {
		return fmt.Errorf("get parent node: %w", err)
	}
	if parent == nil {
		return fmt.Errorf("parent node %s not found in tree %s", payload.ParentID, payload.TreeID)
	}
	depth := parent.Depth + 1

	node := TreeNodeRow{
		ID:         event.ID,
		TreeID:     payload.TreeID,
		UserID:     payload.UserID,
		ParentID:   &payload.ParentID,
		SponsorID:  &payload.SponsorID,
		Position:   payload.Position,
		Depth:      depth,
		EnrolledAt: payload.EnrolledAt,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := c.store.InsertNode(ctx, node); err != nil {
		return fmt.Errorf("store placed node: %w", err)
	}

	// HEU-553: for a matrix tree this stores the requested placement above and
	// then calls AddNode, whose matrix arm ignores parent_id and position and
	// re-derives by spillover. The row and the engine can therefore disagree.
	//
	// That disagreement is now observable: LoadTree replays matrix nodes at
	// their stored slots, so a restart reshapes the live tree to match the row.
	// A matrix node_placed event must carry engine-derived placement, not
	// requested placement, until HEU-553 routes this through AddNodeAt.
	return c.withRetry(ctx, "add_node", payload.TreeID, payload.UserID, func() error {
		var opts []AddNodeOption
		if payload.Position != nil {
			opts = append(opts, WithPosition(*payload.Position))
		}
		return c.engine.AddNode(ctx, payload.TreeID, payload.UserID, payload.ParentID, payload.SponsorID, payload.EnrolledAt.Unix(), opts...)
	})
}

func (c *TreeEventConsumer) handleNodeRemoved(ctx context.Context, event platform.Event) error {
	var payload NodeRemovedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal node_removed payload: %w", err)
	}

	if err := c.store.DeleteNode(ctx, payload.TreeID, payload.UserID); err != nil {
		return fmt.Errorf("soft-delete node: %w", err)
	}

	return c.withRetry(ctx, "remove_node", payload.TreeID, payload.UserID, func() error {
		return c.engine.RemoveNode(ctx, payload.TreeID, payload.UserID)
	})
}

// withRetry executes fn with retries. Respects context cancellation between
// attempts. Logs at ERROR level when all retries are exhausted (interim
// notification path until HEU-296).
func (c *TreeEventConsumer) withRetry(ctx context.Context, op, treeID, userID string, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			if attempt < c.maxRetries {
				select {
				case <-ctx.Done():
					return fmt.Errorf("engine %s cancelled during retry: %w", op, ctx.Err())
				case <-time.After(c.retryDelay):
				}
			}
			continue
		}
		return nil
	}

	log.Printf("ERROR tree consumer: engine %s failed after %d retries tree_id=%s user_id=%s err=%v",
		op, c.maxRetries, treeID, userID, lastErr)
	return fmt.Errorf("engine %s failed after %d retries: %w", op, c.maxRetries, lastErr)
}
