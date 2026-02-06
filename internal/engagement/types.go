package engagement

// MessageRequest describes a single raw-content message.
type MessageRequest struct {
	RecipientUserID string
	Channel         string // "email" initially. Interface doesn't preclude "sms", "push".
	Subject         string
	Body            string
	FromContext     string // Which context is sending — for audit
}

// TemplatedMessageRequest — callers just specify the template and recipient.
// Engagement resolves standard tokens. AdditionalTokens handles edge cases.
type TemplatedMessageRequest struct {
	RecipientUserID  string
	TemplateID       string
	Channel          string
	AdditionalTokens map[string]string // Optional context-specific data
	FromContext      string
}

// BulkMessageRequest describes a broadcast to multiple recipients.
type BulkMessageRequest struct {
	TemplateID      string
	Channel         string
	RecipientFilter RecipientFilter // By rank, account type, market, status, structure
	FromContext     string
}

// RecipientFilter selects recipients for bulk messages.
// Supports filtering by rank, account type, market, status, and structure.
type RecipientFilter struct {
	AccountCategory string // Optional
	Status          string // Optional
	MarketID        string // Optional
	RankID          string // Optional
	StructureID     string // Optional
}

// MessageResult is the outcome of sending a single message.
type MessageResult struct {
	MessageID       string
	Status          string // "queued", "rejected"
	RejectionReason string // If blacklisted or invalid
}

// BulkMessageResult is the outcome of a bulk send.
type BulkMessageResult struct {
	BatchID       string
	TotalQueued   int
	TotalRejected int // Blacklisted recipients filtered automatically
}
