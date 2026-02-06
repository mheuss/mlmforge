package financial

import "time"

// ChargeRequest describes a payment to process.
type ChargeRequest struct {
	UserID          string
	Amount          float64
	Currency        string // Market's local currency
	PaymentMethodID string // References wallet
	OrderID         string // For correlation
	Description     string
}

// ChargeResult is the outcome of a charge attempt.
type ChargeResult struct {
	TransactionID    string
	Status           string // "approved", "declined", "error"
	GatewayReference string
	DeclineReason    string // If declined
}

// RefundRequest describes a refund to process.
type RefundRequest struct {
	TransactionID string // Original charge
	Amount        float64
	Reason        string
}

// RefundResult is the outcome of a refund attempt.
type RefundResult struct {
	TransactionID    string
	Status           string // "approved", "declined", "error"
	GatewayReference string
}

// Availability reports whether a payment method type is operational.
type Availability struct {
	PaymentMethod string
	Available     bool
	Reason        string // If unavailable
}

// PaymentMethod represents a saved payment instrument.
type PaymentMethod struct {
	ID              string
	UserID          string
	Type            string // "credit_card", "ach", "check"
	Last4           string
	ExpirationMonth int // Card only
	ExpirationYear  int // Card only
	IsDefault       bool
	Label           string // User-friendly ("Visa ending 4242")
}

// PaymentMethodInput is the data needed to save a new payment method.
type PaymentMethodInput struct {
	Type         string
	GatewayToken string // From client-side tokenization
	Label        string
}

// Invoice supports order-linked and standalone types. OrderID is empty
// for standalone invoices (fees, administrative charges).
type Invoice struct {
	ID          string
	UserID      string
	OrderID     string // Optional — empty for standalone invoices
	InvoiceType string // "order", "fee", "administrative", "adjustment"
	Amount      float64
	Currency    string // In the order's market currency
	Status      string // "pending", "paid", "void"
	LineItems   []InvoiceLineItem
	Description string // Important for standalone invoices
	IssuedAt    time.Time
	PaidAt      time.Time
}

// InvoiceLineItem is a single line on an invoice.
type InvoiceLineItem struct {
	Description string
	Quantity    int
	UnitPrice   float64
	Total       float64
}

// InvoiceFilter supports filtering invoices.
type InvoiceFilter struct {
	InvoiceType string // Optional
	Status      string // Optional
	DateFrom    time.Time
	DateTo      time.Time
	Page        int
	PageSize    int
}

// InvoicePage is a paginated result of invoices.
type InvoicePage struct {
	Invoices   []Invoice
	TotalCount int
	Page       int
	PageSize   int
}
