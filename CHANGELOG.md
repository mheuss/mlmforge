# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Comprehensive research documentation covering MLM fundamentals, compensation plans, industry glossary, competitive landscape, and legal/regulatory framework
- Foundational design principles document defining Go+Rust architecture, 8 bounded contexts, event sourcing strategy, and three-tier extensibility model
- Development workflow with pre-implementation and pre-commit quality gates
- Go module structure with 8 bounded context packages and interface definitions (26 interfaces, 70+ domain types)
- Rust workspace with `network-engine` crate for tree structures and commission calculations
- CI pipeline for Go (build, test, fmt, vet, golangci-lint) and Rust (build, test, fmt, clippy)
- Unilevel tree structure in Rust with arena storage, 12 operations, and property-based tests
- Binary tree structure in Rust with position-indexed placement, slots map, and property-based tests
- Compensation plan configuration types in Rust. 85 public types across 14 files covering all 6 plan types.
- Compensation plan configuration pipeline in Go. JSON Schema validation, YAML unmarshal, commission resolution, business-rule validation, and structural translation. 75 tests at 95% coverage.
- JSON Schema (Draft 2020-12) for compensation plan YAML validation
- EventStore interface with PostgreSQL and in-memory implementations. Optimistic concurrency, category queries, JSON envelope with opaque payload.
- Unilevel commission calculator. Two-phase prep+walk algorithm with compression, active leg tiers, and rate table lookup. 185 tests.
- Binary commission calculator. Pairing bonus with carry-forward and cap per period. Handler in worker, Go EngineClient method. 30 tests.
- Go/Rust integration boundary. NDJSON subprocess protocol, StdioTransport, EngineClient with 15 operations, Rust worker binary with dispatch and handlers.
- Sponsor query handlers (get_sponsor, get_sponsor_upline, get_sponsored) across both tree types and worker operations
- TreeNavigator trait for polymorphic dispatch across tree types
- Sponsor edges on Node struct with sponsor-line traversal operations
- Shared Arena struct used by all tree types for cache-friendly storage and traversal
- 24 design rationale documents (001-024) documenting architecture decisions
- Three project reviews (2026-02-18, 2026-02-20, 2026-02-21) with systemic fixes: deterministic ordering, schema validation, property tests, EventStore hardening, cross-language deserialization safety, transport concurrency fixes
- Matrix tree structure in Rust with BFS spillover, holding tank, and pruning. 72 unit tests, 13 property tests.
- Matrix commission calculator. Level-based upline walk on the placement tree with depth capped at `min(height, max_depth)`. Compression, active leg tiers, rate table lookup. 21 unit tests, 7 property tests.
- Stairstep breakaway commission calculator. Two-phase walk: Walk 1 for personal group (level-based, stops at breakaway boundaries), Walk 2 for generation overrides on breakaway legs. Differential and fixed override modes. BreakawayConfig with override_calculation enum. 40+ tests.
- Shared commission walk module (`commission/walk.rs`). Generic level-based walk extracted from unilevel/matrix/stairstep calculators. `LevelWalkConfig` with `should_stop` callback for plan-specific boundaries. ADR-022.
- Australian X-Up (pass-up) for unilevel commissions. `PassUpConfig` on `UnilevelStructureConfig`. `build_pass_up_context` precomputes skip sets by enrollment order. Supports `includes_commissions` mode for full subtree skipping.
- Fixed override mode for stairstep. `FixedOverride` configuration alongside differential overrides.
- Binary multi-position caps and ownership parameter. `MultiPositionCapMode` config, `position_id` on earnings, aggregate cap post-processing, `ownership` map parameter on `calculate_binary_pairing`.
- Generation commission calculator. Standalone calculator for generation-based compensation. ThresholdRank and SameRank boundary modes, `empty_consumes` flag, combined level+generation mode. Reuses `count_generations_upward` from stairstep. ADR-024. 30+ tests.
- Migration framework. `golang-migrate` integration with CLI subcommand. Schema management for EventStore and tree_nodes tables.
- Tree persistence layer. `TreeStore` interface with PostgreSQL and in-memory implementations. Event-sourced adjacency table (`tree_nodes`). `TreeEventConsumer` projects tree mutation events to the store. Startup bulk-load rebuilds engine state from the table. 15+ integration tests. ADR-022.
- Board plan engine. Flat position-array boards with configurable width/height (2-5 x 1-4). Board cycling, member displacement pool, dissolution, inactive compression, stalled detection, and board commission calculation. Snapshot serialization. ADR-023. 100+ tests.
- Streamline engine. Linear chain structures with multiple streams per structure. Dynamic compression, stream expansion, frozen/unfrozen streams, member allowance management. Streamline commission calculator. Snapshot serialization. 50+ tests.
- Handlers module split. Restructured monolithic `handlers.rs` (2,444 lines) into domain sub-modules: `handlers/{common,tree,commission,board_plan,streamline,snapshot}.rs`. Extracted `require_plan`, `require_unilevel_tree`, `require_binary_tree` helpers to deduplicate commission handler boilerplate.
- Design rationale documents 021-024: sponsor vs placement in commission, shared commission walk, snapshot persistence, generation calculator reuse.

### Changed
- `PairingConfig.weekly_cap` renamed to `cap_per_period`
- `config.PaymentMethod` renamed to `PayoutMethod` to avoid naming collision with `financial.PaymentMethod`

### Fixed
- JSON Schema `search_mode` enum aligned with Rust types
- `cv_amount` validation rejects negative and NaN values

---

## [0.1.0] — 2026-02-06

### Added
- Initial project structure
- Legacy codebase for reference analysis
