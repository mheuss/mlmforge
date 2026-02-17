package operations

import "time"

// --- Ticket types ---

// Ticket represents a customer service ticket.
type Ticket struct {
	ID              string
	UserID          string
	AdminUserID     string // Assigned agent
	Subject         string
	Description     string
	Status          string // "open", "in_progress", "awaiting_customer", "resolved", "closed"
	Priority        string // "low", "normal", "high", "urgent"
	RelatedEntities []RelatedEntity
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ClosedAt        time.Time
}

// TicketRequest is the data needed to create a ticket.
type TicketRequest struct {
	UserID          string
	Subject         string
	Description     string
	Priority        string
	RelatedEntities []RelatedEntity
}

// RelatedEntity links a ticket to any system entity without coupling
// the ticket schema to specific entity types.
type RelatedEntity struct {
	EntityType  string // "order", "commission", "autoship", "chargeback", "user", "product", "structure"
	EntityID    string
	Description string // Human-readable ("Binary tree placement dispute")
}

// TicketResponse is a single reply on a ticket.
type TicketResponse struct {
	AuthorID   string
	AuthorType string // "admin", "customer", "system"
	Body       string
	CreatedAt  time.Time
}

// TicketFilter supports filtering tickets.
type TicketFilter struct {
	Status            string // Optional
	Priority          string // Optional
	AdminUserID       string // Optional
	UserID            string // Optional
	RelatedEntityType string // Optional
	RelatedEntityID   string // Optional
	DateFrom          time.Time
	DateTo            time.Time
	Page              int
	PageSize          int
}

// TicketPage is a paginated result of tickets.
type TicketPage struct {
	Tickets    []Ticket
	TotalCount int
	Page       int
	PageSize   int
}

// --- Report types ---

// ReportDefinition describes an available report.
type ReportDefinition struct {
	ID          string
	Name        string
	Description string
	Category    string // "performance", "sales", "commission", "membership", "compliance"
	Parameters  []ReportParameter
}

// ReportParameter describes a parameter a report accepts.
type ReportParameter struct {
	Name     string
	Type     string // "string", "date", "select"
	Required bool
	Options  []string // For select type
}

// ReportRequest is the input to run a report.
type ReportRequest struct {
	ReportID   string
	Parameters map[string]string
	Page       int
	PageSize   int
}

// ReportResult is the output of a report run.
type ReportResult struct {
	ReportID  string
	Columns   []ColumnDefinition
	Rows      [][]any
	TotalRows int
	Summary   map[string]any // Aggregate totals if applicable
}

// ColumnDefinition describes a column in a report result.
type ColumnDefinition struct {
	Name string
	Type string // "string", "number", "date", "currency"
}

// ExportFormat specifies a report export file format.
type ExportFormat string

const (
	ExportCSV  ExportFormat = "csv"
	ExportPDF  ExportFormat = "pdf"
	ExportXLSX ExportFormat = "xlsx"
)

// --- Content types ---

// Content represents a CMS-managed page.
type Content struct {
	ID            string
	Slug          string // URL-friendly identifier
	Title         string
	Body          string // HTML or markdown
	Status        string // "published", "draft", "archived"
	CreatedAt     time.Time
	UpdatedAt     time.Time
	AuthorAdminID string
}

// ContentFilter supports filtering content.
type ContentFilter struct {
	Status   string // Optional
	Page     int
	PageSize int
}

// ContentPage is a paginated result of content items.
type ContentPage struct {
	Items      []Content
	TotalCount int
	Page       int
	PageSize   int
}
