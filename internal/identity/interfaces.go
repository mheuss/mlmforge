package identity

import "context"

// UserReader provides read-only access to user identity data.
// Consumed by all contexts. Deliberately excludes sensitive fields
// (password hash is internal to Identity, tax ID is internal to Financial,
// sponsor_id is owned by Network Engine per ADR-009).
type UserReader interface {
	// GetByID retrieves a user by their unique ID.
	GetByID(ctx context.Context, userID string) (User, error)

	// GetByEmail retrieves a user by email address.
	GetByEmail(ctx context.Context, email string) (User, error)

	// GetByMemberNumber retrieves a user by their member number.
	GetByMemberNumber(ctx context.Context, memberNumber string) (User, error)

	// List returns users matching the filter criteria, paginated.
	List(ctx context.Context, filter UserFilter) (UserPage, error)
}

// AddressReader provides read-only access to user addresses.
// Internationalized — supports all address formats via flexible fields.
// Country-specific validation is enforced by Identity internally.
type AddressReader interface {
	// GetForUser retrieves a specific address type for a user.
	GetForUser(ctx context.Context, userID string, addressType string) (Address, error)

	// ListForUser retrieves all addresses for a user.
	ListForUser(ctx context.Context, userID string) ([]Address, error)
}

// StatusTransition is the command interface for user status changes.
// Identity owns the state machine and validates all transitions (ADR-006).
// State machines differ by account category. Other contexts request
// transitions — they do not directly mutate user status.
type StatusTransition interface {
	// RequestTransition requests a status change. Identity validates the
	// transition against the appropriate state machine and writes the
	// audit event. Returns error if the transition is invalid.
	RequestTransition(ctx context.Context, userID string, newStatus string, reason string) error
}

// AuthProvider handles authentication and authorization.
// Stubbed — full design deferred until Portals tier.
type AuthProvider interface {
	Authenticate(ctx context.Context, credentials Credentials) (AuthResult, error)
	ValidateSession(ctx context.Context, sessionID string) (Session, error)
	CheckPermission(ctx context.Context, userID string, permission string) (bool, error)
}
