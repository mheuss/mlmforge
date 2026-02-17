# Bugs and Todos

## Active

Items committed to the current sprint/cycle.

(none)

## Backlog

Acknowledged items not yet scheduled.

- [ ] [chore] Consider unifying `BreakawayConfig.override_calculation` + `differential` into a single enum (`OverrideConfig::Differential(DifferentialConfig) | OverrideConfig::FixedOverride`). Current approach uses an enum + optional field with doc comments stating the dependency. Go owns validation. Revisit when stairstep engine is implemented. (code review finding S2)
- [ ] [chore] Consider unifying `InfinityBonusConfig.rate_mode` + `flat_rate` + `decreasing_rates` into a single enum (`InfinityRate::Flat(f64) | InfinityRate::Decreasing(BTreeMap<u8, f64>)`). Same pattern as BreakawayConfig coupling. Revisit when bonus engine is implemented. (code review finding S3)
- [ ] [design] Define `FixedOverride` configuration for stairstep breakaway. The `OverrideCalculation` enum has a `FixedOverride` variant but the design doc never specifies what fields it carries. The `differential` block is fully specced but `fixed_override` was listed as an option without a definition. Resolve when stairstep engine is implemented: either define the structure or remove the variant. (code review finding S4)
- [ ] [feature] Implement sponsor/placement split in tree Node struct — sponsor_id separate from tree parent, two traversal paths for commission walk vs sponsor bonus walk (identified during unilevel review, see unilevel.md donated placement annotation)
- [ ] [feature] Implement Binary tree structure in Rust
- [ ] [feature] Implement Matrix tree structure in Rust
- [ ] [feature] Implement Stairstep tree structure in Rust
- [ ] [feature] Implement Streamline tree structure in Rust
- [ ] [feature] Build HTTP API layer (technology TBD — REST, GraphQL, or hybrid)
- [ ] [feature] Build admin portal (React/Next.js)
- [ ] [feature] Build distributor back office portal
- [ ] [feature] Implement WASM plugin SDK
- [ ] [feature] Implement webhook extension points
- [ ] [feature] Set up NATS event bus integration
- [ ] [feature] Compensation plan simulator CLI
- [ ] [chore] Add Debug impl to UnilevelTree (and future tree types). `#[derive(Debug)]` works but dumps the entire arena, which is unusable at scale. Recommended: custom `Debug` impl that prints node count and root user ID. Defer until the second tree type is implemented, then add a consistent Debug pattern across all tree structs.
- [ ] [chore] Extract shared test helpers (`test_uuid`, `test_uuid_u16`) into a common `#[cfg(test)]` module when the second tree type is implemented
- [ ] [chore] Define `TreeNavigator` trait when the second tree type is implemented, covering shared operations (`get_parent`, `get_children`, `get_upline`, `get_downline`, `get_position`, `is_descendant_of`)
- [ ] [chore] Extract shared arena logic into `tree/arena.rs` when the second tree type is implemented (see `docs/development/network-engine.md`)
- [ ] [feature] Admin UI: draft compensation plan list with one-click historical data simulation launch
- [ ] [feature] Synthetic data generator for compensation plan simulation — generate X users with Y signups/period, Z autoships, A orders over B periods. Must not write to production database. Explore AI integration: natural language description of desired data → LLM parses into generator config.
- [ ] [feature] Commission run superseded_by field and void-and-rerun workflow for mid-period plan changes (ADR-013)
- [ ] [feature] Adjustment records as first-class entities — per-distributor delta between voided and replacement commission runs (ADR-013)
- [ ] [feature] Payout process must check for pending adjustments before disbursing (ADR-013)
- [ ] [feature] Admin UI warning when activating a plan mid-period with already-paid commissions (ADR-013 scenario 3)
- [ ] [feature] Implement EventStore retention modes (compact vs full) and table partitioning by commission period. Compact mode purges raw events after a configurable window, rolling them into snapshots. Full mode retains all events indefinitely. Deferred from EventStore design (2026-02-16).
- [ ] [chore] EventStore: validate that Append rejects empty events slice (zero-length append). Both implementations currently accept it silently, which may mask bugs in callers. (code review finding S4, 2026-02-16)
- [ ] [chore] EventStore: validate NewEvent fields on Append (non-empty ID, Type, Payload). Currently no validation — callers must ensure correctness. Consider whether the store or the caller should own this. (code review finding S5, 2026-02-16)
- [ ] [chore] EventStore: add t.Parallel() to independent memory tests for faster execution. All tests use isolated store instances, so parallelism is safe. (code review finding S6, 2026-02-16)
- [ ] [chore] Adapt sync-orchestration command for Go/Rust service patterns
- [ ] [feature] Define CommissionEarned domain event (referenced in Financial event consumption but not yet defined)
- [ ] [feature] Revisit SignupProduct.RecurringFee for complex renewal pricing (monthly vs. annual, tiered)
- [ ] [feature] Implement enrollment saga orchestrator in Identity (decision 006)
- [ ] [feature] Implement per-structure holding tanks for deferred placement (decision 003)
- [ ] [feature] Implement rank groups with multiple evaluation modes (decision 003)
- [ ] [refactor] Investigate unifying `UnilevelCommission`, `MatrixCommission`, and `StairstepCommission` commission types — all three share identical fields (`BroadCommissionPercent`, `VolumeToDollarMultiplier`, `CommissionableDepth`, `RateTable`, `Compression`). Stairstep adds `Breakaway`. Consider extracting a shared `LevelCommissionConfig` struct. Meets the three-case threshold for abstraction. (code review finding M-3)

## Resolved

Completed items awaiting migration to VERSION_HISTORY.md at next release.

- [x] [docs] MLM industry research (fundamentals, comp plans, glossary, competitive landscape, legal)
- [x] [docs] Design principles document
- [x] [chore] Workflow setup (CLAUDE.md, commands, supporting docs)
- [x] [feature] Set up Go module structure with bounded context directories
- [x] [feature] Set up Rust workspace for Network Engine
- [x] [chore] Configure CI pipeline (Go test, Rust test, linting, formatting)
- [x] [docs] Domain discovery — 8 bounded context analyses from the legacy system
- [x] [docs] Cross-domain synthesis — dependency graph, shared concerns, ownership disputes, interface candidates
- [x] [decision] AD-1: Enrollment orchestration → lightweight saga in Identity (ADR-007)
- [x] [decision] AD-2: Event bus → in-process first, extract to NATS (ADR-008)
- [x] [decision] AD-3: Reporting → hybrid, single-context owns its own, cross-cutting in Operations (ADR-011)
- [x] [decision] AD-4: Configuration → split by ownership, infra in Platform, business rules in owning context
- [x] [decision] AD-5: Batch processing → three-phase migration (jobs → events → orchestration)
- [x] [decision] 13 ownership disputes resolved (sponsor_id → Network Engine, context immutability principle, income via events, etc.)
- [x] [docs] ADRs 006–011 written in DEVELOPMENT.md
- [x] [feature] Define core interfaces between bounded contexts (22 interfaces, 70+ types across 7 provider contexts)
- [x] [docs] Architecture decision records — 6 documents in `decisions/`, voice matched to project style, README rewritten, legacy system name removed
- [x] [feature] Implement basic Unilevel tree structure in Rust (arena storage, 11 operations, 44 unit tests, 6 property-based tests, decision 007)
- [x] [docs] Compensation plan configuration design — brainstorm session resolving all 9 open questions, full design document with plain-English narrative + YAML schema + Rust types for all 6 plan types, 10 config areas
- [x] [docs] Compensation plan decision files (008-014) — per-structure configurable options with plain-English explanations for common, unilevel, binary, matrix (+ board), stairstep, generation, streamline (+ monoline)
- [x] [feature] Compensation plan configuration types in Rust — 85 public types across 14 files in `config/` module, 141 unit tests with JSON deserialization round-trips. Self-documenting: every type and field has a doc comment explaining business meaning. (`engine/network-engine/src/config/`)
- [x] [feature] Compensation plan configuration pipeline — JSON Schema (Draft 2020-12, monolithic), Rust serde renames (~22 fields), Go validation pipeline (5-step: schema → unmarshal → commission resolution → business rules → translation). ADR-015 documents the schema/wire-format decisions. 75 Go tests, 95% coverage.
- [x] [feature] Implement EventStore interface and PostgreSQL implementation — `EventStore` interface, `Event`/`NewEvent`/`ConcurrencyError` types, `PostgresEventStore` (pgx v5), `MemoryEventStore` (testing). 20 unit tests + 7 integration tests.
- [x] [feature] Implement unilevel commission calculator in Rust — two-phase algorithm (prep + walk), compression (SkipInactive, SkipBelowRank), active leg tier depth limits, rate table lookup. ADR-017 written with 6 architectural decisions. 185 tests (177 unit + 8 property-based).
- [x] [chore] Upgrade workflow files to latest WORKFLOW_TEMPLATE.md — reviewed template, project was 95% current. Added `review-project` command (the only new item). No other changes needed.

## Session Notes

### Session — 2026-02-12

**Stopped after:** Completed the compensation plan configuration brainstorm and design document. All 9 open QUESTION annotations resolved. Design document written to `docs/plans/2026-02-12-compensation-plan-config-design.md`. Decision files 008-014 created and committed.

**Next up:** Pre-implementation for the compensation plan configuration types. The design document drives the implementation plan. Key deliverables: YAML schema definition, Rust config structs in the network engine, Go validation pipeline. This is the last design step before commission engine implementation begins.

**Open questions:** None. All brainstorm questions resolved.

### Session — 2026-02-13

**Stopped after:** Implemented all 14 tasks of the compensation plan configuration types plan. 85 public types across 14 files, 141 unit tests. Code reviewed (4 suggestions: 1 won't-fix, 3 deferred to BUGS_AND_TODOS.md). Pre-commit complete. Merged to main via fast-forward. Worktree and feature branch cleaned up.

**Next up:** YAML schema definition and Go validation pipeline for compensation plan configuration. This completes the config pipeline (ADR-012). After that, commission engine implementation begins.

**Open questions:** None. Three deferred code review findings (S2, S3, S4) logged in Backlog for future engine implementation.

### Session — 2026-02-14

**Stopped after:** Completed the YAML schema design brainstorm. Design document written to `docs/plans/2026-02-14-yaml-schema-design.md` (local, untracked). Key decisions: JSON Schema Draft 2020-12, monolithic file at `schemas/compensation-plan.schema.json`, design-doc YAML names as canonical wire format with Rust `serde(rename)` alignment, schema validates structure while Go validates business rules. Identified ~22 Rust serde renames and 3 structural Go translations needed.

**Next up:** Implementation planning for the YAML schema task. Three deliverables: (1) JSON Schema file, (2) Rust serde renames + test updates, (3) schema validation tests. Then Go validation pipeline as a separate task.

**Open questions:** None.

### Session — 2026-02-15

**Stopped after:** Completed the entire compensation plan configuration pipeline. Rust serde renames (28 fields), JSON Schema file, Go validation pipeline (5-step: schema → unmarshal → commission resolution → business rules → translation), ADR-015 written. Three rounds of code review produced and fixed 14 findings (missing validation rules, test coverage gaps, DRY violations, stale docs). M-3 (identical commission structs) deferred to backlog. Final state: 75 Go tests at 95% coverage, all passing. ADR-015 updated with 2 missing structural translations.

**Next up:** Commission engine implementation begins. The config pipeline is complete end-to-end. Likely starting with the unilevel commission calculator in Rust, consuming the typed `CompensationPlan` struct.

**Open questions:** None. One deferred code review finding (M-3: unify identical commission structs) logged in Backlog.

### Session — 2026-02-16

**Stopped after:** Completed EventStore implementation. Groomed (brainstorm → design doc → implementation plan → preflight), executed via subagent-driven development in worktree, code reviewed (3 fixes applied: category matching parity, Postgres Append efficiency, design doc sync), ADR-016 written, ADRs 001 and 004 updated. Merged to main, worktree cleaned up. 20 unit tests + 7 integration tests. Three deferred code review findings (S4-S6) logged in Backlog.

**Next up:** Commission engine implementation. The config pipeline and event store are both complete. Natural starting point is the unilevel commission calculator in Rust, consuming the typed `CompensationPlan` struct and writing results to the EventStore.

**Open questions:** None.

### Session — 2026-02-17

**Stopped after:** Completed unilevel commission calculator implementation. Groomed (brainstorm → design doc → implementation plan → preflight), executed via subagent-driven development in worktree (11 tasks), code reviewed after each task (spec compliance + code quality). 3 code review fixes applied (u16→u8 narrowing guard, boundary test for exact PV threshold, redundant get_upline call). ADR-017 written with 6 architectural decisions. Merged to main via fast-forward, worktree cleaned up. 185 tests (177 unit + 8 property-based). Also upgraded workflow files — added `review-project` command from latest WORKFLOW_TEMPLATE.md.

**Next up:** Next commission calculator (binary, matrix, or stairstep) or Go integration boundary (FFI/gRPC) for calling the Rust calculator from the Go platform layer.

**Open questions:** None.
