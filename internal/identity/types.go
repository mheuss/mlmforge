package identity

import "time"

// User is the read-only projection of a user that other contexts see.
// Account type has two levels: AccountCategory for broad type (95% of
// business logic) and AccountClassification for specific variant (tax,
// legal compliance, enrollment forms).
type User struct {
	ID                    string
	Email                 string
	MemberNumber          string
	FirstName             string
	LastName              string
	MarketingName         string
	CompanyName           string
	AccountCategory       string // "distributor", "customer", "prospect"
	AccountClassification string // "individual", "married_partnership", "non_profit", "business", "church", "retail"
	Status                string // Identity owns the state machine
	RankID                string // Cached — Network Engine is source of truth
	MarketID              string // Regional market
	PreferredCurrency     string // For display and payout
	PreferredLocale       string // Language/locale for communications
	Country               string // ISO 3166-1 alpha-2
	CreatedAt             time.Time
}

// UserFilter supports filtering by common user attributes.
type UserFilter struct {
	AccountCategory       string // Optional
	AccountClassification string // Optional
	Status                string // Optional
	MarketID              string // Optional
	DateFrom              time.Time
	DateTo                time.Time
	Page                  int
	PageSize              int
}

// UserPage is a paginated result of users.
type UserPage struct {
	Users      []User
	TotalCount int
	Page       int
	PageSize   int
}

// Address supports international formats. Region covers state (US),
// province (CA), prefecture (JP), county (UK), etc. Meta holds
// country-specific extensions (district, building name, floor number).
type Address struct {
	ID          string
	UserID      string
	Type        string // "shipping", "billing", "mailing"
	Street1     string
	Street2     string
	City        string
	Region      string // State, province, prefecture, county, etc.
	PostalCode  string
	Country     string            // ISO 3166-1 alpha-2. Required. Drives validation.
	PhoneNumber string            // E.164 format (+1xxxyyyzzzz)
	Meta        map[string]string // Country-specific fields
}

// Credentials is the authentication input. Stubbed — details deferred
// until Portals tier.
type Credentials struct {
	Email    string
	Password string
}

// AuthResult is the authentication output. Stubbed — details deferred
// until Portals tier.
type AuthResult struct {
	UserID    string
	SessionID string
	Success   bool
}

// Session is a reference to a Platform session.
// Re-declared here for AuthProvider's return type. When Platform's
// SessionManager is fully designed, this may be replaced with an import.
type Session struct {
	ID        string
	UserID    string
	Zone      string
	CreatedAt time.Time
	ExpiresAt time.Time
}
