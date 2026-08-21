# Version History

## Unreleased

**Test count:** 819 (Rust), 218 (Go)

### Added
- Project research documentation (MLM fundamentals, compensation plans, glossary, competitive landscape, legal/regulatory)
- Design principles document (architecture, bounded contexts, extensibility model)
- Workflow setup (CLAUDE.md, pre-implementation, pre-commit, resume-plan, sync-orchestration)
- Go module structure with 8 bounded context packages (`internal/{context}/`)
- Rust workspace with `network-engine` crate (`engine/`)
- CI pipeline. GitHub Actions for Go (build, test, fmt, vet) and Rust (build, test, fmt, clippy).
- Domain discovery. 8 bounded context analyses from legacy osMLM codebase (`docs/discovery/`).
- Cross-domain synthesis. Dependency graph, 6 shared concerns, 13 ownership disputes, ~20 interface candidates (`docs/discovery/synthesis.md`).
- ADRs 006–011: Context boundary immutability, enrollment orchestration, event bus strategy, sponsor as tree relationship, domain events for data flow, reporting ownership
- Go interface definitions for all 8 bounded contexts. 26 interfaces, 70+ domain types, 10 domain events (Platform, Identity, Network Engine, Financial, Commerce, Engagement, Operations, Portals).
- Unilevel tree implementation in Rust. Arena storage, 12 operations (add_root, add_node, remove_node, get_parent, get_children, get_upline, get_downline, get_position, get_branch, count_downline, count_branch, is_descendant_of), 44 unit tests, 6 property-based tests, 4 edge case tests.
- Decision 007: Unilevel tree implementation choices (arena storage, UUID user IDs, iterative BFS, tombstone deletion, position-indexed model)
- Network engine development guide (`docs/development/network-engine.md`)
- Compensation plan discovery. Per-structure configuration analysis for all 7 plan types (`docs/discovery/compensation-plans/`), combining legacy system extraction with industry research.
- Identified industry-standard bonus types not in legacy: matching bonus, infinity bonus, rank advancement, override/differential, sponsor/introducer, fast start, pairing, matrix completion, car/lifestyle, leadership pool
- Identified additional plan types: generation plan (new file), monoline (degenerate streamline config), Australian X-Up (unilevel variant), board plan (matrix cycling mode)
- Confirmed hybrid plans are already supported by multi-structure architecture
- Review guide for compensation plan annotation workflow (`docs/discovery/compensation-plans/REVIEW-GUIDE.md`)
- ADRs 012–013: Compensation plan configuration storage (hybrid relational + JSONB, typed Rust structs, Go config pipeline, version lifecycle, immutability rules), commission run integrity and mid-period plan changes (void-and-rerun, adjustment records)
- Unilevel compensation plan review annotations. MUST (6 industry-standard features), SHOULD (leadership development bonus, donated placement, rollover volume lifespan), active leg requirement documented.
- Compensation plan configuration design document. Brainstorm resolving all 9 open questions, full design covering 6 plan types x 10 configuration areas with plain-English narrative, YAML schema, Rust types, and validation rules (`docs/plans/2026-02-12-compensation-plan-config-design.md`).
- Decisions 008-014: Per-structure compensation configuration options. Common (periods, volume, ranks, bonuses, payout, caps), unilevel (rate table, compression, pass-up, donated placement), binary (pairing bonus, volume-after-payout, carry-forward, cycle/step), matrix (width/height, completion bonus, position bonus, board plan), stairstep (breakaway, differential overrides, generation counting), generation (boundary modes, empty generations, combined level+generation), streamline (dynamic compression, streams, rank expansion, monoline).
- Compensation plan configuration types in Rust. 85 public types across 14 files in `config/` module: `CompensationPlan` root struct, `StructureConfig` tagged enum (6 plan types), period/volume/rank/eligibility/commission/compression types, per-structure configs (binary pairing/cycle, stairstep breakaway, generation boundaries, streamline dynamic compression, matrix spillover/pruning), 12 bonus program types, payout/caps/placement types. 99 unit tests with JSON deserialization round-trips. Self-documenting: every type and field has a doc comment explaining business meaning. (`engine/network-engine/src/config/`)
- ADR-015: Compensation plan schema and wire format. Canonical YAML wire names, Rust serde renames, JSON Schema Draft 2020-12 for structural validation, Go for business-rule validation, 5 structural translations (`decisions/015-compensation-plan-schema-and-wire-format.md`).
- Rust serde renames for compensation plan config types. ~22 fields aligned to canonical YAML wire names (`engine/network-engine/src/config/`).
- JSON Schema for compensation plan YAML. Draft 2020-12, monolithic file with `$defs`, `if/then/else` structure discriminator, descriptions on every property (`schemas/compensation-plan.schema.json`).
- Go validation pipeline. 5-step process (JSON Schema validation, YAML unmarshal, commission resolution, business-rule validation, structural translation), 8 files in `internal/config/`, 75 tests at 95% coverage. Business rules validate referential integrity (rank/structure references), cross-field dependencies, ordering constraints, and produce semantic warnings. Structural translation handles 5 YAML-to-Rust shape differences.
- EventStore interface and implementations in Platform. `EventStore` interface with `Append` (optimistic concurrency via expected version), `ReadStream`, `ReadCategory`. `Event` envelope with `json.RawMessage` payload. `NewEvent` input type. `ConcurrencyError` with stream/expected/actual versions. Two implementations: `PostgresEventStore` (pgx v5, pgxpool, JSONB, category index via `split_part`) and `MemoryEventStore` (in-memory for testing). 20 unit tests, 7 PostgreSQL integration tests. (`internal/platform/`)
- ADR-016: EventStore design. Unified store for event sourcing and domain events, JSON envelope, category-ID stream naming, optimistic concurrency, pgx v5 driver, Platform ownership (`decisions/016-eventstore-design.md`).
- Unilevel commission calculator in Rust. Two-phase prep+walk algorithm, SkipInactive and SkipBelowRank compression, active leg tier depth limits, rate table lookup. 185 tests (177 unit + 8 property-based). ADR-017 documents 6 architectural decisions. (`engine/network-engine/src/commission/`)
- ADR-017: Commission calculation architecture. Snapshots carry facts, calculators apply rules. Flat output, two-phase pattern, compression in the walk. (`decisions/017-commission-calculation-architecture.md`)
- Go/Rust integration boundary. NDJSON subprocess protocol over stdin/stdout, `EngineTransport` interface, `StdioTransport`, `EngineClient` with 15 operations, Rust worker binary with dispatch and handlers, contract test fixtures shared between Go and Rust. (`internal/networkengine/`, `engine/worker/`)
- ADR-018: Config pipeline. Five-stage Go validation pipeline, two-pass commission parsing, Commission marker interface, validation severity. (`decisions/018-config-pipeline.md`)
- ADR-019: NDJSON protocol. Request-response envelope with ID correlation, RawValue params, error code taxonomy, panic recovery, context cancellation, stderr capture. (`decisions/019-ndjson-protocol.md`)
- ADR-020: Tree topology separation. Trees are pure topology. Placement logic lives in the caller. Sponsor edges are data, not logic. (`decisions/020-tree-topology-separation.md`)
- Sponsor edges on shared Node struct. Separate sponsor/sponsored fields alongside parent/children, with sponsor-line traversals (get_sponsor, get_sponsor_upline, get_sponsored) in both tree types and worker operations.
- Binary tree in Rust. Arena-backed with slots map for position tracking. 25 unit tests, 13 edge case tests, 10 property tests. Worker integration with create_tree/add_node/query operations. Go EngineClient updated.
- Binary commission calculator. Pairing bonus with carry-forward, cap per period. 30 tests. (`engine/network-engine/src/commission/binary.rs`)
- TreeNavigator trait extraction. 13-method read-only interface for polymorphic dispatch across tree types. Worker handlers use `dyn TreeNavigator` for query dispatch.
- Shared Arena struct extracted to `tree/arena.rs`. Storage, alloc/free, resolve, BFS downline, upline walk, sponsor walks, position queries. Both tree types compose it.
- Shared test helpers extracted to `tree/test_helpers.rs`. `test_uuid` and `test_uuid_u16` used by all tree types and property tests.
- Project review (2026-02-18). 90 findings identified and resolved across Rust engine, Go platform, config pipeline, and documentation.
- Project review (2026-02-20). 38 findings across 5 review areas (Rust engine, Rust worker, Go internal, docs/tracking, schemas/config). Organized into 8 parallel batches. All resolved across Waves 1-4.
- Project review (2026-02-21). 62 findings (2 high, 20 medium, ~40 low). High and medium items resolved in-session across 3 waves. Systemic fixes include deterministic ordering, schema validation, property tests, EventStore hardening, and documentation updates.
- Matrix tree in Rust. Arena-backed forced matrix with configurable width, BFS spillover, holding tank for deferred placement, pruning (promote-earliest, move-to-tank). 72 unit tests, 13 property tests. Worker integration.
- Matrix commission calculator in Rust. Level-based upline walk on the placement tree with effective depth `min(height, max_depth)`. Mirrors unilevel pattern (ADR-017). SkipInactive and SkipBelowRank compression, active leg tier depth limits, rate table lookup, multiplier fallback. 21 unit tests, 7 property-based tests. (`engine/network-engine/src/commission/matrix.rs`)
- Stairstep breakaway commission calculator in Rust. Two-phase walk: Walk 1 for personal group earnings (level-based, stops at breakaway boundaries), Walk 2 for generation overrides on breakaway legs. Differential and fixed override modes. BreakawayConfig with override_calculation enum. (`engine/network-engine/src/commission/stairstep.rs`)
- Shared commission walk module. Generic level-based walk extracted from unilevel/matrix/stairstep calculators. `LevelWalkConfig` with `should_stop` callback for plan-specific boundary logic. ADR-022. (`engine/network-engine/src/commission/walk.rs`)
- Australian X-Up (pass-up) for unilevel commissions. `PassUpConfig` moved from `BonusConfig` to `UnilevelStructureConfig`. `build_pass_up_context` precomputes skip sets by enrollment order with `includes_commissions` mode for full subtree skipping. (`engine/network-engine/src/commission/walk.rs`)
- Fixed override mode for stairstep. `FixedOverride` configuration alongside differential overrides. (`engine/network-engine/src/commission/stairstep.rs`)
- Binary multi-position caps and ownership parameter. `MultiPositionCapMode` config enum, `position_id` field on `BinaryCommissionEarning`, aggregate cap post-processing, `ownership` map parameter on `calculate_binary_pairing`. (`engine/network-engine/src/commission/binary.rs`)
- Generation commission calculator in Rust. Standalone calculator for generation-based compensation. ThresholdRank and SameRank boundary modes, `empty_consumes` flag, combined level+generation mode. Reuses `count_generations_upward` from stairstep via `breakaway_set` parameter. ADR-024. (`engine/network-engine/src/commission/generation.rs`)
- Generation guide and design rationale document. (`content/guides/generation.md`, `content/design-rationale/024-generation-calculator-reuse.md`)
- Migration framework in Go. `golang-migrate` integration with Cobra CLI subcommand. Schema management for EventStore (`000001`) and tree_nodes (`000002`) tables. (`internal/platform/`)
- Tree persistence layer in Go. `TreeStore` interface with PostgreSQL (`PostgresTreeStore`) and in-memory (`MemoryTreeStore`) implementations. Event-sourced adjacency table (`tree_nodes`) with `TreeEventConsumer` projecting tree mutation events. Startup bulk-load from table to rebuild engine state. 15+ integration tests. (`internal/networkengine/`, `migrations/`)
- Board plan engine in Rust. Flat position-array boards with configurable dimensions (width 2-5, height 1-4). Board creation, member addition, cycling on full boards, displaced member pool, dissolution, inactive compression, stalled board detection. Board commission calculation via `calculate_board_commissions`. Snapshot serialization (ADR-023). `TreeInstance::BoardPlan` variant. Worker handlers for all board operations. Go `EngineClient` methods. 100+ tests. (`engine/network-engine/src/board_plan/`, `engine/network-engine-worker/src/handlers/board_plan.rs`)
- Streamline engine in Rust. Linear chain structures with multiple streams per structure. Dynamic compression, stream expansion/freezing, member allowance management, stream assignment modes (round-robin, least-full). Streamline commission calculator. Snapshot serialization. `TreeInstance::Streamline` variant. Worker handlers for all streamline operations. Go `EngineClient` methods. 50+ tests. (`engine/network-engine/src/streamline/`, `engine/network-engine-worker/src/handlers/streamline.rs`)
- Handlers module split. Restructured monolithic `handlers.rs` (2,444 lines, 41 functions) into domain sub-modules: `handlers/{common,tree,commission,board_plan,streamline,snapshot}.rs`. Extracted `require_plan`, `require_unilevel_tree`, `require_binary_tree` helpers to deduplicate commission handler boilerplate. Dispatch table updated to qualified paths. (`engine/network-engine-worker/src/handlers/`)
- Design rationale documents 021-024: sponsor vs placement in commission walks, shared commission walk extraction, snapshot persistence for tree-layer types, generation calculator reuse of `count_generations_upward`.

### Changed
- Doc block accuracy fixes in `error.rs` (panic documentation), `unilevel.rs` (get_node description, downline count terminology)
- `PairingConfig.weekly_cap` renamed to `cap_per_period`, removed redundant serde rename
- `config.PaymentMethod` renamed to `PayoutMethod` in Go and Rust to disambiguate from `financial.PaymentMethod`
- `interface{}` replaced with `any` in `operations/types.go`
- Custom Debug impl for UnilevelTree and BinaryTree shows node count and root user ID instead of full arena dump
- PostgresEventStore.Append N+1 INSERT replaced with pgx.Batch for single round-trip
- MemoryEventStore.ReadStream linear scan replaced with index-based slice
- CI: Rust jobs now use `--workspace` flag. Added `golangci-lint` step with `.golangci.yml` config.

### Fixed
- Production `unwrap()` in unilevel.rs replaced with `match` on `Option`
- `cv_amount` validation added (rejects negative and NaN)
- JSON Schema `search_mode` enum aligned with Rust types and ADR-008
- Multi-tree rank evaluation no longer undercounts ranks. `evaluate_ranks` iterates to a fixpoint instead of a single ordered pass, so a distributor's descendants are counted regardless of cross-structure depth. (HEU-460)
- Clippy `--all-targets` gate. Boxed `Response`'s `result` and `error` fields so the worker's 15 `result_large_err` errors clear, then tightened CI's clippy step to `--all-targets`. The NDJSON wire format is unchanged. Boxing also makes `Response` 48 bytes in every build, where it was 112 or 152 depending on whether a sibling crate's dev-dependencies were in the build graph. (HEU-560)

### Removed
- None.

---

## 0.1.0 — 2026-02-06

**Test count:** 0

### Added
- Initial project structure
- Legacy osMLM codebase for reference
- Domain space index (30 domains cataloged from legacy system)

### Changed
- None.

### Fixed
- None.

### Removed
- None.
