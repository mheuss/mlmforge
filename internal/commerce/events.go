package commerce

import "time"

// --- Commerce Domain Events ---
// Each carries enough data for consumers to react without calling back
// into Commerce.

// OrderCompleted is emitted when a store or signup order is finalized.
type OrderCompleted struct {
	OrderID     string
	UserID      string
	MarketID    string
	OrderType   string // "store", "signup"
	Items       []OrderItem
	Total       float64
	Currency    string
	CompletedAt time.Time
}

// OrderItem is a line item in an order event.
type OrderItem struct {
	ProductID string
	Quantity  int
	Price     float64
	Currency  string
	CVPoints  float64
}

// OrderRefunded is emitted when a voluntary refund is processed.
type OrderRefunded struct {
	OrderID      string
	UserID       string
	RefundAmount float64
	Currency     string
	Items        []RefundItem
	Reason       string
	RefundedAt   time.Time
}

// RefundItem is a line item in a refund event.
type RefundItem struct {
	ProductID string
	Quantity  int
	Amount    float64
	CVPoints  float64
}

// OrderChargedBack is emitted when the customer's bank initiates a dispute.
// Distinct from refund: involuntary, may involve fees, triggers account review.
type OrderChargedBack struct {
	OrderID          string
	UserID           string
	ChargebackAmount float64
	Currency         string
	DisputeReason    string
	GatewayDisputeID string
	ChargedBackAt    time.Time
}

// AutoshipCreated is emitted when a new autoship subscription is set up.
type AutoshipCreated struct {
	AutoshipID  string
	UserID      string
	Items       []AutoshipItem
	Frequency   string
	NextRunDate time.Time
}

// AutoshipProcessed is emitted when an autoship order is successfully
// charged and created.
type AutoshipProcessed struct {
	AutoshipID  string
	OrderID     string
	UserID      string
	Items       []AutoshipItem
	Total       float64
	Currency    string
	CVPoints    float64
	ProcessedAt time.Time
}

// AutoshipCancelled is emitted when an autoship subscription is terminated.
type AutoshipCancelled struct {
	AutoshipID  string
	UserID      string
	Reason      string
	CancelledAt time.Time
}

// AutoshipChargedBack is emitted when a bank disputes a recurring charge.
type AutoshipChargedBack struct {
	AutoshipID       string
	OrderID          string
	UserID           string
	ChargebackAmount float64
	Currency         string
	DisputeReason    string
	GatewayDisputeID string
	ChargedBackAt    time.Time
}
