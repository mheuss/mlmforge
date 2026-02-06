# 004: Interface Contracts

## The Problem

MLMForge is a modular monolith designed for eventual service extraction. The interfaces between bounded contexts are the seams along which the system can be decomposed. Get them right and extraction is a deployment decision. Get them wrong and extraction becomes a rewrite.

The legacy system had no formal interfaces. Subsystems called each other's internal functions and joined each other's database tables freely.

## Decisions

### Interfaces Live in the Provider's Package

Each interface is defined in the package of the context that implements it. `identity.UserReader` lives in `internal/identity/`. Consumers import from the provider's package.

The provider knows what it can deliver. Identity defines `UserReader` and controls what data is exposed. Password hashes are deliberately excluded. Consumers that only need a subset of methods can define narrower local interfaces. This is idiomatic Go.

### Read/Write Separation

Interfaces are split by access pattern.

Read interfaces provide data without side effects: `UserReader`, `CommissionResult`, `ProductCatalog`, `InvoiceProvider`.

Command interfaces mutate state: `StatusTransition`, `CommissionAdmin`, `VolumeRecorder`, `StructurePlacer`.

The clearest example is Network Engine's commission interfaces. `CommissionResult` provides read-only access to data, summaries, and projections. `CommissionAdmin` runs commissions, approves periods, and triggers projections. The distributor back office only needs the reader. The nightly batch job only needs the admin. Each consumer depends on exactly what it needs.

### Domain Events for Cross-Context Data Flow

When one context needs to inform others about something that happened, it emits a domain event.

Commerce emits `OrderCompleted` when an order is finalized. Network Engine listens and records volume. Financial listens and records income. Commerce does not need to know its consumers.

Events carry enough data for consumers to react without calling back into the producer. `OrderCompleted` includes the full item list with CV points. Network Engine does not need to ask Commerce what was in the order.

This is critical for extensibility. A future CRM integration can listen to `OrderCompleted` without modifying Commerce.

### Event Bus Strategy

Events start as in-process function calls behind Go interfaces. When a context is extracted to a service, the implementation is swapped for NATS without changing producer or consumer code.

The system is designed for async semantics from day one. Handlers are idempotent. Delivery is at-least-once. This avoids the trap of relying on synchronous guarantees that will not exist after extraction.

### Stubbed Interfaces

`AuthProvider` (Identity) and `SessionManager` (Platform) are defined but intentionally stubbed. Their method signatures exist for completeness. The full design is deferred until the Portals tier when authentication and session management are actually needed.

They are defined now because other interfaces reference session concepts. Having the types in place prevents forward-declaration problems without committing to a premature design.

### Signatures Use Domain Types

Interface methods use domain-specific types, not primitives.

```go
RecordVolume(ctx context.Context, source VolumeSource) ([]VolumeAttribution, error)
```

Every parameter and return type is a named struct with documented fields. This makes interfaces readable without external documentation. Adding a field to a request type does not change any method signature.

### The Rust Boundary is Invisible

Network Engine's 7 interfaces are pure Go. Consumers have no idea Rust is involved. The package handles FFI/gRPC communication internally.

Consumers do not need Rust tooling. The Rust engine can be replaced or upgraded without touching any consumer. Testing consumers requires only a Go mock, not a running Rust binary.

## What We Considered

**Consumer-defined interfaces.** Each consumer defines what it needs from a provider. This fragments the contract. Multiple consumers might define slightly different versions of the same interface.

**Shared interface package.** A separate `internal/contracts/` package that all contexts import. This creates a central dependency that all contexts must agree on simultaneously. Provider-owned interfaces let each context evolve its API independently.

**gRPC from day one.** Define all interfaces as Protocol Buffer services. This adds serialization overhead and tooling complexity that is not needed in the monolith phase. Go interfaces are simpler and sufficient until extraction.

**Command-only communication.** All cross-context communication through synchronous calls, no events. This creates tight coupling. Commerce would need to know every system that cares about order completion.

## Interface Inventory

24 interfaces across 7 provider contexts. Portals is a pure consumer with no interfaces.

| Context | Interfaces |
|---------|-----------|
| Platform | ConfigStore, AuditWriter, JobScheduler, SessionManager |
| Identity | UserReader, AddressReader, StatusTransition, AuthProvider |
| Network Engine | TreeNavigator, RankProvider, VolumeRecorder, CommissionResult, CommissionAdmin, StructurePlacer, PlanConfiguration |
| Financial | PaymentProcessor, WalletManager, InvoiceProvider |
| Commerce | ProductCatalog, AutoshipManager |
| Engagement | MessageSender |
| Operations | TicketManager, ReportRunner, ContentManager |

Plus 10 domain events: 7 from Commerce (order and autoship lifecycle), 1 from Engagement (prospect creation), and 2 deferred until their features are built.

## What This Enables

- **Interface stability.** Adding methods is safe. Changing existing signatures requires coordinated updates. This is by design.
- **Go mocks for testing.** All interfaces are pure Go. Consumers can be tested with standard mocks without running any other context.
- **Clear extraction path.** Replace a Go interface implementation with a gRPC client stub. Replace the in-process event bus with NATS. Consumer code does not change.
- **Consistent pagination.** All list operations return the same page structure. Generic pagination helpers are straightforward to build.
