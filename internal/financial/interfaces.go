package financial

import "context"

// PaymentProcessor is the command interface for charging payments.
// Gateway abstraction is internal — consumers don't know which gateway
// is used. Handles charges in any market currency; the gateway manages
// cross-currency processing.
type PaymentProcessor interface {
	// Charge processes a payment against a saved payment method.
	Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error)

	// Refund reverses a previous charge. Supports partial refunds.
	Refund(ctx context.Context, req RefundRequest) (RefundResult, error)

	// CheckAvailability reports whether a payment method type is configured.
	CheckAvailability(ctx context.Context, paymentMethod string) (Availability, error)
}

// WalletManager handles saved payment method CRUD. Uses gateway
// tokenization — no raw card or bank data is stored in MLMForge.
type WalletManager interface {
	GetForUser(ctx context.Context, userID string) ([]PaymentMethod, error)
	Add(ctx context.Context, userID string, input PaymentMethodInput) (PaymentMethod, error)
	Remove(ctx context.Context, userID string, paymentMethodID string) error
	SetDefault(ctx context.Context, userID string, paymentMethodID string) error
}

// InvoiceProvider provides read-only access to invoices.
// Supports both order-linked and standalone invoices (fees,
// administrative charges, adjustments).
type InvoiceProvider interface {
	GetInvoice(ctx context.Context, invoiceID string) (Invoice, error)
	ListForUser(ctx context.Context, userID string, filter InvoiceFilter) (InvoicePage, error)
	ListForPeriod(ctx context.Context, period string, filter InvoiceFilter) (InvoicePage, error)
}
