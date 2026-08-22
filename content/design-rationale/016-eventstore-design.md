# 016: EventStore Design

## The Problem

Domain events need persistent storage. Network Engine rebuilds commission state from event replay (event sourcing). All bounded contexts communicate through domain events (cross-context data flow, decision 004). The legacy system had no event persistence. Events were fire-and-forget function calls. If a handler failed or the system crashed, the event was lost.

Two distinct use cases share the same storage pattern: append-only writes and sequential reads. Event sourcing writes to a single stream and replays in version order. Domain event consumers read across all streams of a category in insertion order. Both need optimistic concurrency to prevent conflicting writes.

## Decisions

### Unified Store

A single `EventStore` handles both event sourcing and domain events. The API is the same: append events to a named stream, read a stream by version, read a category by global position. Stream names distinguish the use case. `commission-period-2026-01` is event sourcing. `order-abc123` is a domain event stream.

We considered separate stores for each pattern. The read/write semantics are identical. Separate stores would duplicate the interface, the schema, the concurrency logic, and the testing. One store with category-based reads covers both cases.

### JSON Envelope

Events use a common `Event` envelope. The payload is `json.RawMessage`. The store writes and reads the payload as opaque JSON. It does not interpret, validate, or deserialize the contents.

```go
type Event struct {
    ID             string
    Stream         string
    Type           string
    Version        int64
    GlobalPosition int64
    Payload        json.RawMessage
    Metadata       json.RawMessage
    Timestamp      time.Time
}
```

Callers marshal their domain events (like `OrderCompleted`) into JSON before calling `Append`. This keeps the store decoupled from domain types. Adding a new event type requires no store changes.

The alternative was a generic `any` payload with type registration. This adds complexity for no benefit. JSON is the wire format. Domain events are already JSON-serializable.

### Category-ID Stream Naming

Streams follow the convention `{category}-{id}`. The category is everything before the first hyphen. `order-abc123` has category `order`. `commission-period-2026-01` has category `commission`.

`ReadCategory` uses this convention to query across all streams of a type. In PostgreSQL, `split_part(stream, '-', 1)` extracts the category. A functional index makes the query efficient without a separate column.

We considered a separate `category` column. The naming convention is simpler. It avoids schema redundancy and forces consistent stream naming across all contexts.

### Optimistic Concurrency

`Append` takes an `expectedVersion` parameter. If the stream's current version does not match, the append fails with a `ConcurrencyError`.

Three modes:
- `expectedVersion = 0`. Stream must be new (no events yet)
- `expectedVersion = N`. Stream must have exactly N events
- `expectedVersion = -1`. Skip the check, append unconditionally

The PostgreSQL implementation enforces concurrency at two levels. The application checks `MAX(version)` before inserting. The `UNIQUE(stream, version)` constraint catches races that slip through the application check. Both produce the same `ConcurrencyError`.

We considered pessimistic locking (`SELECT FOR UPDATE`). Optimistic concurrency is simpler and performs better under low contention, which is the expected case for stream writes. Concurrent writes to the same stream are a bug in most scenarios, not a normal workload.

### Pagination

Both `ReadStream` and `ReadCategory` accept a `limit int64` parameter. A limit of 0 means "read all matching events." A positive limit caps the result set.

`ReadStream` also takes `fromVersion` (inclusive start position within a stream). `ReadCategory` takes `afterPosition` (exclusive cursor against the global insertion order). Together, these support incremental consumption: read a batch, record the last position, resume from there.

The 0-means-unlimited convention avoids a separate "read all" method. Callers that need full replay pass 0. Callers that need bounded reads pass a positive value. The PostgreSQL implementation adds `LIMIT $N` to the query only when the limit is positive.

### Platform Ownership

The `EventStore` interface and implementations live in `internal/platform/`, alongside `ConfigStore`, `AuditWriter`, and `JobScheduler`. Platform provides infrastructure. All contexts import from Platform.

We considered putting the interface in a shared contracts package. This contradicts decision 004: interfaces live in the provider's package. We considered putting it in Network Engine since event sourcing is its primary use case. But domain events are system-wide. Platform is the right home for shared infrastructure.

### pgx v5 Driver

The PostgreSQL implementation uses `github.com/jackc/pgx/v5` with `pgxpool` for connection pooling. No `database/sql` abstraction layer.

The project is committed to PostgreSQL (see ADR-005 in DEVELOPMENT.md). There is no need for database portability. pgx provides native JSONB support, connection pooling, and direct access to PostgreSQL-specific features like `split_part` in queries. The `database/sql` abstraction would add indirection without benefit.

### Two Implementations

`PostgresEventStore` for production. `MemoryEventStore` for testing. Both satisfy the same `EventStore` interface.

The in-memory implementation uses a mutex-protected map and slice. It provides the same behavioral guarantees as PostgreSQL: atomic multi-event appends, version-based concurrency, category matching via first-hyphen extraction.

Unit tests run against the in-memory implementation. Integration tests run against PostgreSQL when a connection string is available. Both implementations are tested against the same behavioral expectations.

## What We Considered

**EventStoreDB or Kafka.** Purpose-built event stores. Adds operational complexity that is not justified at this stage. The `EventStore` interface is the abstraction point. Enterprise deployments can swap in a specialized store without changing any consumer.

**Separate streams table.** A `streams` table tracking current version per stream, avoiding `MAX(version)` queries. Adds a second table to maintain atomically. PostgreSQL's `COALESCE(MAX(version), 0)` is efficient with the unique constraint index. The extra table is premature optimization.

**Snapshot support.** Periodic snapshots to avoid replaying the full event history. Deferred until the commission engine is implemented. The interface does not preclude adding snapshots later.

**Retention and partitioning.** Compact mode (purge after window) vs full mode (retain forever). Deferred to a separate task. The append-only table can be partitioned later without changing the interface.

## What This Enables

- **Event sourcing for Network Engine.** Commission periods, volume attribution, and rank changes are stored as events. State is rebuilt by replaying the stream. No mutable state tables.
- **Domain event persistence.** `OrderCompleted`, `AutoshipProcessed`, and other cross-context events survive process crashes. Consumers can replay from a position to catch up after failures.
- **Audit trail.** Every event is immutable and timestamped. The global position provides a total ordering across all streams.
- **Clean extraction path.** The `EventStore` interface can be backed by PostgreSQL, EventStoreDB, or a message broker. Consumer code does not change.
