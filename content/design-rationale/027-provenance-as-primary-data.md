# 027: Provenance as Primary Data

> **"ADR-NNN" in this document refers to the `DEVELOPMENT.md` sequence, not to
> this folder's numbering.** The two are independent. See
> [Numbering](INDEX.md#numbering).
>
> The distinction matters here. ADR-016 happens to match
> `016-eventstore-design.md`, which makes the two sequences look aligned. They
> are not. ADR-021 is *Tree Persistence as Event Projection*, while this
> folder's `021` covers sponsor versus placement. ADR-003, ADR-010, and ADR-011
> diverge the same way.
>
> `DEVELOPMENT.md` is not published, so these references do not resolve for
> external readers yet. HEU-546 tracks moving the cited decisions into this
> folder.

## The Problem

Commission calculations are the source of disputes and regulatory audits. Every
earning must record why it is what it is. The volume source, the rate applied,
the rank at calculation time, the tree path walked, and every compression
decision.

ADR-021 established the pattern for Network Engine persistence. Tree mutations
are events in the EventStore. The EventStore is the single source of truth. The
adjacency table and the in-memory engine are derived projections that can be
rebuilt from events.

The question is whether commission data follows that pattern.

It looks like it should. ADR-003 commits the Network Engine to full event
sourcing. ADR-016 built one EventStore to serve both event sourcing and domain
events. The infrastructure is ready.

It does not follow the pattern. This document explains why.

## The Decision

"Commission audit trail" was one label covering four kinds of data. Each gets
its own home.

| Data | Home | Rebuildable from events |
|------|------|-------------------------|
| Commission run | Relational row | No |
| Commission results | Relational table | No |
| Calculation provenance | Dedicated table | No |
| Operational audit | `AuditWriter` | No |
| `CommissionEarned` | EventStore | Not applicable, it is the event |

**Calculation provenance is primary data. It is not a projection.** This is a
deliberate exception to ADR-021.

`CommissionEarned` stays in the EventStore, but only as a cross-context
notification. It is not the system of record.

## The Reasoning

### Retention decides it

ADR-003 defines two retention modes. Compact is the default. It purges raw
events after a 90-day window and rolls them into snapshots. Full retains
indefinitely.

Commission records are kept for dispute resolution and regulatory audit. Those
horizons run to years, not months.

Put provenance in the event stream and the default configuration deletes it.
A regulatory retention requirement then sits one config toggle away from being
violated. That is not a risk worth carrying for architectural consistency.

"Just enable Full mode" does not fix it. It makes a correctness property depend
on a setting that ships wrong. It also charges every deployment indefinite
storage for tree churn in order to keep commission data.

### Snapshots do not rescue it

Compaction rolls events into snapshots, so the obvious objection is that
nothing is really lost.

A snapshot captures current state. Provenance is historical fact. A tree
snapshot preserves who sits where today. It cannot preserve why a distributor
earned a given amount at a given level with a given set of compression skips
during a period that closed two years ago.

Compaction destroys exactly what audit needs and keeps exactly what it does
not.

### A projection that cannot be rebuilt is not a projection

This is the part worth naming.

Suppose provenance lives in a table fed by events, following ADR-021. The
events compact after 90 days. The table survives, because it is a table.

The table is now primary data wearing a projection's clothing. Nobody can
rebuild it. Nobody knows they cannot rebuild it, because the code still looks
like the tree persistence pattern that can.

Declaring provenance primary from the start costs nothing and removes that
trap.

### Why not `AuditWriter`

`AuditWriter` is the declared home for audit records in Platform. It is the
wrong shape for this.

`AuditEvent.Detail` is a `map[string]string`. A walked path is a list of node
IDs. A compression decision is a node paired with a reason. Forcing those
through a string map means encoding JSON inside strings, which is worse than
either a typed table or a JSONB column.

`AuditWriter` is not wrong. It was aimed at the wrong half. Operational audit
questions like who triggered a run and who voided one fit its actor, action,
and entity shape exactly. That half stays with `AuditWriter`.

### Why the run is a row

A commission run has mutable status. It moves from running to complete, and a
voided run gains a `superseded_by` reference to its replacement.

That is a row you update. The current status is what gets queried, not the
history of status changes. The run is low volume, one per period per plan.

Event sourcing the run would be defensible. It is not worth the indirection at
this volume for a field that exists to be read.

### `CommissionEarned` is a notification, and its grain follows from that

ADR-010 says producers announce facts and Financial records income internally.
ADR-011 puts single-context reports with their owning context and names
"commission detail by period" as Network Engine's.

So Financial needs the total per distributor. It does not need the breakdown,
and it does not own the breakdown.

The event carries one record per distributor per run. Not one per earning. A
mid-size plan paying several levels across a period's orders produces earnings
in the millions. Emitting an event per earning would flood the store and force
Financial to re-aggregate data that belongs to Network Engine.

Compaction is safe for this event. It is a delivery mechanism. The durable
records live in Financial's income schema and in the Network Engine results
store.

## Revisit Trigger

Two things would reopen this.

**Per-category retention lands with a hard guarantee.** HEU-19 covers retention
modes and is unstarted. If it adds per-category retention that keeps
`commission-*` streams while compacting `tree-*`, and that guarantee is
enforced rather than configured, provenance in the event stream becomes viable
again. Treat it as an optimization if it arrives. Do not treat it as the
mechanism that makes commission auditable.

**Measured provenance volume turns out small.** The volume reasoning here is
order-of-magnitude, not measurement. Nobody has run this at scale. If
provenance is cheap, the dedicated-table argument weakens to a preference. The
retention argument does not weaken, and it is the one carrying the weight.

## What This Means

- Calculation provenance lives in its own table. It is primary data. Do not
  document it as a projection and do not write code that assumes it can be
  rebuilt.
- Commission runs and results live in relational tables, not event streams.
- Operational audit goes through `AuditWriter`, which gains its first
  implementation and production caller from commission runs.
- `CommissionEarned` goes to the EventStore at one event per distributor per
  run.
- ADR-021's rule that projections are rebuildable from events still holds
  everywhere else. This is the documented exception, not a repeal.
- Commission data must never become retention-dependent. Any future retention
  work inherits that constraint.
