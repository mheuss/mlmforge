package operations

import "context"

// TicketManager handles customer service ticket lifecycle.
// Tickets can reference any entity in the system via generic
// related entities — orders, commissions, autoships, chargebacks,
// tree placements, products, etc.
type TicketManager interface {
	Create(ctx context.Context, req TicketRequest) (Ticket, error)
	GetByID(ctx context.Context, ticketID string) (Ticket, error)
	List(ctx context.Context, filter TicketFilter) (TicketPage, error)
	AddResponse(ctx context.Context, ticketID string, resp TicketResponse) error
	UpdateStatus(ctx context.Context, ticketID string, newStatus string) error
	Assign(ctx context.Context, ticketID string, adminUserID string) error
}

// ReportRunner provides cross-cutting reporting (ADR-011).
// Operations owns reports spanning 2+ contexts. Single-context
// reports live with their owning context. Generic runner pattern —
// reports are data-driven, not code-driven per report type.
type ReportRunner interface {
	// ListReports returns available report definitions.
	ListReports(ctx context.Context) ([]ReportDefinition, error)

	// RunReport executes a report with the given parameters, paginated.
	RunReport(ctx context.Context, req ReportRequest) (ReportResult, error)

	// ExportReport generates a downloadable file in the requested format.
	ExportReport(ctx context.Context, req ReportRequest, format ExportFormat) ([]byte, error)
}

// ContentManager provides simple CMS for admin-managed back office
// pages (announcements, policy documents, training materials).
// Status-based versioning: published, draft, archived.
type ContentManager interface {
	GetContent(ctx context.Context, contentID string) (Content, error)
	GetBySlug(ctx context.Context, slug string) (Content, error)
	List(ctx context.Context, filter ContentFilter) (ContentPage, error)
}
