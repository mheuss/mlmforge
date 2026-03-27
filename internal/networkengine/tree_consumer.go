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
	engine     *EngineClient
	maxRetries int
	retryDelay time.Duration
}

// NewTreeEventConsumer creates a consumer that projects events into
// the given store and engine. Default retry count is 2.
func NewTreeEventConsumer(store TreeStore, engine *EngineClient) *TreeEventConsumer {
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

	// Look up parent depth to derive child depth.
	parent, err := c.store.GetNode(ctx, payload.TreeID, payload.ParentID)
	if err != nil {
		return fmt.Errorf("get parent node: %w", err)
	}
	depth := 0
	if parent != nil {
		depth = parent.Depth + 1
	}

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

// withRetry executes fn with retries. Logs at ERROR level when all retries
// are exhausted (interim notification path until HEU-296).
func (c *TreeEventConsumer) withRetry(_ context.Context, op, treeID, userID string, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			if attempt < c.maxRetries {
				time.Sleep(c.retryDelay)
			}
			continue
		}
		return nil
	}

	log.Printf("ERROR tree consumer: engine %s failed after %d retries tree_id=%s user_id=%s err=%v",
		op, c.maxRetries, treeID, userID, lastErr)
	return fmt.Errorf("engine %s failed after %d retries: %w", op, c.maxRetries, lastErr)
}
