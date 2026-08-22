# 006: Enrollment Orchestration

> **Status: not built.** No enrollment saga orchestrator exists.
> `internal/identity/identity.go` is a package comment and nothing else.
> `QualifyAndPlace` and `GetPendingPlacements` are not in the codebase, and
> neither is the enrollment policy configuration. Steps 4 and 5 of the sequence
> depend on an event bus that has not been written. Read this document as the
> design to build against, not as a description of current behavior.

## The Problem

Enrollment is the most complex cross-cutting workflow in an MLM platform. It touches 5 bounded contexts in sequence:

1. **Identity** creates the user record
2. **Financial** charges the signup fee
3. **Network Engine** places the user on tree structures
4. **Commerce** sets up autoship (if required)
5. **Engagement** queues welcome communications

The legacy system handled this as a 400-line procedural PHP script that directly mutated tables across all subsystems. A failure at step 3 might leave a charged user with no tree placement. Manual intervention was sometimes necessary.

A critical business requirement complicated the design: payment failure during enrollment is not always a hard stop. Some companies require payment before enrollment completes. Others prefer to create the user in a pending state so customer service or the sponsor can collect payment later. This must be configurable.

## The Decision

### Saga Orchestrator in Identity

Enrollment uses a lightweight saga orchestrator that lives in Identity. The orchestrator knows the company's enrollment policy and coordinates the sequence. It handles rollback based on how far the process has progressed.

It lives in Identity because that is where enrollment begins. If other cross-context orchestrations emerge later, the pattern can be extracted.

### Configurable Payment Policy

The orchestrator supports two enrollment policies.

**Payment-required.** Payment must succeed before proceeding. If the charge fails, the user record is rolled back or marked as failed. Enrollment does not complete.

**Payment-deferred.** If payment fails, the user is created in a pending state. Customer service or the sponsor contacts the person to collect payment. Some companies prioritize capturing the enrollee's information over immediate payment. They would rather have a pending user they can follow up with than lose the lead entirely.

### The Enrollment Sequence

```
1. Create user record (Identity)
   Failure: stop, return error

2. Attempt payment (Financial)
   Failure (payment-required): rollback user, return error
   Failure (payment-deferred): mark user pending, continue with reduced flow

3. Place user on tree structures (Network Engine)
   Evaluates each structure independently
   Auto-places if qualified and auto-placeable
   Routes to holding tank if position choice needed
   Skips if not qualified

4. Set up autoship (Commerce)
   Fire-and-forget event. Failure does not roll back enrollment.

5. Queue welcome email (Engagement)
   Fire-and-forget event. Failure does not roll back enrollment.
```

Steps 4 and 5 are events, not synchronous calls. A user is enrolled when they have a user record, a payment (or pending status), and tree placements. Whether the welcome email sends is a downstream concern.

### Structure Placement

Placement is more nuanced than "put the user on the tree." Each structure has its own qualification requirements. A user might qualify for the unilevel immediately but not for the stairstep (requires a Gold Package purchase) or the binary (requires manual position selection).

For each structure, the outcome is one of:
- **Placed.** Qualified and auto-placeable.
- **Held.** Qualified but needs manual position selection. Goes to the per-structure holding tank.
- **Not qualified.** Does not meet placement requirements. Can qualify later.

### Post-Enrollment Qualification

Users can qualify for additional structures after enrollment. If a distributor later purchases a Gold Package that qualifies them for the stairstep, the `QualifyAndPlace` method handles this. It checks requirements against the user's current state and either places them, routes them to the holding tank, or reports what is still missing.

## Why a Saga

**Why not event choreography?** In choreography, Identity emits `UserCreated`, Financial listens and charges, Network Engine listens and places. The problem is that each context would need to know the company's enrollment policy. Should Financial charge on `UserCreated` or wait? Should Network Engine place on `PaymentSucceeded` or also on `PaymentFailed`? The enrollment policy becomes distributed business logic scattered across contexts that should not know about each other.

**Why not a dedicated Enrollment context?** Enrollment is fundamentally about creating a user. A separate context would be a thin orchestrator with no domain data of its own. One orchestration does not justify a new context. If more emerge, we can extract the pattern.

**Why fire-and-forget for autoship and welcome email?** They are not part of the enrollment contract. If the welcome email fails, the enrollment is still valid. Failures can be retried or handled by operations.

## What We Considered

**Procedural script.** A single function that does everything in sequence with try/catch. Error handling is ad hoc. No policy configurability. Directly mutates tables across contexts.

**Two-phase commit.** Distributed transactions across contexts. Does not match the deployment model and adds unnecessary complexity for what is a business workflow, not a data consistency problem.

## What This Enables

- **Single place for enrollment logic.** The entire workflow lives in one orchestrator. No tracing events across contexts to understand what happens.
- **Policy as configuration.** Adding a new enrollment policy (like a free trial with 30-day deferred payment) is a configuration change, not a code change to Financial or Network Engine.
- **Qualification gates are extensible.** New placement requirements (like completing a training module) can be added as new requirement types without changing the placement interface.
- **Holding tank UX.** Sponsors and admins manage pending placements through their dashboard. `GetPendingPlacements` returns all pending placements across all structures for a sponsor.
