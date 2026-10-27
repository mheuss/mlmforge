# Version History

## Unreleased

**Test count:** 1018 (Rust), 580 (Go)

### Added
- Project research documentation (MLM fundamentals, compensation plans, glossary, competitive landscape, legal/regulatory)
- Design principles document (architecture, bounded contexts, extensibility model)
- Workflow setup (CLAUDE.md, pre-implementation, pre-commit, resume-plan, sync-orchestration)
- Go module structure with 8 bounded context packages (`internal/{context}/`)
- Rust workspace with `network-engine` crate (`engine/`)
- CI pipeline. GitHub Actions for Go (build, test, fmt, vet) and Rust (build, test, fmt, clippy).
- Domain discovery. 8 bounded context analyses from legacy osMLM codebase (`mlmforge-hub:docs/discovery/`).
- Cross-domain synthesis. Dependency graph, 6 shared concerns, 13 ownership disputes, ~20 interface candidates (`mlmforge-hub:docs/discovery/synthesis.md`).
- ADRs 006–011: Context boundary immutability, enrollment orchestration, event bus strategy, sponsor as tree relationship, domain events for data flow, reporting ownership
- Go interface definitions for all 8 bounded contexts. 26 interfaces, 70+ domain types, 10 domain events (Platform, Identity, Network Engine, Financial, Commerce, Engagement, Operations, Portals).
- Unilevel tree implementation in Rust. Arena storage, 12 operations (add_root, add_node, remove_node, get_parent, get_children, get_upline, get_downline, get_position, get_branch, count_downline, count_branch, is_descendant_of), 44 unit tests, 6 property-based tests, 4 edge case tests.
- Decision 007: Unilevel tree implementation choices (arena storage, UUID user IDs, iterative BFS, tombstone deletion, position-indexed model)
- Network engine development guide (`docs/development/network-engine.md`)
- Compensation plan discovery. Per-structure configuration analysis for all 7 plan types (`mlmforge-hub:docs/discovery/compensation-plans/`), combining legacy system extraction with industry research.
- Identified industry-standard bonus types not in legacy: matching bonus, infinity bonus, rank advancement, override/differential, sponsor/introducer, fast start, pairing, matrix completion, car/lifestyle, leadership pool
- Identified additional plan types: generation plan (new file), monoline (degenerate streamline config), Australian X-Up (unilevel variant), board plan (matrix cycling mode)
- Confirmed hybrid plans are already supported by multi-structure architecture
- Review guide for compensation plan annotation workflow (`mlmforge-hub:docs/discovery/compensation-plans/REVIEW-GUIDE.md`)
- ADRs 012–013: Compensation plan configuration storage (hybrid relational + JSONB, typed Rust structs, Go config pipeline, version lifecycle, immutability rules), commission run integrity and mid-period plan changes (void-and-rerun, adjustment records)
- Unilevel compensation plan review annotations. MUST (6 industry-standard features), SHOULD (leadership development bonus, donated placement, rollover volume lifespan), active leg requirement documented.
- Compensation plan configuration design document. Brainstorm resolving all 9 open questions, full design covering 6 plan types x 10 configuration areas with plain-English narrative, YAML schema, Rust types, and validation rules.
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
- Tree loads build the engine from the event stream, restoring a stored snapshot when one is usable. `tree_snapshots` (migration 000010) keeps one snapshot per tree, taken every 1000 events (HEU-297).
- Matrix removal through the event path. `tree remove --pruning-mode promote_earliest|holding_tank` appends a matrix removal, and the consumer projects every removed user, re-sponsored recruit and relocated survivor in one transaction. The worker answers `check_mutation` for a matrix `remove_node` and reports `relocated`. Protocol 14. Replay rebuilds the holding tank. The engine now refuses to place a user who is in the holding tank, with `USER_ALREADY_EXISTS`. (HEU-582)

### Changed
- Doc block accuracy fixes in `error.rs` (panic documentation), `unilevel.rs` (get_node description, downline count terminology)
- `PairingConfig.weekly_cap` renamed to `cap_per_period`, removed redundant serde rename
- `config.PaymentMethod` renamed to `PayoutMethod` in Go and Rust to disambiguate from `financial.PaymentMethod`
- `interface{}` replaced with `any` in `operations/types.go`
- Custom Debug impl for UnilevelTree and BinaryTree shows node count and root user ID instead of full arena dump
- PostgresEventStore.Append N+1 INSERT replaced with pgx.Batch for single round-trip
- MemoryEventStore.ReadStream linear scan replaced with index-based slice
- CI: Rust jobs now use `--workspace` flag. Added `golangci-lint` step with `.golangci.yml` config.
- Every compensation plan's lowest rank requires nothing. Go validation and the engine's `load_plan` refuse a lowest rank that sets a nonzero personal, group or retail volume, a distributor count or leg quality. They also refuse required products, a window or a tenure. Both name the rank. The rank evaluator no longer evaluates the lowest rank. Every distributor in an `evaluate_ranks` request holds at least that rank, including one in no registered tree. `evaluate_ranks` returns `unranked` only for a plan with no ranks. Protocol 18. (HEU-761)
- A `max_group_volume_per_leg` of 0 now means no per-leg cap in the engine. The engine used to read 0 as a cap of zero. A rank with that cap failed any distributor whose leg carried volume. The engine also loads a plan that leaves the field out. It used to refuse that plan. The engine's `load_plan` refuses a negative or non-finite cap. It names the rank. In Go, the schema refuses a negative cap first. `validateRanks` refuses a negative or non-finite cap for callers that skip the schema. It names the rank. Protocol 19. (HEU-955)
- Every commission calculation that takes a tree refuses a request unless every distributor it requires has a snapshot. The error names the lowest missing user ID. Before, leaving out a snapshot could change who got paid with no error, when the omitted node sat off every walked path. Matrix leaves out the holding tank. Binary checks the owner of each position. Streamline checks every member of every stream, frozen streams included. Stairstep's override walk returns an error where it used to skip a node with no snapshot. The engine's `load_plan` refuses a generation structure with `level_commissions_enabled` true and no `level_commission`. Protocol 20. (HEU-731, HEU-727, HEU-728)
- A rank's per-leg cap now limits how much each first-level leg counts toward group volume. It used to fail the rank when any leg was above the cap. Personal volume is not capped. A plan can set the cap as `max_group_volume_per_leg_fraction`, a fraction of the rank's `group_volume` requirement in [0, 1]. Setting both cap fields above 0 is refused by the schema, Go validation and the engine's `load_plan`. The schema and `load_plan` refuse an unknown key in a structure qualification. When `group_volume` is above 0, Go reports a fixed cap at or above it as a warning, not an error. Protocol 21. (HEU-960)
- Rank evaluation refuses a request that leaves out a member of any tree a rank above the lowest names. The error names the lowest missing user ID. Before, an omitted leg could lower an upline's evaluated rank with no error. A distributor in none of those trees is still accepted and holds the lowest rank. Protocol 22. (HEU-965)
- For values of up to 15 significant digits, a fractional per-leg cap now equals the fixed cap it stands for. The engine multiplies the decimal fraction and group volume exactly. 29% of 100,000 used to cap at 28,999.999999999996. That could withhold a rank that the fixed cap of 29,000 awards. Protocol 23. (HEU-966)
- The engine computes volume and money as exact decimals, not `f64`. Numbers on the wire stay bare JSON numbers. They are now written without trailing zeros, so `500.0` becomes `500`. A quoted number is refused. An authored value must have an absolute value below 10^15, with at most 10 decimal places. Protocol 24. (HEU-969, phase 1)
- New refusals with exact decimals. The engine refuses a negative carried binary leg volume, and a sum or product it cannot hold exactly (`Overflow` or `Precision`). Go refuses a quoted number on the wire, and YAML number forms JSON does not have, such as hex, `1_000`, `+5`, `5.` and `.5`. (HEU-969, phase 1)
- A volume-ratio payout rounds once, half to even, at 13 decimal places. An aggregate-cap share truncates at 13 places, and each earner's last share takes the remainder, so the shares sum exactly to the cap. (HEU-969, phase 1)
- The plan hash changes for a plan value that f64 wrote in exponent form. `0.0000001` used to be written `1e-7`. (HEU-969, phase 1)
- The engine's `load_plan` refuses a plan whose rank ordinals repeat or do not ascend, two ranks sharing a name, or a negative rank volume threshold. The thresholds are `personal_volume`, `group_volume`, `min_retail_volume`, `distributor_count.min_leg_group_volume` and a `contains_personal_volume` predicate's `min_personal_volume`. Go already refused the ordinal and name cases. It now refuses the negative thresholds for a caller that skips the schema. The schema caps a rank ordinal at 65535, the engine's limit. A plan that loaded under protocol 24 can fail under 25. Protocol 25. (HEU-646, HEU-752, HEU-753, HEU-974)
- Each earning's money is rounded to the plan currency's ISO 4217 minor unit, ties away from zero. Volume and rates are not rounded. (HEU-969, phase 2)
- A binary aggregate cap is split by largest remainder in whole minor units, so the shares sum exactly to the cap. This replaces the phase 1 split at 13 places. (HEU-969, phase 2)
- `load_plan` and Go validation refuse a base currency missing from the minor-unit table, and a plan money value that is not a whole number of minor units. (HEU-969, phase 2)
- A library call to `calculate_binary_pairing` with a pairing percent outside [0, 1] returns an error. (HEU-982)

### Fixed
- Production `unwrap()` in unilevel.rs replaced with `match` on `Option`
- `cv_amount` validation added (rejects negative and NaN)
- JSON Schema `search_mode` enum aligned with Rust types and ADR-008
- Multi-tree rank evaluation no longer undercounts ranks. `evaluate_ranks` iterates to a fixpoint instead of a single ordered pass, so a distributor's descendants are counted regardless of cross-structure depth. (HEU-460)
- Clippy `--all-targets` gate. Boxed `Response`'s `result` and `error` fields so the worker's 15 `result_large_err` errors clear, then tightened CI's clippy step to `--all-targets`. The NDJSON wire format is unchanged. Boxing also fixes `Response`'s size at 48 bytes in every build. (HEU-560)
- Worker test build and shipped build now link the same `serde_json`. A `preserve_order` dev-feature reached the worker through Cargo feature unification under `cargo test` and `cargo clippy --all-targets`, but never under `cargo build`, so `serde_json::Value` payloads emitted insertion order in test builds and sorted order in production. Nothing exercised the bytes that shipped. Eight integer-keyed config maps now parse their keys through `deserialize_with` helpers in `serde_helpers.rs`, which works on both serde's native path and its `Content` buffer, so key order stopped mattering and the feature could go. The two binaries now emit byte-identical NDJSON. (HEU-648)
- `cargo test -p network-engine-worker` passes. It failed `load_plan_rejects_duplicate_structure_names` with `invalid type: string "1", expected u8`, which read as a deserialize bug and was a build-scope artifact of the same feature split. (HEU-638)
- Duplicate rate-key spellings are rejected. The schema's `propertyNames` patterns are `^0*(...)`, so `"1"` and `"01"` are both valid and name the same Rust `u8` map entry, and one rate silently won. Caught in Go validation with a `duplicate_value` error naming both spellings, and in the engine as a parse error. Accepted spellings are otherwise unchanged. (HEU-648)
- `tree load` prints the canonical lowercase tree ID it loaded. It used to print `--tree-id` as typed, so an uppercase ID printed one string and loaded another. `LoadResult` carries the canonical ID. The startup example in UC-NET-012 reports a failed create on its own instead of as "0 of 0 non-root placements". (HEU-879, HEU-881)
- Markdown always diffs as text. `.gitattributes` sets `*.md diff`, so a markdown file holding a stray NUL byte no longer renders as binary. `git grep` now prints its matching lines, and gitleaks now reads its added lines. Without the attribute gitleaks skipped the whole file, while still counting the commit as scanned. (HEU-802)
- `docs/development/postgres-stores.md` states the paired-store rule itself: a new memory/Postgres store pair has one test body that runs against both twins. Its opening pointer named UC-NET-008. That use case is about rank result persistence. It covers neither paired twins nor a shared suite. (HEU-792)
- The in-memory tree store matches every id it holds by UUID value, as Postgres does. That covers row, tree, user, parent, sponsor and event ids. It used to match them by spelling, so an uppercase id found no row in memory and the right row in Postgres. It stores and returns them in canonical form. (HEU-931)
- The qualification history store's twins run one shared test suite. The memory twin now stamps EvaluatedAt at microsecond precision with no monotonic reading, as a Postgres read returns it. It also keeps its own copies of each rank and ordinal, so a caller changing a saved entry or a returned row no longer changes what the store holds. It refuses an ordinal of 0 and a save under a cancelled context, as Postgres does. (HEU-938)
- Every commission calculator checks each snapshot's `personal_volume` before it reads any snapshot. NaN, both infinities and negative values return `InvalidPersonalVolume`, which is `CALCULATION_ERROR` on the wire. A NaN used to clear the minimum-PV gate at any threshold. A `<` comparison against NaN is always false. Over the wire only a negative value reaches the check. The worker's JSON parse already refuses NaN and infinity before any calculator runs. Protocol 15. (HEU-709)
- A unilevel, binary or matrix tree refuses the nil UUID as a user id with `NIL_USER_ID` on add or place. A streamline engine refuses it on add. The arena marks a freed slot with the nil UUID, so a live node with that id could not be told apart from a freed slot. Reading a freed slot through the arena's node accessors now panics in release builds too. It used to return the freed slot's zeroed node. A tree load refuses a projected row whose user id is the nil UUID as invalid data, so reject-event can clear that placement when it is the stream's last event. Protocol 16. (HEU-710)
- A matrix add or place of a user in the holding tank answers `USER_IN_HOLDING_TANK` in place of `USER_ALREADY_EXISTS`. `tree reject-event` counts that code as evidence. It also counts replay refusing the stream's last `node_placed` with a refusal code. A tanked user's placement is now cleared whether reject-event or an earlier write delivered it first. Protocol 17. (HEU-930)
- The tree event consumer refuses a `root_added`, `node_placed` or `node_removed` whose user, parent or sponsor ID is not a canonical lowercase UUID. It also refuses the nil UUID, except as a root's sponsor. It refuses before any store or engine call. Then `tree reject-event` can clear the event. A malformed ID used to stop the tree with a raw store or engine error that reject-event could not clear. Replay runs the same checks. (HEU-933)
- The Go toolchain moves to go1.27.3 in `go.mod`, `mise.toml` and CI. govulncheck flags GO-2026-6809 in `crypto/x509` at go1.27.2. It failed the Go vulnerability check on main's CI run at `bd08108`. (HEU-973)

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
