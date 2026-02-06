package engagement

import "context"

// MessageSender is the consolidated messaging interface. Handles
// transactional, templated, and bulk messages. Blacklist enforcement
// is internal — callers submit, Engagement filters.
type MessageSender interface {
	// Send queues a single message with raw content.
	// For contexts that want full control over the body.
	Send(ctx context.Context, req MessageRequest) (MessageResult, error)

	// SendBulk queues a broadcast to multiple recipients with filtering.
	SendBulk(ctx context.Context, req BulkMessageRequest) (BulkMessageResult, error)

	// SendTemplated queues a form letter. Engagement owns token resolution —
	// templates declare what they need (user, sponsor, rank, commission data)
	// and Engagement queries the appropriate read interfaces internally.
	SendTemplated(ctx context.Context, req TemplatedMessageRequest) (MessageResult, error)
}
