package platform

import "context"

// ConfigStore provides read-only access to system configuration.
// Each context reads its own config namespace. Config is loaded at startup
// from YAML/environment and is not mutated at runtime. Domain-specific
// business rules belong in the owning context's config, not here.
type ConfigStore interface {
	// Get retrieves a config value by key. Returns error if not found.
	Get(ctx context.Context, key string) (string, error)

	// GetWithDefault retrieves a config value, returning the default if not found.
	GetWithDefault(ctx context.Context, key string, defaultVal string) string

	// GetAll retrieves all config values matching a key prefix.
	GetAll(ctx context.Context, prefix string) (map[string]string, error)
}

// AuditWriter records structured audit events. Write-only append.
// Every context writes its own audit events through this interface.
// Operations reads the audit store for reporting via a separate query path.
type AuditWriter interface {
	// Write records a single audit event. Never fails silently.
	Write(ctx context.Context, event AuditEvent) error
}

// JobScheduler manages background job registration and execution.
// Contexts register jobs at startup. The scheduler calls handlers
// at the scheduled time. Jobs are the migration path for legacy
// cron bots (ADR, batch migration Phase 1).
type JobScheduler interface {
	// Register adds a job to the scheduler. Duplicate IDs are rejected.
	Register(ctx context.Context, job JobDefinition) error

	// Cancel removes a scheduled job.
	Cancel(ctx context.Context, jobID string) error
}

// SessionManager handles session lifecycle. Stubbed — full design
// deferred until Portals tier when AuthProvider is also designed.
type SessionManager interface {
	Create(ctx context.Context, userID string, zone string) (Session, error)
	Validate(ctx context.Context, sessionID string) (Session, error)
	Destroy(ctx context.Context, sessionID string) error
}
