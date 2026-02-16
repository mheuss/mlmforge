package platform

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const createEventsTableSQL = `
CREATE TABLE IF NOT EXISTS events (
    global_position BIGSERIAL    PRIMARY KEY,
    id              UUID         NOT NULL UNIQUE,
    stream          TEXT         NOT NULL,
    type            TEXT         NOT NULL,
    version         BIGINT       NOT NULL,
    payload         JSONB        NOT NULL,
    metadata        JSONB,
    timestamp       TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT events_stream_version_key UNIQUE (stream, version)
);

CREATE INDEX IF NOT EXISTS idx_events_category
    ON events (split_part(stream, '-', 1), global_position);
`

// Compile-time check: PostgresEventStore implements EventStore.
var _ EventStore = (*PostgresEventStore)(nil)

// PostgresEventStore is an EventStore backed by PostgreSQL.
// Events are stored in a single table with JSONB payloads.
type PostgresEventStore struct {
	pool *pgxpool.Pool
}

// NewPostgresEventStore creates a PostgreSQL-backed event store.
func NewPostgresEventStore(pool *pgxpool.Pool) *PostgresEventStore {
	return &PostgresEventStore{pool: pool}
}

// CreateSchema creates the events table and indexes if they don't exist.
func (s *PostgresEventStore) CreateSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, createEventsTableSQL)
	return err
}

// Append writes events to a stream atomically with optimistic concurrency.
// Uses a database transaction. The (stream, version) unique constraint
// enforces concurrency at the database level.
func (s *PostgresEventStore) Append(ctx context.Context, stream string, expectedVersion int64, events []NewEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Query current stream version once. Used for concurrency check (when
	// expectedVersion >= 0) and as the base for version numbering (when < 0).
	var currentVersion int64
	err = tx.QueryRow(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM events WHERE stream = $1",
		stream,
	).Scan(&currentVersion)
	if err != nil {
		return err
	}

	if expectedVersion >= 0 && currentVersion != expectedVersion {
		return &ConcurrencyError{
			Stream:          stream,
			ExpectedVersion: expectedVersion,
			ActualVersion:   currentVersion,
		}
	}

	// When skipping version check, base versions on the current max.
	startVersion := expectedVersion
	if expectedVersion < 0 {
		startVersion = currentVersion
	}

	for i, ne := range events {
		version := startVersion + int64(i) + 1

		_, err := tx.Exec(ctx,
			`INSERT INTO events (id, stream, type, version, payload, metadata)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			ne.ID, stream, ne.Type, version, ne.Payload, ne.Metadata,
		)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.ConstraintName == "events_stream_version_key" {
				return &ConcurrencyError{
					Stream:          stream,
					ExpectedVersion: expectedVersion,
					ActualVersion:   version - 1,
				}
			}
			return err
		}
	}

	return tx.Commit(ctx)
}

// ReadStream returns events from a single stream starting at fromVersion.
func (s *PostgresEventStore) ReadStream(ctx context.Context, stream string, fromVersion int64) ([]Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT global_position, id, stream, type, version, payload, metadata, timestamp
		 FROM events
		 WHERE stream = $1 AND version >= $2
		 ORDER BY version`,
		stream, fromVersion,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEvents(rows)
}

// ReadCategory returns events across streams matching a category prefix.
func (s *PostgresEventStore) ReadCategory(ctx context.Context, category string, afterPosition int64) ([]Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT global_position, id, stream, type, version, payload, metadata, timestamp
		 FROM events
		 WHERE split_part(stream, '-', 1) = $1 AND global_position > $2
		 ORDER BY global_position`,
		category, afterPosition,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEvents(rows)
}

// scanEvents reads all rows into a slice of Event.
func scanEvents(rows pgx.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var e Event
		err := rows.Scan(
			&e.GlobalPosition, &e.ID, &e.Stream, &e.Type,
			&e.Version, &e.Payload, &e.Metadata, &e.Timestamp,
		)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
