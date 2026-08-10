# Network Engine Development Guide

Implementation patterns and conventions for the Rust network engine. Read this before working on any tree type or engine component.

## Arena Storage Pattern

All tree types use the same storage approach:

- `Vec<Node>` arena for contiguous, cache-friendly node storage
- `HashMap<Uuid, NodeIndex>` for O(1) lookup by user ID
- `NodeIndex(usize)` wrapper for type-safe arena handles
- Free list (`Vec<NodeIndex>`) for tombstone slot reuse

Tree walks follow arena indices directly. No hash lookups during traversal. The HashMap is only used at entry points (resolving a user ID to a node).

Shared arena logic lives in `tree/arena.rs`. Both UnilevelTree and BinaryTree compose the shared `Arena` struct for storage, alloc/free, resolve, BFS downline, upline walk, sponsor walks, and position queries. The abstraction was extracted when the binary tree was implemented.

## Node Visibility Convention

`Node` is the internal arena type. It is also the read-only view returned by traversal methods (`get_parent`, `get_children`, `get_upline`, `get_downline`, `get_branch`).

- `user_id`, `depth`, `enrolled_at` are `pub` — the consumer-facing read surface.
- `parent`, `children` are `pub(crate)` — arena indices that are meaningless outside the tree.

`TreePosition` is the enriched output type with derived data (downline counts, child count, position). Use it when consumers need computed data. Use `&Node` when they need raw traversal results.

## Traversal Pattern

All downline and branch walks use iterative BFS with `VecDeque`. No recursion. This is non-negotiable — recursive traversal blows the stack on deep chains.

- BFS gives level-ordered results, which matches how distributors think about their organization.
- Upline walks follow parent links directly. O(d) where d is depth. No queue needed.
- `is_descendant_of` walks upline from the candidate, not downline from the ancestor. O(d) vs O(n).

Depth limiting is handled by the enqueue guard only. Nodes at the depth boundary are collected but their children are not enqueued.

## Position-Indexed Model

Child position is the key abstraction across tree types:

- **Unilevel:** Position = index in parent's `children` Vec. Unbounded width.
- **Binary:** Position 0 = left leg, position 1 = right leg.
- **Matrix:** Positions 0 through width-1.

The same `get_branch(user, position)` call works across all tree types. `downline_counts` in `TreePosition` uses `HashMap<usize, usize>` because positions can be sparse (binary node with only a right child has position 1 but not 0).

## Error Handling

Every fallible public method returns `Result<T, TreeError>`. No panics for input errors.

The one exception: internal consistency violations (e.g., a node claims a parent that doesn't list it as a child). These indicate a bug in the tree implementation, not bad input. Use `expect` with a clear message. Document the rationale inline.

Error messages follow the pattern: lowercase, no trailing period, context in parentheses. Example: `"position {position} out of range for user {user_id} (has {child_count} children)"`.

## Tombstone Deletion

Removed nodes are tombstoned: cleared to `Uuid::nil()` with empty fields, then added to the free list. The slot is reused by the next `add_root` or `add_node`.

Clear the slot on removal. This releases heap allocations (the children Vec) and marks the slot as dead so stale data is never accidentally read.

Removal is leaf-only. Removing a node with children returns an error. The caller must remove children first, working from leaves up.

## Testing Conventions

### Unit tests

One logical assertion per test. Build small trees by hand. Test names follow `method_behavior` convention: `add_root_to_empty_tree`, `remove_node_with_children_fails`.

### Test helpers

`test_uuid(n: u8)` generates deterministic UUIDs for small tests. `test_uuid_u16(n: u16)` handles tests exceeding 255 nodes. Shared test helpers live in `tree/test_helpers.rs` and are used by all tree types and property tests.

### Property-based tests (proptest)

Every tree type must have these six property tests:

1. **Parent-child consistency.** Bidirectional: every child's parent points back, every parent's children list contains the child.
2. **Depth consistency.** Every node's depth equals parent's depth + 1. Root has depth 0.
3. **Upline completeness.** `get_upline(node, 0)` returns exactly `depth` nodes and ends at root.
4. **Downline containment.** Every node in `get_downline(user, 0)` satisfies `is_descendant_of`.
5. **Count matches collection.** `count_downline` equals `get_downline.len()` for any depth.
6. **Branch partitioning.** Union of all branches equals full downline. No duplicates, no missing nodes.

Use `build_random_tree` with randomized parent selection to generate arbitrary tree shapes. Reduce `node_count` range for O(n^2) properties.

Tree-type-specific invariants go on top of these six. Binary: max 2 children per node. Matrix: fixed width constraint.

### Edge cases

Every tree type must test:

- Empty tree (no root)
- Single-node tree
- Deep chain (1000 nodes)
- Wide fan (1000 children under one node)

These catch stack overflow, off-by-one, and performance issues that unit tests miss.

### Proptest regression files and vacuity checks

Proptest writes a `.proptest-regressions` seed file next to a test the first time a case fails. The project checks the legitimate ones in (see `binary_commission_properties.proptest-regressions`) so saved edge cases re-run for everyone.

Two gotchas show up when hardening these tests. A vacuity check deliberately breaks an assertion to confirm it fails, then reverts. That leaves a stray regression file for the artificial failure. Delete it before committing. Scope the delete to the exact file. A `rm tests/*.proptest-regressions` glob also wipes the checked-in ones. Separately, the `FileFailurePersistence::SourceParallel set, but failed to find lib.rs or main.rs` line during integration-test runs is benign. Proptest just cannot locate the crate root from the test binary.

### Completeness gate for commission ops

`every_structure_type_has_a_dispatchable_commission_op` (in `network-engine-worker/tests/worker_integration.rs`) stops a new plan type from shipping with an orphaned calculator. It has two halves. `commission_op` is a wildcard-free `match` over every `StructureConfig` variant. Adding a variant without an arm fails to compile the test crate, which forces you to name the op. A runtime loop then sends each named op to a real worker and asserts it never returns `UNKNOWN_OP`, confirming the op is wired into dispatch.

The two halves are not auto-synced. The runtime op list and `EXPECTED_OPS` are hand-maintained, so an op named in the match but omitted from the list slips the count assert. The backstop is the per-op integration test every real calculator gets. The gate also depends on `StructureConfig` staying exhaustive. If it becomes `#[non_exhaustive]`, the match would need a `_` arm and the compile-time forcing function breaks silently.

When you add a plan type: add its `StructureConfig` arm to `commission_op`, add the same op string to the runtime list, and bump `EXPECTED_OPS`.

## Shared Walk Module

Commission calculators that use level-based walks (unilevel, matrix, stairstep Walk 1, streamline, and generation) delegate to `commission/walk.rs`. The walk is generic over `TreeNavigator`. Plan-specific behavior is injected via `LevelWalkConfig` (e.g., matrix height ceiling) and the `should_stop` callback (e.g., stairstep breakaway boundaries). Binary uses pairing mechanics and does not use this module.

The walk function does not sort its output. Callers sort after combining results from multiple walk phases (stairstep combines Walk 1 and Walk 2 before sorting).

Stairstep calls the walk once per volume source rather than passing the full slice. This is because the `should_stop` closure captures a per-source group leader for breakaway boundary detection.

### Pass-up testing gotcha

Pass-up is plan-level config (same count for every distributor). In a linear chain tree, each node sponsors exactly one child, so that child is always passed up. This causes cascading skips where nearly every node is skipped. Use wider tree shapes (multiple children per node, with buffer nodes) when testing pass-up to avoid this artifact. Chain topologies are valid for property tests that verify invariants but not for integration tests that assert specific earning patterns.

## Per-Earner Thresholds: Filter Before Emit

Some plans configure per-earner thresholds on a shared walk (e.g., per-rank generation depth: silver caps at 2 generations, diamond caps at 7). Repeating the walk per earner is wasteful. The pattern is:

1. Walk once to the deepest configured cap (`walk_depth = max(default, max(per_rank_values))`)
2. Filter the emitted entries against each earner's own cap before commission emission

The filter sits between the walk primitive and `emit_*_earnings`. It looks up the earner's rank in the snapshot map, resolves the cap via a small helper (`earner_max_generations(rank, cfg)`), and admits entries where `entry.generation <= cap`. Entries for earners not present in the snapshot map should never appear in practice because the boundary set is built from snapshot keys; defensive `unwrap_or(default)` is fine.

Examples in `commission/generation.rs`:
- ThresholdRank source loop: shared walk + per-earner filter (HEU-425)
- SameRank per-rank-ordinal loop: each walk uses `earner_max_generations(rank_name, cfg)` directly because the walk is already partitioned by rank; no separate filter needed

When boundary mode partitions the walk by rank (SameRank), prefer threading the per-earner cap into the walk's termination value. When the walk is shared (ThresholdRank), filter after the walk. Don't reach for the walk primitive's signature — the constraint is at the call site.

## Board Plan Engine

The board plan engine (`board_plan/engine.rs`) manages multiple small boards. It is NOT a tree type. It doesn't use Arena or implement TreeNavigator. The `TreeInstance::BoardPlan` variant returns `None` from `as_navigator()`. Query handlers that call `as_navigator()` must handle the `None` case.

Board dimensions are capped: width 2-5, height 1-4. Boards use flat BFS-ordered position arrays, not arena storage.

The displaced member pool (`displaced_members`) holds members removed via dissolution or cycling without re-entry. They are placed before new enrollees in `add_member`. The `place_displaced_members` method must push unplaced members back to the pool if no boards have openings. Never propagate errors that would drop them.

All tree-layer types (including BoardPlanEngine) must remain serde-serializable. See design-rationale 023.

## Timestamps

`enrolled_at` is Unix seconds (i64) throughout the engine. Not milliseconds, not nanoseconds.

It is also load-bearing, not just descriptive. `MatrixTree` picks the promotion target on node removal with `min_by_key(enrolled_at)`, so the value decides *who moves up* when someone leaves. Anything that rebuilds a tree — reload, snapshot restore, migration — must carry `enrolled_at` through unchanged. Lose it or shift it and the tree still looks structurally correct, then promotes the wrong distributor on the next removal, with commission consequences and no error anywhere.

Assert it explicitly in round-trip tests. HEU-534 shipped a matrix reload where the value travelled correctly but nothing checked it; zeroing it during replay left the entire Go package green.

## Rank Evaluation: Empty `qualification.structures`

The `evaluate_ranks` worker handler (`engine/network-engine-worker/src/handlers/rank.rs`) only registers tree navigators for structures referenced by at least one rank's `qualification.structures`. An empty list yields an empty navigator map, `evaluation_order_for_users` returns no users, and the result is `{"ranks": {}}`.

This trips up tests and ad-hoc verification that load a plan whose ranks have empty qualification (e.g., the existing `testPlanJSON` constant in Go integration tests). Workaround for tests: add at least one rank whose `qualification.structures` references a tree the test creates. The Task 23 integration test introduced a local `rankIntegrationPlanJSON` fixture for exactly this reason.

A possible follow-up is to register every loaded structure in the navigator map, not just those referenced by qualifications, so empty-qualification ranks behave as "always pass." That requires product-team alignment on the intended semantics — the current behavior is a defensible reading too.

## Contract-Test Harness: `setup_raw` for Adjacent-Tagged Enums

`engine/network-engine-worker/tests/contract_tests.rs` and `internal/networkengine/contract_test.go` round-trip fixture setup steps through `serde_json::Value` / `map[string]any`. Both serializers re-emit JSON object keys in alphabetical order. This breaks deserialization of adjacent-tagged enums whose content carries non-string map keys.

Concrete case: `StructureConfig` uses `#[serde(tag = "type", content = "config")]`. The `Unilevel` variant's content includes `rate_table: BTreeMap<u8, f64>`. After alphabetical sort, `"config"` precedes `"type"`, and serde fails the deserialize because it sees the rate-table content before knowing the variant.

Fixtures that load plans (or any other adjacent-tagged enum with non-string-keyed content) must use the `setup_raw: ["..."]` field instead of `setup: [{...}]`. The harness sends `setup_raw` strings verbatim as NDJSON, bypassing the `Value` round-trip. Both `setup` and `setup_raw` are mutually exclusive per fixture (the harness asserts).

Pattern: `request_raw` exists for the same reason on the request side. If you find yourself adding a new escape hatch for `params`, follow that precedent.

## Contract-Test Harness: Result Numbers Must Match serde's Output

The Rust contract test asserts the whole `result` subtree with `assert_eq!` on `serde_json::Value`. That comparison treats integers and floats as different types: `Number::Float(500.0) != Number::PosInt(500)`. So a fixture's `expected_response.result` must use the same numeric form the worker emits.

- `f64` fields carry a decimal: `dollar_amount: 500.0`, `rate: 0.1`, `left: 0.0`. Never `500` or `0`.
- Integer fields (`u8`/`u32`) are bare: `level: 1`, `cycle_number: 1`, and map values like `updated_cycle_counts`. Never `1.0`.

The Go harness uses `assert.JSONEq`, which coerces every JSON number to `float64`. It will not catch an int/float mismatch. Only the Rust side will. Match serde's output and both pass.

This tripped up every fixture in HEU-514. Any new commission-result fixture (HEU-397, HEU-528, HEU-529) will hit it too.

## Contract-Test Harness: No Per-Fixture Filter

`contract_tests.rs` holds exactly one test function, `contract_fixtures_match_worker_behavior`, which loops over every fixture in `engine/testdata/contracts/`. Fixture names are not test names.

Filtering by one — `cargo test --test contract_tests calculate_streamline` — matches zero tests, prints `0 passed; 1 filtered out`, and **exits 0**. That reads as a pass, which is worse than a failure.

Run it unfiltered. To confirm a specific fixture actually executed, add `-- --nocapture`: the harness prints `contract: <name> -- <description>` for each one.

HEU-583's plan specified the filtered form on three steps, including the two that changed the money path and the wire contract. Following it literally would have recorded "Expected: PASS" against a run that asserted nothing.

## Streamline: Rank Gates Qualification, Not Rate

`calculate_streamline` builds its rate table so every plan rank maps to the *same* per-level percents (`commission/streamline.rs`). Rank does not change what a level pays. It only decides whether a distributor clears that level's `min_rank` threshold, through the dynamic-compression check in `walk.rs`.

This is the opposite of unilevel and matrix, where `rate_table` is keyed by rank and a higher rank earns a higher percentage. A test that raises a snapshot's rank on streamline expecting a bigger payout will see no change — the distributor either qualifies at that level or is skipped entirely.

## Qualification History Persistence

`evaluate_ranks` is stateless in the Rust engine. Per-period rank results are
persisted on the Go side via `QualificationHistoryStore`, behind the opt-in
`WithPersistence(periodID, store)` option on `EngineClient.EvaluateRanks`.

### Write semantics

`SaveResult(ctx, periodID, entries)` replaces the period completely. The
Postgres implementation runs `DELETE FROM qualification_history WHERE
period_id = $1` followed by `pgx.CopyFrom` inside one transaction. This is
the only correct pattern: per-row UPSERT cannot remove users dropped from a
re-evaluation, breaking BR5 from the design.

### period_id contract

The store treats `period_id` as opaque and orders rows lexicographically.
Callers must zero-pad so the strings sort correctly: `"2026-05"`,
`"2026-W21"`. Mixed widths like `"2026-1"` / `"2026-10"` / `"2026-2"` sort
incorrectly. A unit test in `qualification_history_store_memory_test.go`
documents the failure mode.

### Read semantics

- `GetByPeriod(periodID)` — ordered by `user_id` ASC. PK serves this query
  directly.
- `GetByUserAndPeriodRange(userID, fromPeriod, toPeriod)` — inclusive on
  both ends, ordered by `period_id` ASC. The `(user_id, period_id)`
  secondary index serves this as a leftmost-prefix range scan.
- `GetByUsersAndPeriodRange(userIDs, fromPeriod, toPeriod)` — the batched
  form of the above: one bounded read for a set of distributors over a
  period range, inclusive on both ends. Ordered by `period_id` ASC, then
  `user_id` ASC. The same `(user_id, period_id)` index serves it
  (`user_id = ANY(...)` plus range). An empty `userIDs` or
  `fromPeriod > toPeriod` returns no rows. Missing `(user, period)` pairs
  are omitted, never synthesized as Unranked (BR7). `BuildHistoryWindow`
  uses this as the single bounded read that replaced the per-period
  `GetByPeriod` fan-out (HEU-516).

Missing rows mean "not evaluated for this period." Rows with `rank IS NULL`
mean "evaluated, did not qualify" (Unranked). HEU-446 predicates rely on
this distinction.

### Error contract on the engine client

`EvaluateRanks` returns the engine result and a wrapped store error when
the worker call succeeded but the store write failed. NFR4 guarantees no
half-replaced data — prior period rows are intact. The caller can retry
the store write or surface the failure.

## Period ID Labels (`internal/period`)

`internal/period` turns a plan's `PeriodConfig` (length + `start_date`) into
ordered, lexicographically sortable `period_id` labels. It is pure: no clock, no
I/O. The periodic `RankDriver` (use-case UC-NET-010) uses it to size and build
the prior-period axis.

All math is date-only and civil-date-anchored (BR10): every `time.Time` reduces
to its own calendar Y/M/D (in the value's location) at UTC midnight before any
arithmetic. The caller's civil date is authoritative, not the UTC-shifted
instant, so labels are stable across timezone and DST. A `2026-05-15T23:30-05:00`
input labels as May 15, not the UTC-shifted May 16.

Label formats, all zero-padded so the strings sort chronologically: month
`2006-01`, quarter `YYYY-QN`, semi-month `YYYY-MM-H{1,2}` (H1 = days 1-15,
H2 = 16-end), week `ISOyear-Www`.

Non-obvious: a **week `period_id` is the ISO week of the anchor-aligned 7-day
bucket start**, which can differ from the input date's own ISO week. Weeks are
7-day buckets off the plan's `start_date` grid, not calendar Mon-Sun weeks, and
the label is `bucketStart.ISOWeek()`. Consecutive buckets still map to
consecutive ISO weeks (7 days shifts the weekday by zero and the ISO week by
one), so labels stay sortable and unique. `TestLabelSortable` verifies this
across 53-week ISO years. Do not "fix" the label to use the input date's ISO
week.

## `evaluate_ranks` Request: Nil Maps/Slices Must Marshal as Empty, Not Null

The `EvaluateRanksRequest` DTO (`internal/networkengine/wire_types.go`) mirrors
the Rust `EvaluationInputs`. Three fields have **no** `omitempty` on the Go side
and **no** `#[serde(default)]` on the Rust side: `distributors` (map),
`volume_sources` (slice), and each distributor's `active_products` (slice). A nil
Go map or slice marshals to JSON `null`, and the Rust worker then fails to
deserialize the whole request.

So any code that builds an `EvaluateRanksRequest` must normalize nil to empty
(`{}` / `[]`) before the call. `RankDriver.EvaluatePeriod` does this at all three
levels, and copies the distributors map first so it never mutates the provider's
stored input. A real `PeriodInputProvider` (HEU-505) must hand over empties, not
nils, or rely on the driver's normalization.

Contrast: `history_window` and `history` **do** carry `omitempty` +
`serde(default)`, so a no-gate plan omits them entirely. That is correct, not a
bug.

Pin the wire shape with raw-byte assertions, because unmarshaling into the Go
struct collapses `null`, `[]`, and omitted into the same empty value. Assert the
literal bytes: `"active_products":[]` is present and `:null` is absent (see
`rank_driver_test.go`).

## Tree Reload: What `LoadTree` Restores, and What It Does Not

`LoadTree` rebuilds a tree in the engine from the `tree_nodes` adjacency rows. The invariants below are not obvious from the code and matter to anyone touching the loader, the projection, or the worker's tree handlers.

### A root's stored sponsor is dropped on reload

`AddRoot` takes no sponsor and the engine root carries `sponsor: None`. So whatever `sponsor_id` a depth-0 row holds is projection metadata, not a replay input, and reload discards it. Round-tripping a tree therefore does not preserve the root's stored sponsor.

Preflight deliberately exempts that field from every check a non-root gets: no nil check, no self-reference check, no existence check. Validating it would reject rows the engine can restore perfectly well, and treating it as a replay dependency makes the root wait on a node downstream of itself, which rejects the whole tree as a cycle.

Root **parent** and **position** are the opposite: `AddRoot` accepts neither, so a depth-0 row carrying either describes a topology the engine cannot reproduce, and preflight rejects it.

### Replay order must satisfy sponsor edges, not just parent edges

Every tree type resolves the sponsor at insert time and rejects one that is not present yet (`unilevel.rs`, `binary.rs`, `matrix.rs`). Depth order satisfies parents — a parent is always shallower — but says nothing about sponsors.

Automatic spillover always places a recruit below their sponsor, so depth order happened to work. Explicit placement removes that coincidence: an admin override can put a node at depth 1 whose sponsor sits at depth 10, and depth-ordered replay then fails mid-tree.

The error code differs by type, which matters when grepping logs. Only `MatrixTree` remaps the failure to `SponsorNotFound` → `SPONSOR_NOT_FOUND`. `UnilevelTree` and `BinaryTree` let the bare resolve error through, so they report `USER_NOT_FOUND` for the same fault.

`orderForReplay` topologically sorts on both edges. Do not replace it with a depth sort.

### Matrix and binary require a position on every non-root row

Matrix reload replays through `add_node_at`, which takes an explicit slot. Binary replay passes `position` through `add_node`. Both reject a nil position at preflight rather than defaulting to 0, because a silent 0 places the node in the wrong slot and the divergence is invisible afterwards.

Unilevel carries no position; it appends to the parent's child list. A unilevel row that *has* a position is currently tolerated and then silently dropped by the worker — see HEU-563, which argues it should be rejected for symmetry with the root rule.

### Why preflight validates everything before mutating anything

The worker has **no operation to remove a structure** (HEU-557). A load that fails partway leaves the tree stuck until the process restarts. So `LoadTree` proves a load is sound before the first engine call, and its replay failures report how far they got, because that count is the only recovery signal an operator has.

Preflight runs in two phases with different inputs. `validateTreeConfig` checks the tree type, and for a matrix the width and spillover. It reads no rows, so it runs before the store query. A misconfigured load costs no query. `validateNodes` runs after the query and proves the node set is structurally consistent.

The empty-tree short circuit sits between the two phases. A tree with zero rows still reports configuration errors. That is why a typo in startup wiring surfaces at the first load instead of staying invisible until the first node arrives.

### Sponsored-list order is not restored

`Arena::get_sponsored` returns its vec in insertion order, and reload inserts in `(depth, enrolled_at, user_id)` order rather than original event order. So a reloaded tree can report a distributor's recruits in a different order than the live tree did — on the HEU-534 integration fixture, `u1.sponsored` comes back `[u2, u5, u3]` where the event sequence produced `[u2, u3, u5]`.

Harmless today: every other read of that vec is a `retain`, and the adjacency table stores no ordering to restore even if we wanted to. Do not build anything that depends on sponsored-list order surviving a restart.

### Matrix events carry authoritative placement (HEU-553)

`node_placed` events are the single statement of placement. The payload requires `tree_type`. Matrix and binary events require an explicit `position`. Unilevel events must omit it. The consumer rejects an event that fails a payload check before either projection, so those rejections land nowhere. The rejected payload classes: wrong stream, unknown type, missing position, negative position, matrix position above the u8 ceiling (255), binary position outside 0..1, unilevel position present. Matrix placements project through `add_node_at`, so the engine applies exactly the stored parent and position. A partial unique index (migration 000004) makes a double-claimed slot fail at the insert instead of poisoning reload.

Four limits remain:

- The consumer trusts the `tree_type` label. No registry exists to verify it against.
- The gate rejects matrix positions above the u8 ceiling (255), which no width can accept. The real bound is the tree's width, which nothing persists. A position in the width..255 band is therefore stored, refused loudly by the engine, and then makes the next reload preflight reject the whole tree. HEU-554 decides the direction for both gaps. The fix ships under it.
- Redelivering an already-stored event fails at the insert instead of converging (HEU-576).
- The agreement claim covers placement only. A matrix `node_removed` still diverges, because the consumer sends no pruning mode and the worker refuses the removal after the soft-delete lands (HEU-582).

Matrix startup reload is no longer blocked by this defect.

### Postgres index order is a redelivery discriminator

`tree_nodes.id` is the event ID, and the primary key is declared inline in `CREATE TABLE`, so it carries the lowest OID and Postgres checks it before either named partial index. The failure mode therefore identifies the cause. A `tree_nodes_pkey` violation means this exact event was already stored. It does not prove the engine applied it. The store insert lands before the engine call, so a delivery that failed at the engine leaves the row behind (HEU-576). An `idx_tree_nodes_tree_user` violation means a different event claims the same active user, which is real corruption. HEU-576's idempotent redelivery leans on this distinction. `MemoryTreeStore.InsertNode` mirrors the same check order, so the discriminator holds against the test double too. `TestTreePersistence_DuplicateDeliveryPinsNonIdempotence` pins today's behavior and flips to clean success when HEU-576 lands.
