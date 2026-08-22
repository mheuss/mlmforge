# 002: Context Boundaries

> **Status: design intent, partly built.** The rule holds where it has been
> implemented, but several interfaces named below do not exist.
> `CommissionAdmin` and `VolumeRecorder` were never written.
> `CommissionResult` is a struct in `networkengine`, not a read interface.
> There is no `RefundProcessed` event. Commerce declares `OrderRefunded`, and
> no context emits it, because no event bus has been built. See
> [001](001-bounded-contexts.md) for which contexts have code.

## The Problem

The legacy system let any subsystem write to any other subsystem's data. Financial changed user statuses. Commerce toggled order states. Operations modified commission records. There was no single audit point for anything. Race conditions were common and business rules were scattered everywhere.

## The Decision

Bounded contexts are not directly mutable from outside. All mutations go through the owning context's command interface. No context writes to another context's schema.

If Financial needs to change a user's status after a billing failure, it calls `identity.StatusTransition.RequestTransition()`. Identity validates the transition against its state machine, applies it, and writes the audit event.

If Operations needs to void a commission during a dispute, it emits a `RefundProcessed` event. Network Engine listens and handles the clawback internally.

If Commerce needs a user's address, it calls `identity.AddressReader.GetForUser()`. It gets a read-only copy. It cannot modify the address.

The pattern is straightforward:

- **Reads** go through read interfaces (`UserReader`, `CommissionResult`, `ProductCatalog`)
- **Writes** go through command interfaces (`StatusTransition`, `CommissionAdmin`, `VolumeRecorder`)
- **Cross-context notifications** use domain events. The provider announces what happened. Interested consumers react.

## Why This Matters

When Financial sets `user.status = 'cancelled'` directly, there is no validation that the transition is legal. There is no audit event from Identity's perspective. There is no way for Identity to enforce its state machine. The 15 lines of code you save by skipping the command interface become hundreds of lines of debugging when status gets into an impossible state.

With this rule, exactly one place validates and applies status changes: Identity's `StatusTransition`. If you want to understand the status state machine, you look in one place.

In the modular monolith, a command call is a function call. Zero network overhead. After extraction to services, it becomes a gRPC call. The correctness guarantees are worth the microseconds.

## What We Considered

**Shared database with conventions.** Let all contexts write to shared tables but establish naming conventions for who "should" write what. Conventions aren't enforced by the compiler. They erode under deadline pressure.

**Event-only communication.** All inter-context communication through events, no synchronous command interfaces. Some operations need synchronous validation. Enrollment needs to know immediately if a status transition is valid, not eventually.

**Separate databases per context from day one.** Each context gets its own PostgreSQL instance. Premature. Schema isolation within a single database achieves the same boundary enforcement without the operational complexity.

## Ownership Resolutions

Applying this principle resolved 13 ownership disputes from the legacy analysis.

| Entity | Legacy Problem | Resolution |
|--------|---------------|------------|
| `sponsor_id` | Stored on user record but is a tree relationship | Network Engine owns. Sponsor is the parent node in the enrollment tree. Removed from user entity entirely. |
| `user.status` | Mutated by Financial, Commerce, Operations | Identity owns the state machine. Others call `StatusTransition`. |
| `user.rank` | Stored by Identity, calculated by Network Engine | Network Engine owns. Identity caches a read-only copy. |
| Configuration | Single table mixing infrastructure and business rules | Split. Platform owns infrastructure config. Each domain context owns its business rules. |
| Audit logging | Scattered across subsystems | Platform provides infrastructure. Each context writes its own audit events. |
| Email delivery | Multiple contexts send directly | Engagement owns send infrastructure. Others submit via `MessageSender`. |
| Addresses | Stored by Identity, consumed by Commerce and Financial | Identity owns. Others get read-only access via `AddressReader`. |

## What This Enables

- **Single audit point.** Every entity has exactly one context that writes to it. If commission data is wrong, you look in Network Engine. If a user's status is wrong, you look in Identity.
- **State machines in one place.** Validation rules for status transitions, order state changes, commission period lifecycle all live in their owning context.
- **Clean extraction.** Contexts already communicate through interfaces. Extracting one to a service does not change how mutations work, only how the call is transported.
