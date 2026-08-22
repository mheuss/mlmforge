# Development

## Architectural Decision Records

Significant technical decisions with context and rationale.

### ADR-001: Go + Rust Dual-Language Architecture

**Status:** Accepted

**Context:** MLMForge needs to be both a high-performance commission calculation engine and a full-featured web application platform. The commission engine must walk trees of millions of nodes with predictable performance. The web layer needs high concurrency for API serving.

**Decision:** Use Rust for the Network Engine (commission calculations, tree walking, volume aggregation, bonus computation) and Go for everything else (HTTP API, business logic, all other bounded contexts). Rust engine runs as a subprocess with NDJSON over stdin/stdout (StdioTransport). A gRPC transport can replace this for distributed deployment.

**Consequences:**
- Two languages means two build systems, two test suites, two sets of conventions
- Smaller contributor pool for Rust components
- Exceptional performance for the critical path (commission calculations)
- Go's single-binary deployment enables simple "out of the box" experience
- Clear architectural boundary between the engine and the application layer

### ADR-002: Modular Monolith with Clean Extraction

**Status:** Accepted

**Context:** MLMForge must serve both startups (single binary, minimal ops) and enterprises (independently scaled services). Building microservices from day one adds operational complexity that kills the "works out of the box" goal.

**Decision:** Build as a modular monolith with 8 bounded contexts communicating through well-defined in-process interfaces. Each context owns its own PostgreSQL schema. Contexts can be extracted to independent services by swapping in-process calls for gRPC/NATS without code changes.

**Consequences:**
- Simple deployment for small companies (single binary + single Postgres)
- Requires discipline to maintain context boundaries (no cross-schema joins)
- Extraction to microservices is a deployment decision, not a rewrite
- Schema-per-context adds some complexity to database management
- Interface design is critical. Get it wrong and extraction becomes painful

### ADR-003: Event Sourcing for Commission Engine

**Status:** Accepted

**Context:** Commission calculations in MLM are the source of disputes, regulatory audits, and the most complex business logic. No existing platform provides adequate audit trails or the ability to simulate plan changes.

**Decision:** The Network Engine is fully event-sourced. All commission-relevant activity is stored as immutable events. Current state is derived by replaying events. Two retention modes: Compact (default, 90-day window then snapshot) and Full (indefinite, enables simulation).

**Consequences:**
- Complete audit trail for every commission dollar calculated
- Enables "what-if" compensation plan simulation (killer feature)
- Historical recalculation by replaying events
- More storage required (mitigated by compact mode)
- Steeper learning curve for contributors unfamiliar with event sourcing
- All contexts emit domain events to the event bus (not just the engine)

### ADR-004: Three-Tier Extensibility Model

**Status:** Accepted

**Context:** MLMForge must serve companies with radically different customization needs, from "just configure a plan" to "we need custom business logic in the commission engine."

**Decision:** Three extension tiers ordered by proximity to the core:
1. **Configuration (YAML/JSON).** 90% of companies. Comp plans, ranks, bonuses as data.
2. **WASM plugins.** Hot path extensions inside the Rust engine. Near-native speed, sandboxed.
3. **Webhooks + NATS event bus.** Warm path (sync HTTP) and cold path (async events) for external integrations.

**Consequences:**
- Configuration-first means most companies never write code
- WASM adds complexity but provides safe, fast in-process extensibility
- Webhooks are universally understood and language-agnostic
- NATS event bus provides async integration and observability for free
- Plugin SDK must be well-documented and easy to use

### ADR-005: PostgreSQL as Primary Datastore

**Status:** Accepted

**Context:** Need a database that handles event store, relational data, JSON payloads, and scales from a single instance to distributed deployments.

**Decision:** PostgreSQL for all contexts. Event store implemented as partitioned tables with JSONB payloads. Enterprise deployments can swap in EventStoreDB or Kafka for the event store via the `EventStore` interface.

**Consequences:**
- Single database technology simplifies operations for small deployments
- PostgreSQL handles event store patterns well (append-only, partitioned, JSONB)
- Enterprise has a clean upgrade path to specialized event stores
- Schema-per-context within a single Postgres instance is straightforward
- Avoids polyglot persistence complexity in early stages

### ADR-006: Context Boundary Immutability

**Status:** Accepted

**Context:** The legacy system has multiple contexts directly mutating each other's data. Financial sets `user.status`, Commerce creates orders on behalf of billing, Operations modifies order state. This creates hidden coupling, conflicting mutations, and no single audit point for state changes.

**Decision:** Bounded contexts are not directly mutable from outside. All mutations go through the owning context's command interface. If Financial needs to change a user's status, it calls Identity's `StatusTransition` command. Identity validates and applies the change. No context writes to another context's schema.

**Consequences:**
- Every state change has a single audit point in the owning context
- Conflicting mutations are impossible. The owner validates and serializes
- Slightly more ceremony for cross-context operations (command call vs. direct write)
- State machines and validation rules live in exactly one place
- Cleaner extraction to services later. Already communicating via interfaces

### ADR-007: Enrollment Orchestration Pattern

**Status:** Accepted

**Context:** Enrollment touches 5 bounded contexts (Identity, Financial, Network Engine, Commerce, Engagement) in a specific sequence. The legacy system handles this as a 400-line procedural script. Payment failure behavior is not universal. Some companies require payment to succeed before enrollment completes. Others prefer to create the user in a pending state and have CS or the sponsor collect payment later.

**Decision:** Lightweight saga orchestrator living in Identity. The orchestrator knows the company's enrollment policy (payment-required vs. payment-deferred), coordinates the sequence (create user → attempt payment → branch on policy → place on tree → set up autoship), and handles rollback based on progress. Non-critical steps (welcome email, autoship) are fire-and-forget events from the orchestrator. Event choreography rejected because contexts would need to know each other's enrollment policies, creating distributed business logic.

**Consequences:**
- Single place to understand the enrollment workflow
- Configurable payment-failure behavior without changing any downstream context
- Rollback logic is centralized, not distributed
- Identity takes on coordination responsibility beyond just user CRUD
- If similar cross-context orchestrations emerge (e.g., user removal), the pattern can be extracted into a shared orchestration library

### ADR-008: Event Bus Strategy

**Status:** Accepted

**Context:** The system needs domain events for cross-context communication (income recording, rank changes, order completion, etc.). NATS is the target for distributed deployments, but the modular monolith starts as a single binary.

**Decision:** In-process event bus first, extract to NATS when decomposing. Define event interfaces as Go interfaces now. The in-process implementation is function calls behind those interfaces. When a context is extracted to a service, swap in NATS without changing producer or consumer code. This directly follows ADR-002 (modular monolith with clean extraction).

**Consequences:**
- No infrastructure dependency (NATS) required for development or small deployments
- Event interfaces are designed for async semantics from day one (at-least-once, idempotent handlers)
- In-process implementation is simpler to debug and test
- Extraction to NATS is a configuration change, not a code change
- Must resist the temptation to rely on in-process synchronous guarantees that won't exist after extraction

### ADR-009: Sponsor as Tree Relationship

**Status:** Accepted

**Context:** The legacy system stores `sponsor_id` on the user record, but sponsor is fundamentally a tree relationship. It is the parent node in the enrollment/unilevel tree. Sponsors can change (compression when a sponsor drops out). Every tree type (binary, unilevel, matrix, stairstep, streamline) has a parent/upline concept. Storing sponsor on the user record creates a denormalized copy that drifts when tree operations occur.

**Decision:** Remove `sponsor_id` from the user entity entirely. Network Engine owns all tree relationships, including the enrollment tree where "sponsor" equals "parent node." Contexts that need sponsor information query Network Engine's `TreeNavigator` interface. If performance becomes an issue after microservice extraction, add a cached projection then.

**Consequences:**
- Identity knows who you are. Network Engine knows where you sit
- Sponsor compression is a single-context operation (Network Engine only)
- No denormalization drift between Identity and Network Engine
- Contexts that previously joined on `users.sponsor_id` now call `TreeNavigator`
- In the modular monolith, this is a function call. No performance concern
- Clean separation of identity data from network topology data

### ADR-010: Domain Events for Cross-Context Data Flow

**Status:** Accepted

**Context:** Multiple contexts need to record income in Financial (Network Engine for commissions, Commerce for store sales, Engagement for co-op/events). A command-style interface (`Financial.RecordIncome(...)`) creates outbound dependencies from each producer to Financial. The same pattern applies to other cross-context data flows.

**Decision:** Producers emit domain events describing what happened (`CommissionEarned`, `OrderCompleted`, `CoopSharePurchased`). Financial listens and records income internally. This pattern applies generally: contexts announce facts about their domain, interested consumers react. Requires at-least-once delivery with idempotent handlers, dead letter queue for failed processing, alerting, and manual reconciliation tooling in Operations.

**Consequences:**
- Producers are decoupled from Financial's income schema
- Financial decides how to categorize, aggregate, and store income
- Events themselves serve as an audit trail
- Natural fit with event sourcing (ADR-003)
- Eventually consistent. Producers don't get synchronous confirmation of recording
- Dead letter queue and reconciliation tooling are required infrastructure

### ADR-011: Reporting Ownership

**Status:** Accepted

**Context:** The legacy system has 28+ report types spanning all contexts, all living in a single reporting module. Some reports query a single context's data. Others join data across multiple contexts.

**Decision:** Hybrid ownership. Single-context reports live with their owning context (e.g., "commission detail by period" in Network Engine, "product sales by category" in Commerce). Cross-cutting reports that join data from 2+ contexts live in Operations (e.g., "distributor performance" spanning commissions + orders + rank). Each context exposes query interfaces that Operations can call for cross-cutting reports.

**Consequences:**
- Domain experts own their reports and can evolve them independently
- Cross-cutting reports have a clear home (Operations)
- Operations depends on query interfaces from other contexts
- Report ownership is determined by a simple rule: how many contexts does it query?
- Avoids a monolithic reporting module that knows about every context's internals

### ADR-012: Compensation Plan Configuration Storage

**Status:** Accepted

**Context:** Compensation plan configuration is the primary customization surface for MLMForge. Plans define commission structures, rank hierarchies, bonus programs, qualification rules, and dozens of plan-type-specific options. This configuration surface will grow significantly as new plan types, bonus types, and options are added. The design principles commit to "configuration over code" where 90% of companies customize through data, not programming.

**Decision:** Three-part design.

1. **Hybrid database storage.** Relational tables for the structural skeleton (plans, structures, ranks, bonuses). JSONB columns for detailed configuration within each entity. The relational shell provides queryability and referential integrity for high-level relationships. JSONB absorbs growth in plan-type-specific options without schema migrations.

2. **Typed Rust structs as the engine contract.** The Network Engine receives a fully deserialized, strongly typed `CompensationPlan` struct. The engine never parses YAML, reads from the database, or handles raw JSON. It takes typed config plus events and produces commission results.

3. **Go application layer owns the config pipeline.** YAML file (or admin UI input) → validate against schema → store in PostgreSQL → load from PostgreSQL → deserialize to Rust struct → pass across the subprocess boundary (StdioTransport). Validation, versioning, and persistence are Go responsibilities. The engine is a pure function of config + events.

**Database schema shape:**

| Table | Relational columns | JSONB |
|-------|-------------------|-------|
| `compensation_plans` | id, name, version, status, created_at, activated_at, archived_at | none |
| `plan_structures` | id, plan_id, structure_type | config (commission rates, compression, depth, volume settings) |
| `plan_ranks` | id, plan_id, name, ordinal | qualification (volume thresholds, leg requirements, grace periods) |
| `plan_bonuses` | id, plan_id, structure_id, bonus_type | config (rates, depth, windows, eligibility rules) |

**Version lifecycle:** Draft → Active → Archived. Only one version active per plan. Multiple drafts can coexist. Activating a new version archives the current one.

**Immutability rules:**
- Draft versions are freely editable.
- Active and archived versions are immutable. No exceptions.
- To modify an active plan, clone it to a new draft, edit the draft, then activate it.
- Every commission run records the plan version it used. Replay always uses the pinned version.

**Period binding:** New version activation takes effect at the next period boundary by default. An explicit `activate_immediately` override exists for corrections, requires admin confirmation, and leaves an audit trail.

**Consequences:**
- Adding new config options requires updating the Rust struct and YAML schema, not a database migration
- Relational shell enables queries like "which plans use binary structures?" without parsing JSONB
- Plan versioning is a new row with a bumped version number. JSONB configs are immutable snapshots.
- Active-means-immutable eliminates ambiguity. No edge cases around "was this version used yet?"
- Clone-to-draft workflow is simple and auditable
- WASM plugin references fit naturally as fields within the JSONB config
- The engine stays pure and testable. Pass in a struct, get commission results out.
- Admin UI forms map to the relational entities. Detail editing works against JSONB within each entity.
- Draft versions can be simulated against historical or synthetic data before activation
- Trade-off: some config lives in JSONB without relational constraints. Validation must happen in the Go layer before storage.

### ADR-013: Commission Run Integrity and Mid-Period Plan Changes

**Status:** Accepted

**Context:** Commission runs produce financial records that may be paid to distributors. Plan version changes can happen mid-period via the `activate_immediately` override (ADR-012). The system must handle the interaction between immutable commission records, plan version changes, and payout state without losing audit trail or creating inconsistent financial data.

**Decision:** Commission runs and their records are immutable. Mid-period plan changes are handled differently based on payout state.

**Commission run data model:**
- Every commission run records the period, plan version, and status.
- Every commission record traces back to the run that produced it via `run_id`.
- Runs have a `superseded_by` field linking a voided run to its replacement.

**Mid-period change handling:**

1. **No run has executed this period.** No conflict. Next run uses the new version.
2. **Run completed, commissions unpaid.** Old run is voided (not deleted). New run executes against the same period with the new plan version. Payout process only sees the new results. The period lag (`payout_lag_days`) provides a natural buffer for this scenario.
3. **Run completed, commissions paid.** Old run remains as historical fact. New run executes and produces adjustment records. Adjustments are the per-distributor delta between old and new results. Positive adjustments are added to the next payout cycle. Negative adjustments are deducted from future payouts.

**Adjustment records are first-class entities.** They reference both the original run and the replacement run. They are not manual overrides or hacks. The payout process checks for pending adjustments before disbursing.

**Consequences:**
- Complete audit trail. Every commission traces to a run, every run traces to a plan version.
- Voided runs are archived, never deleted. Full history is preserved.
- Adjustment records make mid-period changes transparent and auditable.
- Negative adjustments (clawbacks) are operationally sensitive. Admin UI must surface clear warnings before triggering scenario 3.
- The engine remains pure. It runs config + events → results. Voiding, re-running, and delta calculation are Go application layer responsibilities.
- Period lag serves as a natural safety net. Most mid-period corrections land in scenario 2, not scenario 3.

### ADR-014: Streamline Compensation Configuration

**Status:** Accepted

Full document: [`content/design-rationale/014-streamline-compensation-config.md`](content/design-rationale/014-streamline-compensation-config.md)

**Summary:** Streamline uses linear chains (streams) instead of trees. Each stream is a separate arena with width=1. Dynamic compression is the defining mechanic: each level has its own minimum rank requirement. Distributors can hold positions on multiple streams. Monoline is a streamline variant with a single stream.

### ADR-015: Compensation Plan Schema and Wire Format

**Status:** Accepted

Full document: [`content/design-rationale/015-compensation-plan-schema-and-wire-format.md`](content/design-rationale/015-compensation-plan-schema-and-wire-format.md)

**Summary:** YAML field names are the canonical wire format. Rust uses `#[serde(rename)]` to match. Go passes names through with zero translation. JSON Schema (Draft 2020-12) validates structure. Go validates business rules. Five structural differences (structure tagging, donated placement, streamline levels, binary mode tagging, binary placement key rename) require Go translation.

### ADR-016: EventStore Design

**Status:** Accepted

Full document: [`content/design-rationale/016-eventstore-design.md`](content/design-rationale/016-eventstore-design.md)

**Summary:** A single EventStore in Platform handles both event sourcing (Network Engine) and domain events (all contexts). JSON envelope with `json.RawMessage` payload. Streams named `{category}-{id}` for efficient cross-stream queries. Optimistic concurrency via expected version parameter. PostgreSQL implementation uses pgx v5 with JSONB. In-memory implementation for testing.

### ADR-017: Commission Calculation Architecture

**Status:** Accepted

Full document: [`content/design-rationale/017-commission-calculation-architecture.md`](content/design-rationale/017-commission-calculation-architecture.md)

**Summary:** Seven architectural decisions for all commission calculators. Snapshots carry facts, calculators apply rules. Flat `Vec<CommissionEarning>` output with no pre-grouping. Two-phase prep+walk pattern. Matrix reuses the unilevel walk (only difference is effective depth ceiling). No shared trait until three concrete implementations exist. Compression is part of the walk, not post-processing. Strict on source data, defensive on missing upline nodes.

### ADR-018: Config Pipeline

**Status:** Accepted

Full document: [`content/design-rationale/018-config-pipeline.md`](content/design-rationale/018-config-pipeline.md)

**Summary:** The Go validation pipeline runs five stages in order: JSON Schema validation, YAML unmarshal, commission resolution (two-pass parsing by structure type), business-rule validation (with error/warning severity), and structural translation to Rust-compatible JSON. A `Commission` marker interface provides compile-time safety for the six commission types.

### ADR-019: NDJSON Protocol

**Status:** Accepted

Full document: [`content/design-rationale/019-ndjson-protocol.md`](content/design-rationale/019-ndjson-protocol.md)

**Summary:** The Go/Rust subprocess protocol uses NDJSON (one JSON object per line) over stdin/stdout. Requests carry an `id` and `op` field. Responses carry the same `id` with an `ok` boolean. A fixed error code taxonomy (`STRUCTURE_NOT_FOUND`, `USER_NOT_FOUND`, `INVALID_PARAMS`, etc.) enables typed error handling on the Go side. The Rust worker recovers from panics via `catch_unwind`. Go supports context cancellation for blocked reads. JSON object key order is deterministic and build-independent but is explicitly not part of the contract; Go decodes by struct tag and must not depend on it (HEU-648).

### ADR-020: Tree Topology Separation

**Status:** Accepted

Full document: [`content/design-rationale/020-tree-topology-separation.md`](content/design-rationale/020-tree-topology-separation.md)

**Summary:** Tree structures are pure topology. They enforce shape constraints and provide traversals but contain no placement logic, holding tank knowledge, or business rules. The Go platform layer decides where to place a node. The tree validates the position and wires it up. Sponsor edges are topological data stored alongside placement edges for cache-friendly traversal during commission calculations.

### ADR-021: Tree Persistence as Event Projection

**Status:** Accepted

**Context:** Tree topology lives in the Rust engine's in-memory arena at runtime. The engine is ephemeral. Without persistence, tree state is lost on restart and invisible to reporting tools.

**Decision:** Tree mutations are events appended to the EventStore (extends ADR-003). A synchronous in-process consumer projects each event into two targets: a PostgreSQL adjacency table (the read model for reporting, admin tools, and startup bulk-load) and the Rust engine (the runtime authority). The EventStore is the single source of truth. The adjacency table and engine are derived projections that can be rebuilt from events.

**Consequences:**
- The EventStore owns durability. The adjacency table and engine are rebuildable.
- Store projection happens before engine projection. If the engine fails, the table is consistent and the event exists for replay.
- On startup, the engine is rebuilt from the adjacency table via depth-ordered bulk load, not by replaying the full event stream.
- The synchronous consumer is the initial implementation. If projection latency becomes a concern, the consumer can be made asynchronous without changing the event schema.

### ADR-022: Migration Framework

**Status:** Accepted

**Context:** Database schema was managed inline via `CreateSchema()` methods that executed raw DDL. This approach has no version tracking, no rollback capability, and no way to coordinate schema changes across deployments.

**Decision:** Use golang-migrate with versioned SQL files in `migrations/`. A CLI command (`./mlmforge migrate up/down/version`) applies or rolls back migrations. In production, migrations run as a Kubernetes Job before application pods start. Schema changes follow the backward-compatible expand-and-contract pattern: add new columns/tables first (expand), deploy code that uses them, then remove old columns/tables (contract) in a later migration.

**Consequences:**
- All DDL lives in versioned, reviewable SQL files. No inline schema management.
- Rollback is explicit via `migrate down`. Each migration must have a working down file.
- The expand-and-contract pattern means zero-downtime deployments but requires two migrations for breaking schema changes.
- Replaces the `CreateSchema()` approach. Existing inline DDL was moved to migration 000001.

### ADR-023: Soft Delete for Tree Topology

**Status:** Accepted

**Context:** When a distributor is removed from a tree, the placement history has reporting and audit value. Hard deletes would destroy this history.

**Decision:** Use a `removed_at` timestamp column instead of hard deletes. Active-node queries filter on `removed_at IS NULL`. A partial unique index on `(tree_id, user_id) WHERE removed_at IS NULL` enforces uniqueness for active nodes while allowing historical duplicates (a user removed and later re-placed).

**Consequences:**
- Placement history is preserved for reporting and compliance audits.
- Active-node queries use the partial index for efficient lookups without scanning removed rows.
- Historical queries can include removed nodes by omitting the `removed_at IS NULL` filter.
- Table size grows over time. If this becomes a concern, archival of old removed rows is a future optimization.

### ADR-024: Rank Evaluation Architecture

**Status:** Accepted

**Context:** The Rust commission engine takes `DistributorSnapshot { rank: String, ... }` as input but no code in the repo computes that rank. The Rust config has a fully formed `RankDefinition` shape, but no evaluator reads it. Three rank-epic sibling tickets (HEU-444 distributor count predicates, HEU-445 history persistence, HEU-446 windowed/tenure) cannot ship until per-period rank evaluation exists. The architectural question is where evaluation lives, given that tree walks (for GV, leg volumes, distributor counts) live in the Rust engine and Go has no in-process tree access.

**Decision:** Rank evaluation lives in the Rust engine as a top-level `src/rank/` module, peer to `commission/` and `tree/`. It is exposed as a standalone NDJSON worker op `evaluate_ranks`, matching the convention every other engine operation already follows. The evaluator iterates rank evaluation to a least fixpoint (see design-rationale 026) so a distributor's rank evaluation can rely on already-computed descendant ranks for `DistributorCountRequirement.min_rank`. Inputs are a new `EvaluationInputs` type carrying per-distributor primitives plus volume sources; the plan and trees come from `WorkerState`. Output is `HashMap<UserId, EvaluatedRank>` where `EvaluatedRank` is either `Qualified { rank, ordinal }` or `Unranked`. The existing `DistributorSnapshot.rank: String` field stays as an opt-in override path for what-if simulation.

**Consequences:**
- Tree-aware predicates run in-process where the trees live. No N round-trips per distributor over NDJSON.
- The engine becomes self-sufficient. Callers no longer need to source rank from "somewhere" before running commission.
- Rank evaluation is a peer concern to commission, not a sub-concern. The module layout reflects that.
- Rank evaluation iterates to a least fixpoint (HEU-460), not a single pass. Passes repeat over an accumulating descendant-rank map until no rank changes, bounded by a pass-count guard that returns `RankEvaluationDidNotConverge`. Predicates that read descendant ranks must stay monotone so the loop converges. Predicates that depend on ancestors would widen the model and require rethinking.
- Aspirational rank types in `internal/networkengine/types.go` (`RankGroup`, `RankDescriptor`, `QualificationRule`, `QualificationStatus`, `RuleProgress`, `RankEvent`, `Rank`) are deleted. The Go side uses thin wire DTOs over the Rust types, matching the established pattern for tree, commission, and snapshot ops.
- Streamline and board plan structures are excluded from rank evaluation in HEU-443. They do not expose `TreeNavigator` and rank semantics for those structures differ enough that bolting on evaluation now would be premature.
- Backward compat for callers that already supply `DistributorSnapshot.rank` directly is preserved. No commission flow is forced to use the new op.

---

## Context-Specific Development Guides

For implementation patterns specific to a bounded context, see the relevant guide:

- **Network Engine:** [`docs/development/network-engine.md`](docs/development/network-engine.md). Arena storage, traversal patterns, testing conventions for Rust tree types

---

## Technical Notes

### Patterns

Common patterns used in this codebase.

- **Bounded Context pattern:** Each of the 8 contexts has its own package, schema, and public interface. No direct access to another context's internals.
- **Context immutability:** Contexts are not directly mutable from outside. All mutations go through the owning context's command interface (ADR-006).
- **EventStore interface:** Abstract interface for event persistence. Unified store for event sourcing (Network Engine) and domain events (all contexts). JSON envelope with opaque payload, category-ID stream naming, optimistic concurrency. Default implementation is PostgreSQL with pgx v5. Enterprise can swap in EventStoreDB or Kafka (ADR-016).
- **Domain events for data flow:** Contexts announce facts about their domain via events. Interested consumers react. Producers don't depend on consumers (ADR-010).
- **Configuration-as-data:** Compensation plans, rank definitions, and bonus rules are expressed as YAML/JSON data structures, not code.
- **Configuration ownership:** Infrastructure config (SMTP, encryption, sessions) lives in Platform. Domain-specific business rules (autoship_notify_days, rank_check_promotion_only, minimum_commission_check) live in their owning context.
- **Interface-driven design:** All cross-context communication goes through Go interfaces, enabling both in-process and remote implementations.
- **Saga orchestration:** Complex cross-context workflows use a lightweight saga pattern. The orchestrator lives in the context that initiates the workflow (ADR-007).
- **Batch migration path:** Legacy cron bots → Phase 1: scheduled jobs within owning context → Phase 2: event-driven triggers where possible → Phase 3: multi-context bots become orchestrated workflows.

### Bounded Contexts

| Context | Language | Schema | Responsibility |
|---------|----------|--------|---------------|
| Network Engine | Rust | `network_engine` | Tree structures, commissions, ranks, bonuses |
| Commerce | Go | `commerce` | E-commerce, autoship, coupons, fulfillment |
| Financial | Go | `financial` | Payments, billing, wallet, accounting |
| Identity & Access | Go | `identity` | Users, enrollment, RBAC |
| Engagement | Go | `engagement` | Communications, events, CRM, media |
| Operations | Go | `operations` | Reporting, CS, CMS, config, i18n |
| Portals | Go | `portals` | Admin UI, distributor back office (thin API consumers) |
| Platform | Go | `platform` | Runtime, jobs, config, event persistence, encryption, sessions, audit |

### Gotchas

Non-obvious behaviors, workarounds, known quirks.

- The Rust subprocess boundary uses NDJSON over stdin/stdout. All requests and responses are serialized as JSON. Use the StdioTransport wrapper for all engine communication.
- Event store compact mode purges raw events after the retention window. If you need historical data, ensure full mode is enabled before the window closes.
- Commission calculations use f64 (IEEE 754 double precision) for all monetary values in the Rust engine. Final rounding to cents occurs in the Go application layer before payout. This is a deliberate trade-off: f64 arithmetic is fast and sufficient for commission calculations where sub-cent precision is not required. If precision issues surface in production, the engine can adopt a fixed-point decimal type without changing the wire protocol (the JSON format already uses numbers, not strings).

### External Integrations

Notes on third-party services, APIs, dependencies.

- **NATS.** Event bus for async communication between contexts and external systems
- **Payment gateways.** Abstracted behind a payment broker interface (specific gateways TBD)
- **Shipping carriers.** Abstracted behind a shipping provider interface
- **Tax calculation.** Will integrate with services like Avalara/TaxJar
