package engagement

import "time"

// --- Engagement Domain Events ---

// ProspectCreated is emitted when a new prospect is captured.
type ProspectCreated struct {
	ProspectUserID string
	SponsorUserID  string
	Source         string // "co_op", "lead_capture", "manual", "referral"
	MarketID       string
	CreatedAt      time.Time
}

// Additional domain events deferred until their features are designed:
// - CoopSharePurchased: Financial listens for income recording. Defined when co-op features are built.
// - EventRegistrationPaid: Financial listens for income recording. Defined when event features are built.
