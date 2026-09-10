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

### Go error shapes: sentinel or struct

The Go side splits on one question. Does the condition carry a value a caller
would act on?

| Carries | Shape | Matched with |
|---|---|---|
| nothing to act on | `var ErrX = errors.New(...)` | `errors.Is` |
| a value to act on | `type XError struct { ... }`, pointer receiver on `Error()` | `errors.As` |

`ErrTransportClosed`, `ErrWorkerNotExited` and `ErrWorkerUnreaped` are the first
kind. `LiveRunExistsError`, `RunNotFoundError` and `RunNotRunningError` are the
second, and the first of those names its field as the thing that makes the
condition recoverable.

A diagnostic string is not a value to act on. The two protocol-version sentinels
are fixed strings; the call site wraps each one in a message that quotes the
offending wire payload. They are still sentinels, because nothing branches on
that payload.

Wrap with `%w` rather than returning the bare sentinel, so the message keeps its
context while `errors.Is` still reaches the sentinel.

Prefixes group by subject, not by file. `ErrWorker*` is the subprocess: did it
exit, was it reaped. `ErrProtocolVersion*` is the handshake: what does it claim
to speak.

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

The walk function does not sort its output, and since HEU-641 the five commission calculators do not sort either. All five hand their earnings and walks to `walk_order::assemble`, which orders the walks, remaps each earning's collector id to a final walk index, and only then calls `sort_earnings`. Binary and board plan do not go through it. They return their own result types and never produce a walk. Stairstep still combines Walk 1 and Walk 2 before handing them over.

The walk takes a `&mut Vec<Walk>` collector as its last parameter and pushes one walk record per volume source as it goes. It records one step per node at each of the four sites that consume a level, and a stop on every walk, naming the break site when one fired.

Four consuming sites, five `steps.push` calls. The loop tail is one site with two mutually exclusive branches: a node with a rate is `Paid`, and a node whose rate table has no entry at its level falls back to 0.0 and is `Forfeited`. Both consume the level, so one node yields one step either way. Counting the pushes instead of the sites gives five and is the wrong number, which has now caught two readers.

The `index` on a pushed walk is a collector id, not a position. `walk_order::assemble` replaces it with the real index and remaps the earnings that reference it.

Every caller of this walk passes a real collector. The throwaway-collector pattern belongs to the generation traversal in `generation.rs`, which has an uninstrumented `count_generations_upward` wrapper for stairstep Walk 2. Those earnings carry `walk: null`. That gap is deliberate, not a miss: design-rationale 029 excludes that traversal.

Stairstep calls the walk once per volume source rather than passing the full slice. This is because the `should_stop` closure captures a per-source group leader for breakaway boundary detection.

### A counter compared against a `uN` bound must be wider than `uN`

`walk_level_commissions` counted levels in `u8` and broke on
`level > config.max_depth`, where `max_depth` is also `u8`. At `max_depth: 255`
the counter saturates at 255, `255 > 255` is false, and the break never fires —
every ancestor past position 255 keeps earning at level 255. The bug is
unreachable for every other value, so it hides behind a green suite.

The fix is to widen the counter, not to bound the config: the JSON schema
publishes `"maximum": 255` at every `commissionable_depth` site, so a 254 ceiling
would tighten a published contract in three layers. The walk now counts in `u16`
and narrows back to `u8` once, immediately below the break, where
`level <= max_depth <= u8::MAX` holds by construction. `CommissionEarning.level`
stays `u8`, so nothing changes on the wire.

Same shape to watch for elsewhere: `GenerationCommissionConfig.max_generations`
and `MatrixStructureConfig.height` are unbounded `u8` config fields.
`count_generations_upward` happens to be safe because it checks before
incrementing, but the pattern is worth recognising. When a loop counter is
compared against a config bound, the counter needs headroom above the bound's
maximum or the comparison is unreachable there. (HEU-612)

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

## Contract-Test Harness: `setup_raw` for Byte-Sensitive Fixtures

`engine/network-engine-worker/tests/contract_tests.rs` and `internal/networkengine/contract_test.go` round-trip fixture setup steps through `serde_json::Value` / `map[string]any`, which sorts object keys. Go's `json.Marshal` always sorts, and the Rust side sorts too since `serde_json::Value` is a `BTreeMap`.

**Use `setup_raw` when the fixture needs to control its own bytes.** Malformed JSON, duplicate keys, or a specific key order the assertion depends on — none of those survive a round-trip through a map. `setup_raw` lines are sent verbatim as NDJSON. `setup` and `setup_raw` are mutually exclusive per fixture, and the harness asserts it.

**It is no longer required just because a fixture loads a plan.** That rule existed because sorting puts `config` before `type` in an adjacently-tagged `StructureConfig`, which made serde buffer the content, which stripped the string-to-`u8` coercion that `rate_table: BTreeMap<u8, f64>` needs. Loading a plan through `setup` failed outright. HEU-648 fixed that with `deserialize_with` helpers in `engine/network-engine/src/serde_helpers.rs`, so either key order parses now.

Existing plan fixtures still use `setup_raw`. Leave them: changing a fixture's encoding path changes the bytes the worker sees, which is not a change worth making without a reason.

Pattern: `request_raw` exists for the same reason on the request side. If you find yourself adding a new escape hatch for `params`, follow that precedent.

## Contract-Test Harness: Result Numbers Must Match serde's Output

The Rust contract test asserts the whole `result` subtree with `assert_eq!` on `serde_json::Value`. That comparison treats integers and floats as different types: `Number::Float(500.0) != Number::PosInt(500)`. So a fixture's `expected_response.result` must use the same numeric form the worker emits.

- `f64` fields carry a decimal: `dollar_amount: 500.0`, `rate: 0.1`, `left: 0.0`. Never `500` or `0`.
- Integer fields (`u8`/`u32`) are bare: `level: 1`, `walk: 0`, `cycle_number: 1`, and map values like `updated_cycle_counts`. Never `1.0`. A null `walk` is `null`, which sidesteps the trap entirely; a recorded one does not.

The Go harness uses `assert.JSONEq`, which coerces every JSON number to `float64`. It will not catch an int/float mismatch. Only the Rust side will. Match serde's output and both pass.

This tripped up every fixture in HEU-514. Any new commission-result fixture (HEU-397, HEU-528, HEU-529) will hit it too.

## Contract-Test Harness: No Per-Fixture Filter

`contract_tests.rs` holds exactly one test function, `contract_fixtures_match_worker_behavior`, which loops over every fixture in `engine/testdata/contracts/`. Fixture names are not test names.

Filtering by one — `cargo test --test contract_tests calculate_streamline` — matches zero tests, prints `0 passed; 1 filtered out`, and **exits 0**. That reads as a pass, which is worse than a failure.

Run it unfiltered. To confirm a specific fixture actually executed, add `-- --nocapture`: the harness prints `contract: <name> -- <description>` for each one.

That line is indented two spaces, so `grep '^contract: '` matches nothing and exits nonzero **with no test failure** — the same reads-as-a-pass trap in a different disguise. Grep without the anchor.

HEU-583's plan specified the filtered form on three steps, including the two that changed the money path and the wire contract. Following it literally would have recorded "Expected: PASS" against a run that asserted nothing.

## A Determinism Test Only Bites On Streamline

`walk_order::assign_indexes` sorts a response's walks into a total order, but
for four of the five calculators that sort is a no-op. They already emit in the
order it produces.

| Calculator | Emission order | Sort is |
| -- | -- | -- |
| `calculate_unilevel` | one `walk_level_commissions` call over the whole `volume` slice | a no-op |
| `calculate_matrix` | same | a no-op |
| `calculate_stairstep` | `for source in volume` | a no-op |
| `calculate_generation` SameRank | rank-outer over `unique_ranks`, sorted by ordinal first, then `for source in volume` | a no-op |
| `calculate_streamline` | `for stream in engine.active_streams()`, a `HashMap` values iteration | **load-bearing** |

So a "two runs produce identical indexes" property written against unilevel or
generation cannot fail. Deleting the sort outright leaves it green. Write that
property against streamline, in `tests/streamline_properties.rs`.

Two more traps in it. Build **two** engines rather than running one twice: a
single `HashMap` repeats its own iteration order, so the one-engine version is
vacuous. And two separately built maps get different `RandomState` keys but not
necessarily a different order for a small table, so `prop_assume!` that the two
emission orders actually differ, or roughly half the small cases prove nothing.

Verified by deleting the sort and watching which tests failed. (HEU-641)

## sha2 0.11 Returns An Array With No `LowerHex`

`Sha256::digest` in sha2 0.11 returns a `hybrid_array::Array`, not something
that implements `LowerHex`. The 0.10 idiom does not compile:

```rust
format!("{:x}", Sha256::digest(bytes))   // does not compile on 0.11
```

Format a byte at a time instead. `engine/network-engine-worker/src/handlers/common.rs`
has the working shape.

The dependency is declared `default-features = false`, which turns off the
`oid` feature. That costs 7 new lockfile entries: `sha2`, `digest`,
`crypto-common`, `block-buffer`, `hybrid-array`, `typenum` and `cpufeatures`.
Leaving defaults on additionally pulls `const-oid`, which nothing here needs.
(HEU-641)

## Rust Tests: Package Scope Used To Lie (fixed, HEU-648)

`cargo test -p network-engine-worker` is safe now. It was not before 2026-08-22, and the history is worth keeping because the failure mode was so convincing.

`network-engine` used to enable `serde_json/preserve_order` as a dev-dependency. `network-engine-worker`'s tests inherited it through Cargo feature unification, which needs both crates in the same build. Scoping to one crate dropped the feature, `serde_json::Value` reverted to sorted keys, and any test that round-tripped a plan through `Value` broke on the adjacently-tagged `StructureConfig` with `invalid type: string "1", expected u8`. It looked like a real deserialize bug. It was a build-scope artifact, and it nearly produced a false "main is red" report during HEU-603.

HEU-648 removed the feature. The integer-keyed config maps now parse their keys on either path, so key order stopped mattering and nothing needs `preserve_order` to hold. All build configurations link the same `serde_json`: `cargo tree -i serde_json` reports `default, raw_value, std` under `-e normal,features --workspace`, `-e all --workspace`, and `-e all -p network-engine-worker` alike.

Two rules survive, for different reasons:

- **Run the full suite anyway.** It takes about 1.5 seconds, so scoping buys nothing, and a per-package run is one more thing to get wrong.
- **Lint with `cargo clippy --all-targets --workspace -- -D warnings`.** That is what CI runs (`.github/workflows/ci.yml`), and `--all-targets` is what catches lints in test code. HEU-560 added it.

### The production binary is no longer a third configuration

It used to be. `preserve_order` was dev-only, so `cargo build` never activated it and the shipped worker sorted keys while the wide test build did not. Nothing in the test suite exercised the bytes production emitted.

That is closed. The same NDJSON request piped through a `cargo build -p network-engine-worker` binary and a `cargo build --workspace --all-targets` binary now produces byte-identical output. `network-engine-worker/src/protocol.rs` carries a regression test, `value_payload_emits_sorted_keys`, that fails if `preserve_order` ever re-enters the graph — though only at workspace scope, since a `-p` run would not pull the dev-dependency in either.

`handle_load_plan` still deserializes straight off the `RawValue` (`network-engine-worker/src/handlers/common.rs`) rather than through `Value`, which remains the right shape for a hot path. But routing plan-bearing params through `parse_params` is no longer a correctness hazard.

## Serde And Cargo Gotchas (HEU-648)

Four things that cost real time on HEU-648. All are live traps for the next
person, not history.

### `Option<T>` plus `deserialize_with` silently makes a field required

Serde reads a bare `Option<T>` field as `None` when the key is absent. Adding
`deserialize_with` **disables that**, and the field starts erroring on absence.
Measured:

| Field shape | Absent key |
|---|---|
| `Option<T>` | `Ok(None)` |
| `Option<T>` + `deserialize_with` | `Err("missing field")` |
| `Option<T>` + `default` + `deserialize_with` | `Ok(None)` |

Always pair the two. `config/bonus.rs`'s `decreasing_rates` shows the shape.
Getting this wrong is a silent wire-contract break: every payload omitting the
field starts failing, and no existing test necessarily covers absence.

### `from_value` coerces integer map keys; serde's `Content` buffer does not

`serde_json::from_value::<T>(v)` deserializes integer map keys fine, because
serde_json's own map-key deserializer coerces numeric strings. Serde's `Content`
buffer — reached through a tagged enum whose content arrives before its tag —
does not.

The consequence for tests: **a test that uses `from_value` to "exercise the
buffered path" is not exercising it.** It passes against code that has no key
handling at all. To reach the buffer you need a tagged enum with the content key
emitted first. `config/bonus.rs`'s `BufferProbe` test enum does exactly that.

### `pub(crate)` helpers used only from `#[cfg(test)]` fail `clippy --all-targets`

A helper introduced in one commit and first used in the next is dead code in
between, and `cargo clippy --all-targets --workspace -- -D warnings` rejects it.
If your branch may not contain a red commit, a helper has to land in the same
commit as its first real caller. This reshapes commit boundaries, so plan for it
rather than discovering it at commit time.

### Cargo does not relink on a bare feature-graph change

Comparing what two build configurations emit — say `cargo build -p foo` against
`cargo build --workspace --all-targets` — requires `touch`ing a source file
between the builds. Otherwise the second build reuses the first artifact, the
outputs are trivially identical, and the comparison proves nothing. This one is
quiet: the check appears to pass.

## Worker Binary Freshness

The Go integration suite fails if the Rust worker binary is older than a `.rs` file the binary is built from (`staleWorkerBinary` in `transport_test.go`, HEU-615). A stale binary used to be picked up silently, so a green `go test ./...` could mean the Rust half of the seam was never rebuilt.

The walk skips two things it cannot be stale against.

`engine/target/` holds sources cargo regenerates during the same build that produces the binary. Counting them would report the binary as stale against its own output.

`<crate>/tests/` holds Rust integration tests. Cargo compiles those as separate crates linked into their own test binaries, never into `network-engine-worker`. Counting them produced a failure that no rebuild could clear, because cargo had nothing to relink and the binary's mtime never moved (HEU-634).

The exclusion is anchored to the crate root, meaning a `tests` directory sitting beside a `Cargo.toml`. A `tests` directory under `src/` is a unit-test module, declared with `mod tests;` and compiled into the crate, so it stays in the walk. Matching on the directory name alone would skip it and silently re-open the false green the guard exists to close.

When the guard fires it names the offending file. Rebuild:

```bash
(cd engine && cargo build --workspace)
```

Do not `touch` the binary itself. That clears the guard without rebuilding, which is exactly the stale-binary state HEU-615 exists to catch.

## An Absent Worker Binary Fails In CI And Skips Locally

`findWorkerBinaryAt` skips when the worker binary is missing, which is what lets
`go test ./...` work on a machine with no Rust toolchain. When `CI` is set it
fails instead. Tests reach it through the `findWorkerBinary` wrapper.

The reason is that `go test` counts a skipped test as a success. CI once ran the
whole Go suite with no worker binary present and reported green while every test
that needed a live worker did nothing (HEU-660). The Go job now builds the
worker before the suite runs, and the guard is what tells us if that build ever
comes out again.

If you meet this failure locally, something in your environment is setting `CI`.
The message names the path it checked and the value it read.

## The Freshness Verdict Is Cached Per Instance, Not Per Package

`workerFreshness` caches one staleness answer so the source walk runs once
rather than on every call. The verdict belongs to the binary and source root it
was computed from.

This used to be three package-level variables behind a `sync.Once`, which meant
the first caller to reach it fixed the answer for every later caller no matter
what they asked about. That is fine while one pair of paths is the only pair in
play, and it broke the moment a test passed a different pair: the cached "not
stale" verdict from a temp directory answered the real tree's question, and
HEU-615's guard silently stopped firing. Found in review during HEU-660.

If you need a verdict about a different binary or source root, construct your
own `workerFreshness`. Do not reach for the package one.

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

## Nil Go Collections Marshal as `null`

This bites every request DTO with a map or slice, not one handler. Read it
before adding a handler that takes a collection param.

A nil Go map or slice marshals to JSON `null`, not `{}` or `[]`. On the Rust
side `#[serde(default)]` covers an **absent** key; it does not cover a key
present with a null value. So a caller that leaves a collection unset sends the
one shape neither serde path accepts, and the whole request dies with
`INVALID_PARAMS`.

The trap is that the failing call is usually the *natural* one — a first period
with no prior counts, a period with no volume events, a plan with no history.

### Two fixes, and when each applies

**Engine-side null tolerance** is the default. The worker treats request params
as unvalidated input (design rationale 028), so it should not depend on one
client's marshalling. Use the `network_engine::serde_helpers::null_as_empty`
helper:

| The field is | Attribute | Absent | Null |
|---|---|---|---|
| Required | `#[serde(deserialize_with = "null_as_empty")]` | error | empty |
| Optional | `#[serde(default, deserialize_with = "null_as_empty")]` | empty | empty |

Do **not** reach for Go's `omitempty` on a required field. On its own it breaks
the call: when the collection is empty the key vanishes, the required attribute
has no `default` to fall back on, and the caller gets `INVALID_PARAMS`. The real
trap is what comes next — add `serde(default)` to make it work again and a
dropped field becomes indistinguishable from an empty one, which on a money path
pays zero instead of complaining. Keep required fields null-tolerant and nothing
more.

**Caller-side normalization** is the older approach, still used by
`RankDriver.EvaluatePeriod` for `evaluate_ranks`. It normalizes nil to empty at
all three levels before the call, and copies the distributors map first so it
never mutates the provider's stored input. On its own it only binds callers you
control, so on a field without the engine-side fix a non-Go client sending null
still fails. `evaluate_ranks` now has both, which makes the normalization belt
and braces there. Prefer the engine-side fix for anything new.

Go's `omitempty` is a complement to either, not a fix on its own — it keeps the
bad shape off the wire, but on a field without the engine-side fix the worker
still rejects that shape from anyone else.

### Current state

Across all seven commission handlers (the six siblings plus
`board_calculate_commissions`) and `evaluate_ranks`, every top-level named
request collection is now null-tolerant (HEU-626). What differs between them is
only whether *absent* is also allowed. Nested and query-op collections are a
separate matter; HEU-632 tracks the three that remain, listed below.

- `board_calculate_commissions` — fixed both ways (HEU-603). `cycle_events` is
  required and null-tolerant; `period_cycle_counts` is optional, null-tolerant,
  and carries `omitempty` on the Go side.
- `evaluate_ranks` — `distributors`, `volume_sources`, and each distributor's
  `active_products` are required and null-tolerant. `RankDriver.EvaluatePeriod`
  still normalizes nil to empty before the call; that is now belt and braces
  rather than the thing keeping it working, and it only ever bound Go callers.
  A real `PeriodInputProvider` (HEU-505) may hand over nils safely.
- The six other commission handlers — `snapshots` and `volume` are required and
  null-tolerant; `carry_forward` is optional and no longer depends on Go's
  `omitempty` to stay correct. Binary pairing's `ownership` is left alone: it is
  `Option<HashMap<..>>`, so `Option` absorbs a null natively and it needed no
  help from this ticket.
- `history_window` and `history` are optional and null-tolerant, and keep
  `omitempty` + `serde(default)` so a no-gate plan omits them. Absent, null, and
  empty all mean "no history".

One helper, `network_engine::serde_helpers::null_as_empty`, backs all of it.
HEU-626 moved it out of `config` and deleted a second, independently written
copy that had grown in `handlers/board_plan.rs`. Note it widens null to
`T::default()` for any `T: Default` — on a collection that reads as "empty", but
on a numeric field it would silently produce `0`.

**One narrowing rode along with HEU-626.** The serde work only widens what the
worker accepts. `calculate_generation` is the exception: it now *rejects*
requests it used to answer with `Ok([])`.

- Volume naming a source with no entry in `snapshots` returns
  `SourceNotInSnapshot` (`CALCULATION_ERROR` on the wire). Before, a
  generation-only structure (`level_commissions_enabled: false`) paid nobody and
  reported success, because nothing on that path validated the sources —
  `walk_level_commissions` does it, and generation only reaches that walk when
  level commissions are on.
- When `boundary_rank` is missing from the plan's rank ladder, that arm returns
  early, above the per-source loop, so it used to skip validation entirely.
  Source validation is hoisted above the boundary logic, which closes it. All
  three checks now surface on this path, not just the snapshot one —
  `InvalidCvAmount` and `SourceNotInTree` existed before but sat below the
  early return.

Both were silent zeros on a money path, which is why they were worth closing
inside this ticket rather than after it. Callers that relied on the old lenient
answer will now see an error. Nothing calls `CalculateGeneration` outside tests
today, so the practical blast radius is zero — but HEU-556, HEU-46, and HEU-47
wire these methods up, and they should expect the strict behavior.

`walk::validate_source` owns the check order, and a caller that hands it the
source it is validating gets that order for free. Two calculators reproduce the
order themselves instead. Binary never reaches it: it resolves an owner before
the snapshot lookup, so it validates its own way. Streamline does reach it, once
per stream, but only after a filter has already dropped the sources its own
pre-loop exists to catch. HEU-611 added that pre-loop, described below.

**A second narrowing rode along with HEU-611.** `calculate_streamline` now
rejects requests it used to answer with `Ok([])`, for the same reason generation
did.

- Volume naming a source held by no stream returns `SourceNotInTree`. Before, the
  per-stream filter dropped it before any walk ran and the call reported success
  with empty earnings.
- The snapshot half is narrower than it looks. A source in an active stream with
  no snapshot already errored, because the filter keys on tree membership and the
  walk validated whatever survived it. What is new is `SourceNotInSnapshot` for a
  source no *active* stream holds, which no walk ever reached.
- The checks run in a pre-loop over the whole `volume` slice, ahead of the stream
  walks, because streamline has no single tree to hand `validate_source` — it has
  one tree per stream. That is why the CV/tree/snapshot order is written out a
  second time rather than delegated.
- A source held only by a *frozen* stream is deliberately not an error. Freezing
  is a business state and paying nothing is the right answer, so the source is
  accepted and earns nothing. What keeps that from being a silent zero is
  `frozen_stream_skips` on the *worker's* `calculate_streamline` response, not on
  `CommissionCalculationResult`: one record per (volume entry, frozen stream)
  pair, always present, ordered by volume index then stream id. A direct Rust
  caller of the engine function still gets the bare result.

Same blast radius as the generation narrowing: nothing calls `CalculateStreamline`
outside tests today.

**Still null-intolerant, tracked by HEU-632.** These are nested or query-op
collections the ticket deliberately stopped short of:

- `history`'s inner per-period map. `{"<uuid>": null}` has no defined meaning —
  absent-key and `Some(None)` are the two documented states, and a null inner
  map is neither. `evaluation_inputs_still_rejects_null_inner_history` pins the
  current behavior.
- `cycle_events[].new_boards`.
- `board_compress_inactive`'s `member_ids`.

### Testing it

Pin the wire shape with assertions on the raw bytes. Unmarshaling into the Go
struct collapses `null`, `[]`, and omitted into the same empty value, so a
round-trip test proves nothing. Assert the literal bytes — `"active_products":[]`
present and `:null` absent (`rank_driver_test.go`), or `assert.JSONEq` on the
whole param set (`TestEngineClient_CalculateBoardCommissions_NilCollections`).

Cover both halves on the Rust side too: one test that null is accepted, one that
absent still fails. See `board_calculate_accepts_null_collections` and
`board_calculate_still_requires_cycle_events`.

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

## Worker Shutdown

Three things about closing a worker that are not obvious from the code, all
found by review on HEU-640 after the first two attempts at the fix were wrong.

### A blocked reader wedges the worker

`readLoop` is the only reader of the worker's stdout and the only sender on the
`lines` channel, and that send blocks once the channel is full. Nothing drains
`lines` between the return of one `Call` and the start of the next.

So a worker that keeps writing after answering fills the channel, which stops
`readLoop` reading, which fills the OS stdout pipe, which blocks the worker in
`write`. A worker blocked in `write` never reaches its stdin read, so it never
sees the EOF that closing stdin sends it, and waiting for it to exit never
finishes.

The design note that draining stdout continuously keeps the worker from
blocking on a full pipe is true only while something is draining. Shutdown is
the gap.

### `WaitDelay` is not a deadline for `Wait`

`exec.Cmd.WaitDelay` bounds the wait for I/O to finish **after the process has
exited**, or after an attached `Context` is done. A command built without a
context and still running is bounded by neither: its timer has not started.

So `WaitDelay` does not rescue a wait on a process that will not die. It rescues
a wait on a process that has died while its output is still being copied. Those
are different failures and they need separate bounds.

### Nested deadlines must not share a constant (HEU-671)

Following from the above: the reap bound and `WaitDelay` are nested. `WaitDelay`
bounds the inner wait `os/exec` performs; the reap bound is how long we wait for
that to finish. They were the same 2-second constant, and the outer one
systematically preempted the inner one.

The values were equal but the start points were not. `killAndReap` arms its
timer immediately after the kill. `os/exec` arms `WaitDelay` only once
`Process.Wait` observes the exit, a few milliseconds later. That is not a fair
race, it is a loaded one, and it lands the same way nearly always: measured, the
reap arrived 50-150us after the reap bound had already fired, on essentially
every orphaned-child shutdown. `Close` reported a reap failure for a reap that
had happened.

Two things worth taking from it. Equal constants do not imply a symmetric race,
and the only thing that decides one is when each timer starts, which is not
visible from the constant names. And a race that looks 50/50 from the values can
be near-deterministic in practice, so "nondeterministic but both outcomes are
correct" is a claim to measure rather than reason about.

`reapBound` is now `waitIODelay + 500ms`, written as a delta so the ordering
survives someone retuning the inner bound.

One consequence for testing: once the reap bound outlasts `WaitDelay`, reaching
the unreaped path at all needs a worker that survives `SIGKILL`. No test can
arrange that portably, so the revision logic behind it is driven at the seam
rather than through a subprocess.

### An orphaned grandchild keeps stderr open

Stderr is copied into a buffer rather than being a file, so `os/exec` allocates
a pipe and a goroutine to copy it, and `Wait` does not return until that copy
sees EOF. EOF needs every write end closed.

Killing the worker does not close a write end that a child inherited and still
holds. A worker that spawns anything outliving it therefore keeps `Wait` blocked
after the kill, which is the case `WaitDelay` covers.

### Testing this

A worker script that emits a few hundred short lines proves nothing: it fits in
the pipe buffer, so the worker never blocks and the wedge never happens. The
payload has to exceed both the channel and the pipe.

Three lines here were each deletable with the whole suite green until tests were
written specifically to fail without them. A shutdown path that is only
exercised by well-behaved workers is not exercised at all.

## The Four Consuming Branches In The Level Walk

The Shared Walk Module section above says the walk records a step at each of the
four sites that consume a level, and why four is not five. This is what those
four are.

Anchored by content rather than by line number. These have rotted twice.

| Branch | Condition |
| -- | -- |
| `saturating_add` under the missing-snapshot arm | No snapshot, and no compression configured |
| `saturating_add` with the `// forfeit level` comment | Node is ineligible and was not compressed |
| `saturating_add` under the `max_earning_depth` check | Per-distributor depth cap from active leg tiers |
| the trailing `saturating_add` at the bottom of the node loop, **when `rate == 0.0`** | Eligible, uncompressed, but the rate table has no entry for this rank at this level |

**The fourth is the one that reads as safe, and the ticket, the plan and the
implementation brief all missed it.** All three counted three. The first three
`continue`, so they look like the exceptional paths. The fourth is the
unconditional increment every surviving node reaches, and it forfeits only when
the rate lookup falls back to `unwrap_or(0.0)`.

Missing it leaves `steps.len()` short by one for every zero-rate node. That
compiles, passes every existing fixture, and silently breaks the one property
walk records exist to provide.

It is reachable rather than theoretical. Any plan whose `max_depth` exceeds the
deepest level in its rate table hits it, as does a rank the rate table does not
list. A test for it needs a plan with that shape or it cannot reach the case.

**Non-consuming skips record nothing**, correctly. Pass-up and both compression
branches never advance the counter, so omitting them keeps reconstruction exact.
(HEU-641)

## Adding A Parameter To A `pub` Function Is A Breaking Change

Worth stating because a design document got this wrong and the error survived
several reviews before anyone checked it.

The provenance work threads a `&mut Vec<Walk>` collector into both instrumented
traversals rather than changing their return types. One stated reason was that an
out-parameter avoids breaking `count_generations_upward`'s public signature.
**That is false.** Adding a parameter breaks a `pub` item exactly as changing the
return type does. Every caller has to change either way.

What actually preserves the public signature is the split: `count_generations_upward`
stays as a thin uninstrumented wrapper with its original parameters, and a
crate-internal helper takes the collector. That was a separate decision, and it is
doing the work the out-parameter was credited with.

The out-parameter still won, on two grounds that hold. It matches
`emit_generation_earnings`, which already accumulates through a `&mut Vec`, so it
introduces no second convention. And it keeps the diff in the traversal bodies and
the five calculators rather than spreading it across the most densely tested files
in the repo.

The objection that out-parameters are less idiomatic than returning a value was
raised and overruled on that precedent. Recorded so a reviewer can see it was
decided rather than defaulted into. (HEU-641)
