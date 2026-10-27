# Network Engine Development Guide

Implementation patterns and conventions for the Rust network engine. Read this before working on any tree type or engine component.

This file uses American spelling. Six British spellings were corrected out of it in one day. The sentence you are reading contained the sixth, written while stating the rule.

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

### A stack overflow is not a panic, and recover cannot catch it

An `Error()` method that renders its own cause can recurse without bound if the
cause chain has a cycle. That ends in `fatal error: stack overflow`, which is a
runtime fatal error rather than a panic.

**`recover` does not catch it.** A caller that wraps the call in
`defer recover()` still loses the process.

Reached on HEU-797 by assigning a typed error to its own `Err` field, and by
pointing two typed errors at each other. Both were built by keyed composite
literal from outside the package, which is the shape a test double takes.

So a renderer that walks a cause must either bound the walk or refuse to call
through a cause it could re-enter. Containing the panic is not enough, because
this failure is not a panic.

`errors.Is` has the same unbounded property on a cyclic chain and hangs rather
than crashing. Guarding only the renderer leaves that open.

**A guard inside `Error()` reaches its own package's types and stops there.** A
cause of any other type has to be called, so a caller whose own `Error()`
renders the value holding it still recurses. `Error() string` takes no parameter
to carry a depth, the recursion leaves through a foreign method and comes back,
Go exposes no goroutine identity to key a re-entry set on, and a flag on the
receiver races two goroutines rendering one value. The standard library does not
defend against this either.

**A subprocess test can cover that limit, and one does.** A fatal stack overflow
cannot be recovered in-process, but a child process can be made to die of one
and the parent can assert the exit status and the message.
`debug.SetMaxStack(1 << 20)` keeps it under a second. That is how Go's own
runtime crash tests work.

`errors.Is` is not a precedent here, though it is easy to reach for as one. It
walks `Unwrap` and never calls `Error`, so a cycle closed through `Error` alone
returns at once rather than recursing. The cyclic-chain case above is a
different input.

## Tombstone Deletion

Removed nodes are tombstoned: cleared to `Uuid::nil()` with empty fields, then added to the free list. The slot is reused by the next `add_root` or `add_node`.

Clear the slot on removal. This releases heap allocations (the children Vec) and marks the slot as dead so stale data is never accidentally read.

Removal is leaf-only. Removing a node with children returns an error. The caller must remove children first, working from leaves up.

A live node never holds `Uuid::nil()`. Every mutation that adds a user to a tree refuses it with `NilUserId`. The streamline engine refuses it before it touches any state. (HEU-710)

`Arena::node` and `Arena::node_mut` hard-assert that the slot is live, in release builds too. Without the assert, a release build returns a freed slot as a node with no edges. Both builds were measured in release on 2026-10-19. The runs interleaved a `debug_assert!` build (A) with an `assert!` build (B):

- Branch collection under the root of a 131,071-node full binary tree, 50 per run, six pairs. A: 217, 231, 243, 211, 217, 220 µs, median 218.5. B: 268, 252, 241, 254, 242, 247 µs, median 249.5. About 31 µs more per collection, +14%.
- `calculate_binary_pairing` on the same tree with all 65,536 leaves as volume sources, 10 per run, eight pairs. A: 116.70, 98.93, 112.04, 102.88, 102.35, 99.73, 103.21, 99.21 ms, median 102.62. B: 103.53, 110.65, 103.97, 105.40, 99.07, 100.04, 100.35, 99.69 ms, median 101.94. No measurable difference.

Where the cost lands, read on 2026-10-19:

- Branch collection (`collect_subtree`, behind binary and matrix `get_branch`) calls `node()` twice per node. That is the first measurement.
- Binary commission's leg-volume pass calls `node()` twice per node across the whole tree. It is the only commission calculator that does. That is the second measurement.
- The other commission calculators walk through `walk_upline`, `bfs_downline`, `get_sponsored` and `get_sponsor`, which index `nodes` directly. Stairstep also calls `get_parent` once per breakaway, which reaches `node()` twice.
- Rank evaluation calls `get_children` from the leg-volume and leg-quality predicates, once per predicate it evaluates.
- Rank evaluation also orders users by depth. It calls `get_position` once per user in each tree that holds the user. Binary and matrix `get_position` call `node()` once.
- Mutations call `node()` and `node_mut()` a fixed number of times per operation. Matrix removal is the exception. It reads every node in the removed or promoted subtree.

Rerun both measurements before changing these asserts back.

A panic here reaches the worker's `catch_unwind` and answers `INTERNAL_ERROR`. Nothing restores the engine's state. A panic partway through a mutation leaves it as the handler had left it. A freed-slot read is a bug. A loud failure beats a silently wrong payout.

## Testing Conventions

### Unit tests

One logical assertion per test. Build small trees by hand. Test names follow `method_behavior` convention: `add_root_to_empty_tree`, `remove_node_with_children_fails`.

### Test helpers

`test_uuid(n: u8)` generates deterministic UUIDs for small tests. `test_uuid_u16(n: u16)` handles tests exceeding 255 nodes. Shared test helpers live in `tree/test_helpers.rs` and are used by all tree types and property tests.

### Assert what a regression makes false

A test named for a behavior can pass through the exact regression it describes. Both of HEU-611's cases were found by deleting the code the test was supposed to protect and watching it stay green.

`mixed_active_and_frozen_returns_earnings_and_a_skip` asserted `!earnings.is_empty()` and that the skip list held one entry. Unfilter `active_streams()` so frozen streams get walked and the response comes back with **two** earnings and **still exactly one** skip: both assertions pass, and the property the test is named for is broken. It only bites once it asserts the walk count.

The general shape: asserting a result is non-empty proves something ran, not that the right thing ran. On a partial result, assert what is *absent* — how many walks, which stream ids, which earners — because a regression that does too much leaves every non-emptiness check green.

Deleting the guard and re-running is cheap and it is the only way to know. Do it for any test whose name makes a claim about what did not happen.

### One test can be the only thing holding several guards

Deleting a guard tells you whether *a* test catches it. It does not tell you
*which*. The answer is sometimes "one test carries four checks."

`Arena::validate_restored` checks four edge fields. Two have a dedicated test
naming them. The `sponsor` and `sponsored` checks are killed only by
`validate_restored_checks_every_edge_field`, the loop that walks all four. Two
plausible-looking single-field tests in its place would have left both
unprotected. Every remaining test would still pass.

So when a guard dies, note how many tests died with it. A guard killed by
exactly one test whose name does not mention it is a guard with no owner.

### A guard on the wrong property reports coverage it does not have

Worse than no guard, because it reads as proof.

A round-trip test named for a matrix holding-tank entry asserted four users
placed under the root. That is true whether or not the tank holds anything. It
proves the fixture built, not that the state the test is named for was reached.
The tank was empty on both sides of the round trip. The comparison was one
empty array against another. It passed regardless.

Point the guard at the state in the test's name, not at a side effect of
building the fixture.

### An acceptance test can go vacuous where a rejection test cannot

A rejection test that stops reaching its state starts failing, because the
absence of the error is the failure. An acceptance test that stops reaching its
state keeps passing, because two empty things match.

That asymmetry means a green rejection test is evidence about itself. A green
acceptance test is not. Ask of every acceptance test: if the fixture stopped
producing the state in this test's name, would it still pass? If yes, the test
needs a guard on that state, checked before the thing under test runs.

### Tree member order is not stable

`user_ids` on every tree type, and on `TreeNavigator`, reads a `HashMap`. The
order changes from run to run. A test that only passes when one member comes
first passes or fails at random.

HEU-965 hit this. A test for "the error names the lowest missing ID" placed
node 3 before node 2. A mutant that reported the first missing ID instead of
the lowest still passed it 10 runs in 20, because placement order is not
listing order.

Fix the order the test relies on. Wrap the tree in a test-local
`TreeNavigator` that delegates every method and returns `user_ids` in the
order you need, then assert that order before the call under test.

### The assertion can be wrong rather than the code

Two shapes of this cost time on HEU-797. Both produce a failing test against
correct behavior, which sends the reader to debug the wrong thing.

**`require.NotNil` and `assert.NotNil` reflect.** They report a typed nil as
nil. A nil pointer inside a non-nil `error` interface is not nil at the language
level, and that gap is often the property under test. testify reads it as nil
and fails. A nil slice behaves the same way.

**`errors.Is` can never match a target that is not comparable.** It skips the
equality path for a slice or map target, so the call returns false however the
chain is built. `errors.As` works on those types and is the one to reach for.

Before changing code to satisfy an assertion, check that the assertion measures
what its name says.

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

### Adding a tree type

The tree types are listed by hand, in Go and in the worker. Nothing generates one list from another, and no test compares them. Find every branch on tree type before adding one:

```
git grep -nE 'treeType(Unilevel|Binary|Matrix)|"(unilevel|binary|matrix)" =>' -- '*.go' '*.rs'
```

Some of those branches refuse a type they do not name. Others fall through to a default that treats it like another type. Among them:

- Refuse: the worker's `create_tree` and `restore_snapshot` dispatches, `supportedTreeTypes`, the loader's slot-rule switch, and `checkNodePlacedShape`.
- Fall through: `placementCheck` in the writer, the consumer's placement call, and its position comparison. A new type with slots would be checked without its position and projected with it.

A refusal fails at runtime, not in a test. A fall-through does not fail at all.

### Proptest regression files and vacuity checks

Proptest writes a `.proptest-regressions` seed file next to a test the first time a case fails. The project checks the legitimate ones in (see `binary_commission_properties.proptest-regressions`) so saved edge cases re-run for everyone.

Two gotchas show up when hardening these tests. A vacuity check deliberately breaks an assertion to confirm it fails, then reverts. That leaves a stray regression file for the artificial failure. Delete it before committing. Scope the delete to the exact file. A `rm tests/*.proptest-regressions` glob also wipes the checked-in ones. Separately, the `FileFailurePersistence::SourceParallel set, but failed to find lib.rs or main.rs` line during integration-test runs is benign. Proptest just cannot locate the crate root from the test binary.

### A property net can go quiet without failing

A property test that only asserts something holds after each op passes just as
well when the ops stop doing anything. Twice on the restore-validation branch,
the net looked complete and mostly tested an empty engine.

* Streamline. Only a stream owner can sponsor an enrolment. With sponsors drawn
  from all members, about half of adds failed and runs stayed near four members.
* Board plan. Removed members stay in `sponsor_map`. Once every member was
  removed, no later add had a sponsor, and 205 of 256 runs sat on an empty
  engine.

Neither showed up as a failure. A probe that counted peak members, cycles and
full boards found both.

Before trusting a new property net, measure what it reaches. Count the states
its name claims, not only that it passed.

A test that picks among engine objects by iteration order can also replay
differently, when the engine breaks ties in `HashMap` order. Pick by a key the
strategy controls, such as the lowest occupant, and print the serialized state
on failure. The board plan engine breaks its own ties on `seq`.

### Completeness gate for commission ops

`every_structure_type_has_a_dispatchable_commission_op` (in `network-engine-worker/tests/worker_integration.rs`) stops a new plan type from shipping with an orphaned calculator. It has two halves. `commission_op` is a wildcard-free `match` over every `StructureConfig` variant. Adding a variant without an arm fails to compile the test crate, which forces you to name the op. A runtime loop then sends each named op to a real worker and asserts it never returns `UNKNOWN_OP`, confirming the op is wired into dispatch.

The two halves are not auto-synced. The runtime op list and `EXPECTED_OPS` are hand-maintained, so an op named in the match but omitted from the list slips the count assert. The backstop is the per-op integration test every real calculator gets. The gate also depends on `StructureConfig` staying exhaustive. If it becomes `#[non_exhaustive]`, the match would need a `_` arm and the compile-time forcing function breaks silently.

When you add a plan type: add its `StructureConfig` arm to `commission_op`, add the same op string to the runtime list, and bump `EXPECTED_OPS`.

## Shared Walk Module

Commission calculators that use level-based walks (unilevel, matrix, stairstep Walk 1, streamline, and generation) delegate to `commission/walk.rs`. The walk is generic over `TreeNavigator`. Plan-specific behavior is injected via `LevelWalkConfig` (e.g., matrix height ceiling) and the `should_stop` callback (e.g., stairstep breakaway boundaries). Binary uses pairing mechanics and does not use this module.

The walk function does not sort its output, and since HEU-641 the five commission calculators do not sort either. All five hand their earnings and walks to `walk_order::assemble`, which orders the walks, remaps each earning's collector id to a final walk index, and only then calls `sort_earnings`. Binary and board plan do not go through it. They return their own result types and never produce a walk. Stairstep still combines Walk 1 and Walk 2 before handing them over.

The walk takes a `&mut Vec<Walk>` collector as its last parameter and pushes one walk record per volume source as it goes. It records one step per node at each of the three sites that consume a level, and a stop on every walk, naming the break site when one fired.

Three consuming sites, four `steps.push` calls. The loop tail is one site with two mutually exclusive branches: a node with a rate is `Paid`, and a node whose rate table has no entry at its level falls back to zero and is `Forfeited`. Both consume the level, so one node yields one step either way. Counting the pushes instead of the sites gives four and is the wrong number, which has now caught two readers.

The walk can also fail. A node that reaches its snapshot lookup with no snapshot returns `UplineNotInSnapshot` and no walk is pushed for that source. Walks already pushed for earlier sources stay in the caller's collector, which the caller discards along with the error rather than handing to `walk_order::assemble`.

The public calculators refuse a missing snapshot before the walk runs (HEU-731). So `UplineNotInSnapshot` reaches only a direct caller of the walk.

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
incrementing, but the pattern is worth recognizing. When a loop counter is
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

Boards order by `(created_at, seq)`. The engine assigns `seq` from `next_board_seq` when it creates a board. A split numbers its new boards in child order. Board ids are random per run, so they never break a tie. Placement, `list_boards`, stall detection and the drain all use this order.

Add, remove and compress check their preconditions. Then they cycle every full board, oldest first. Each cycle gets a fresh cascade budget. Dissolve refuses a full board with `BOARD_FULL`. Otherwise it removes the board and then drains. `max_cascade_depth` limits recursion depth within one cascade, so a cascade can leave a board full. A later operation that passes its preconditions cycles it. Restore never drains.

Remove and compress also cycle the member's own board once, if it is full. Then they take the member off whatever board they are on, or out of the displaced pool. Sometimes that cycle re-enters the member onto a board, and the cascade bound leaves that board full. In that case the removal takes the member back off it. The board is then no longer full, so it does not cycle.

The displaced member pool (`displaced_members`) holds members removed via dissolution or cycling without re-entry. They are placed before new enrollees in `add_member`. `remove_member` takes a displaced member out of the pool. The `place_displaced_members` method must push unplaced members back to the pool if no boards have openings. Never propagate errors that would drop them.

All tree-layer types (including BoardPlanEngine) must remain serde-serializable. See design-rationale 023.

## Timestamps

`enrolled_at` is Unix seconds (i64) throughout the engine. Not milliseconds, not nanoseconds.

It is also load-bearing, not just descriptive. `MatrixTree` picks the promotion target on node removal with `min_by_key(enrolled_at)`, so the value decides *who moves up* when someone leaves. Anything that rebuilds a tree — reload, snapshot restore, migration — must carry `enrolled_at` through unchanged. Lose it or shift it and the tree still looks structurally correct, then promotes the wrong distributor on the next removal, with commission consequences and no error anywhere.

Assert it explicitly in round-trip tests. HEU-534 shipped a matrix reload where the value travelled correctly but nothing checked it; zeroing it during replay left the entire Go package green.

## Rank Evaluation: Empty `qualification.structures`

The `evaluate_ranks` worker handler registers tree navigators only for structures that a rank above the lowest names in its `qualification.structures`. The lowest rank's structures are not read, so a structure only it names needs no tree. A distributor in none of the registered trees is not evaluated. It holds the plan's lowest rank.

So a plan whose higher ranks name no structure gives every distributor the lowest rank. No higher rank is reached. A test that needs a higher rank must create a structure and place the distributor in it. A rank above the lowest must name that structure.

A distributor in no registered tree is never evaluated against a higher rank whose qualification names no structure. HEU-953 tracks it.

Every member of a tree whose structure a rank above the lowest names must have an entry in `distributors`. `evaluate_ranks` refuses a request that leaves one out. The error is `EVALUATION_ERROR` and it names the lowest missing user ID (HEU-965). A test that places a node in such a tree must give that node inputs, even when its rank is not asserted.

## Contract-Test Harness: `setup_raw` for Byte-Sensitive Fixtures

`engine/network-engine-worker/tests/contract_tests.rs` and `internal/networkengine/contract_test.go` round-trip fixture setup steps through `serde_json::Value` / `map[string]any`, which sorts object keys. Go's `json.Marshal` always sorts, and the Rust side sorts too since `serde_json::Value` is a `BTreeMap`.

Use `setup_raw` when the fixture needs to control its own bytes. Malformed JSON, duplicate keys, or a specific key order the assertion depends on — none of those survive a round-trip through a map. `setup_raw` lines are sent verbatim as NDJSON. `setup` and `setup_raw` are mutually exclusive per fixture, and the harness asserts it.

It is no longer required just because a fixture loads a plan. That rule existed because sorting puts `config` before `type` in an adjacently-tagged `StructureConfig`, which made serde buffer the content, which stripped the string-to-`u8` coercion that a `rate_table` keyed by `u8` needs. Loading a plan through `setup` failed outright. HEU-648 fixed that with `deserialize_with` helpers in `engine/network-engine/src/serde_helpers.rs`, so either key order parses now.

Existing plan fixtures still use `setup_raw`. Leave them: changing a fixture's encoding path changes the bytes the worker sees, which is not a change worth making without a reason.

Pattern: `request_raw` exists for the same reason on the request side. If you find yourself adding a new escape hatch for `params`, follow that precedent.

## Contract-Test Harness: Numbers Compare As Exact Decimals

Both contract runners compare numbers as exact decimals. A fixture may write `500` or `500.0`. The worker writes every decimal in normalized form, with no trailing zeros. A string is never equal to a number, so a quoted `"500"` still fails against `500`.

## Contract-Test Harness: No Per-Fixture Filter

`contract_tests.rs` holds exactly one test function, `contract_fixtures_match_worker_behavior`, which loops over every fixture in `engine/testdata/contracts/`. Fixture names are not test names.

Filtering by one — `cargo test --test contract_tests calculate_streamline` — matches zero tests, prints `0 passed; 1 filtered out`, and **exits 0**. That reads as a pass, which is worse than a failure.

Run it unfiltered. To confirm a specific fixture actually executed, add `-- --nocapture`: the harness prints `contract: <name> -- <description>` for each one.

That line is indented two spaces, so `grep '^contract: '` matches nothing and exits nonzero **with no test failure** — the same reads-as-a-pass trap in a different disguise. Grep without the anchor.

HEU-583's plan specified the filtered form on three steps, including the two that changed the money path and the wire contract. Following it literally would have recorded "Expected: PASS" against a run that asserted nothing.

The trap is not specific to contract tests. Any name filter that matches nothing prints `filtered out` and exits 0. HEU-611 hit it in `worker_integration`: `cargo test -p network-engine-worker --test worker_integration streamline` never ran `repeated_source_yields_distinguishable_skip_records`, because that name contains no "streamline". The plan prescribed that filter for three steps and the test was RED at the time, so the step would have reported a pass over a test that had never executed.

A filter selects on the *test function's* name, and a suite's tests are not all named after the thing they test. Read the `N passed` count against the number of tests you expect, or run unfiltered. `0 passed` is the loud case; the quiet one is a filter that catches most of a group and drops one.

## For A Mutation Check, Read The Go Contract Runner

The Rust contract runner is one test. It panics on the first fixture that does not match, so it names one failure and hides the rest.

The Go `TestContractFixtures` runs each fixture as its own subtest. It names every fixture that fails. When you break a behaviour to see which fixtures pin it, read the Go runner.

The Go runner talks to the built worker. Rebuild the worker after the mutation, and again after you restore the file. (HEU-960)

## A Calculator-Level Determinism Test Cannot Catch The Walk Sort

`walk_order::assign_indexes` sorts a response's walks into a total order, but
for all five calculators that sort is a no-op. They already emit in the order
it produces.

| Calculator | Emission order | Sort is |
| -- | -- | -- |
| `calculate_unilevel` | one `walk_level_commissions` call over the whole `volume` slice | a no-op |
| `calculate_matrix` | same | a no-op |
| `calculate_stairstep` | `for source in volume` | a no-op |
| `calculate_generation` SameRank | rank-outer over `unique_ranks`, sorted by ordinal first, then `for source in volume` | a no-op |
| `calculate_streamline` | active streams sorted by stream id (HEU-969) | a no-op |

So a "two runs produce identical indexes" property written against any
calculator cannot fail. Deleting the sort outright leaves it green. The sort's
own unit tests in `walk_order.rs` pin it. Streamline emitted in `HashMap` order
until HEU-969 sorted its streams.

Two traps apply to any test that relies on `HashMap` order. Build **two** engines rather than running one twice: a
single `HashMap` repeats its own iteration order, so the one-engine version is
vacuous. And two separately built maps get different `RandomState` keys but not
necessarily a different order for a small table, so `prop_assume!` that the two
emission orders actually differ, or roughly half the small cases prove nothing.

Before HEU-969, this was verified by deleting the sort and watching which tests failed. (HEU-641)

## Numbers Are Exact Decimals

- `AuthoredDec` and `Dec` read JSON numbers only, and refuse a quoted number.
- Adding, subtracting and multiplying give the exact result or a `NumericError`, never a rounded one.
- Four operations are inexact, each with one rule:
  - E1 is `div_ratio`, the reported volume ratio. It follows rust_decimal 1.43's `checked_div`: half to even at the largest scale of 28 or below that fits 96 bits.
  - E2 is `mul_div_ratio`, the volume-ratio payout. It rounds once, half to even, at 13 places. When 13 places do not fit 96 bits, it uses the largest smaller scale that does.
  - E3 is `mul_div_trunc`. It truncates toward zero at a given scale.
- Each earning's money is rounded to the plan currency's minor unit with `round_minor`, ties away from zero.
- An aggregate cap is split with `allocate_largest_remainder` in whole minor units, so the shares sum exactly to the cap.
  - E4 is `floor_div`, a cycle count. It rounds down to an integer.
- An authored value must have an absolute value below 10^15, with at most 10 decimal places.
- serde_json's `arbitrary_precision` is a normal dependency of both crates. A number on the wire never passes through `f64`.
- Tests write decimal values with `d!()`, and compare against a literal with `assert_d!`.
- A request line that does not parse as a request gets `INVALID_REQUEST` with an empty `id`.

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

`cargo fmt` is the trigger people do not expect. It rewrites `.rs` files. Running it after a build makes the binary stale. The next `go test ./...` then turns every worker-dependent test red at 0.00s. The formatter looks like the culprit and is not. Rebuild and re-run.

## Chain The Two Suites With `&&`, Never `;`

The suite spans two toolchains. A single "did it pass" answer needs both. `;` does not give you one:

```bash
go test ./... ; (cd engine && cargo test)     # reports only cargo's status
go test ./... && (cd engine && cargo test)    # reports the first failure
```

Both halves carry their own `cd`, because there is no `Cargo.toml` at the
repository root and a bare `cargo` command run from there fails before it tests
anything.

With `;`, a green Rust run masks a failed Go run. The shell then exits 0. This is not hypothetical: a run that reported exit 0 on this branch had 41 failing Go tests in it.

Report a pass as a count per runner, not as one exit status. Naming both totals and both denominators is checkable. "Exit 0" hides how many commands the status covered.

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
hold an Unranked result. The engine returns one only for a plan with no
ranks (HEU-761). The HEU-446 window and tenure gates treat both as below
threshold.

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
  A real `PeriodInputProvider` (HEU-505) may hand over nils without an
  `INVALID_PARAMS` error.
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

One narrowing rode along with HEU-626. The serde work only widens what the
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

Since HEU-731 a source in the tree with no snapshot is refused earlier.
The error is `DistributorNotInSnapshot`.
The `SourceNotInSnapshot` described above reaches only a direct caller of the
per-source check.

`walk::validate_source` owns the check order, and a caller that hands it the
source it is validating gets that order for free. Two calculators reproduce the
order themselves instead. Binary never reaches it: it resolves an owner before
the snapshot lookup, so it validates its own way. Streamline does reach it, once
per surviving source per stream, but only after a filter has already dropped the
sources its own pre-loop exists to catch. HEU-611 added that pre-loop, described
below.

A second narrowing rode along with HEU-611. `calculate_streamline` now
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

Since HEU-731 a stream member with no snapshot is refused earlier, frozen
streams included. The error is `DistributorNotInSnapshot`.
So the pre-loop's `SourceNotInSnapshot` reaches no caller of
`calculate_streamline`.

Still null-intolerant, tracked by HEU-632. These are nested or query-op
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

Tree loads no longer go through `LoadTree`. See "Building A Tree From Its Stream". This section describes `LoadTree` itself.

`LoadTree` rebuilds a tree in the engine from the `tree_nodes` adjacency rows. The invariants below are not obvious from the code and matter to anyone touching the loader, the projection, or the worker's tree handlers.

### A root's stored sponsor is dropped on reload

`AddRoot` takes no sponsor and the engine root carries `sponsor: None`. So whatever `sponsor_id` a depth-0 row holds is projection metadata, not a replay input, and reload discards it. Round-tripping a tree therefore does not preserve the root's stored sponsor.

Preflight deliberately exempts that field from every check a non-root gets: no nil check, no self-reference check, no existence check. Validating it would reject rows the engine can restore perfectly well, and treating it as a replay dependency makes the root wait on a node downstream of itself, which rejects the whole tree as a cycle.

Root **parent** and **position** are the opposite: `AddRoot` accepts neither, so a depth-0 row carrying either describes a topology the engine cannot reproduce, and preflight rejects it.

### Replay order must satisfy sponsor edges, not just parent edges

Every tree type resolves the sponsor at insert time and rejects one that is not present yet (`unilevel.rs`, `binary.rs`, `matrix.rs`). Depth order satisfies parents — a parent is always shallower — but says nothing about sponsors.

Automatic spillover always places a recruit below their sponsor, so depth order happened to work. Explicit placement removes that coincidence: an admin override can put a node at depth 1 whose sponsor sits at depth 10, and depth-ordered replay then fails mid-tree.

Every tree type reports a missing sponsor as `SponsorNotFound` → `SPONSOR_NOT_FOUND`, and a missing parent as `USER_NOT_FOUND`. The parent is checked first, so a placement missing both reports the parent. Before HEU-940, unilevel and binary reported a missing sponsor as `USER_NOT_FOUND`, so older logs show that code for this fault.

`orderForReplay` topologically sorts on both edges. Do not replace it with a depth sort.

### Matrix and binary require a position on every non-root row

Matrix reload replays through `add_node_at`, which takes an explicit slot. Binary replay passes `position` through `add_node`. Both reject a nil position at preflight rather than defaulting to 0, because a silent 0 places the node in the wrong slot and the divergence is invisible afterwards.

Unilevel carries no position. It appends to the parent's child list. Preflight rejects a unilevel non-root row that has a position, the same way it rejects a root that has one (HEU-563).

### Why preflight validates everything before mutating anything

A load that fails partway leaves a partly built tree in the worker. The engine may refuse a call after the create stage while the worker stays usable. A caller may then drop the tree with `drop_structure`. The engine refuses the same input the same way. A reload succeeds only once the cause has changed. UC-NET-032 covers this. Preflight is meant to catch engine refusals before the create. This path should be rare. After a timeout, a cancellation or a crash, the client is closed or the worker is gone. Starting over then means a new worker. The CLI never drops. Each command starts its own worker, and a failed load ends with it. Either way, a partial load costs a full rebuild. So `LoadTree` proves a load is sound before the first engine call. Its replay failures return `TreeLoadIncompleteError`. That error carries the stage the load reached and how many placements the engine acknowledged.

`Confirmed` counts acknowledgements, not what landed. At `TreeLoadStageNodes` the call that failed may also have taken effect, with the reply lost rather than the work undone. The structure then holds at least `Confirmed` placements and possibly one more. That carve-out does not apply when `Err` is nil, which means the load stopped before the call was sent. At the create stage all three counters are zero. At the root stage, `Total` carries the non-root placement count the load set out to make. The other two counters are zero. The operation in doubt is the root placement, which none of the three counters counts.

The loader cannot tell which happened. It holds a `TreeMutator`. That interface carries only mutations. Nothing on this path can read back whether the placement landed. A caller does not need that answer to start over. After a lost reply, starting over means a new worker. A new worker starts empty. HEU-777 and HEU-784 are the same lost-reply shape on the removal path rather than the replay path.

Preflight runs in two phases with different inputs. `validateTreeConfig` checks the tree type, and for a matrix the width and spillover. It reads no rows, so it runs before the store query. A misconfigured load costs no query. `validateNodes` runs after the query and proves the node set is structurally consistent.

The empty-tree short circuit sits between the two phases. A tree with zero rows still reports configuration errors. That is why a typo in startup wiring surfaces at the first load instead of staying invisible until the first node arrives.

### Sponsored-list order is not restored

`Arena::get_sponsored` returns its vec in insertion order, and reload inserts in `(depth, enrolled_at, user_id)` order rather than original event order. So a reloaded tree can report a distributor's recruits in a different order than the live tree did — on the HEU-534 integration fixture, `u1.sponsored` comes back `[u2, u5, u3]` where the event sequence produced `[u2, u3, u5]`.

Harmless today: every other read of that vec is a `retain`, and the adjacency table stores no ordering to restore even if we wanted to. Do not build anything that depends on sponsored-list order surviving a restart.

### Matrix events carry authoritative placement (HEU-553)

`node_placed` events are the single statement of placement. The payload requires `tree_type`. Matrix and binary events require an explicit `position`. Unilevel events must omit it. The consumer rejects an event that fails a payload check before either projection, so those rejections land nowhere. The rejected payload classes: wrong stream, unknown type, missing position, negative position, matrix position above the u8 ceiling (255), binary position outside 0..1, unilevel position present. All but the first are `node_placed`'s own. The stream check is shared: `root_added` and `node_removed` reject a mismatched stream too, through the same helper. Matrix placements project through `add_node_at`, so the engine applies exactly the stored parent and position. A partial unique index (migration 000004) makes a double-claimed slot fail at the insert instead of poisoning reload.

These limits remain:

- The gate rejects matrix positions above the u8 ceiling (255), which no width can accept. The real bound is the tree's width, which the placement payload does not carry. Under `TreeWriter`, `check_mutation` refuses such a position before the append. An event appended without the writer is stored, refused loudly by the engine, and then makes the next reload preflight reject the whole tree. HEU-554 decides the direction for this gap. The fix ships under it.
- Redelivery is bounded (HEU-576). The scope is the event in flight. `TreeWriter` provides it by calling `HandleEvent` synchronously, in process, under the tree's lock. A redelivered placement whose projection is still current completes, however old it is. It stops being current once its node was removed, or once a later event re-placed the user, and is then refused rather than reapplied. It is also refused when its parent's row was since removed, because the parent lookup runs before the guard (HEU-813). A redelivered removal that the engine refuses converges when its own tombstone is in the store (HEU-811). That holds even when a later placement of the user never reached the engine. When no row carries the removal's stamp and the user has no active row, the consumer refuses the removal as unprojectable. Then `reject-event` can clear it (HEU-868). One whose store write never landed fails typed. The next write or `tree load` redelivers it to an engine rebuilt from the stream. One arriving after a later placement landed in full is below the tree's projected version. The store refuses it (HEU-857). The engine may still have applied it. HEU-789 tracks that for when a caller redelivers old events. `TreeWriter` does not reach that case. Catch-up redelivers only the stream's last event. A removal followed by a later placement is no longer the last event. When a redelivery reaches its store write, the store refuses it if it is below the tree's projected version (HEU-857). The parent lookup and a removal's engine call run before that write.

Matrix startup reload is no longer blocked by the placement divergence HEU-553 fixed.

### The event id is the redelivery discriminator

`tree_nodes.id` is the event ID. The insert names the primary key as its `ON CONFLICT` arbiter and does nothing on a match. An already-stored event is then reported as a skipped row rather than raising. A skip does not prove the engine applied the event. The store insert lands before the engine call. A delivery that failed at the engine leaves the row behind. Since HEU-576 the handler reads the row back. It continues to the engine only when the row is this event's own and still active. It refuses every other state. A conflict against a different event's row is a separate signal and raises. Which constraint gets named when several are violated at once is not settled. The two stores are not pinned to agree on it (HEU-794).

### A tombstone carries the event that removed it

`tree_nodes.id` holds the placing event's ID. A removal cannot use it as a discriminator. Comparing a removal event's ID against it is false on every path, including a removal that genuinely never landed.

Migration 000007 adds `removed_by_event_id`. `ProjectRemoval` writes it in the tombstone's own statement. The stamp lands with the tombstone or not at all. The column is null while the row is active. `UndoRootProjection` leaves it null. Its caller rolls back a row the same handler just inserted. That rollback is not a removal. It has no removal event to name. A rejection's soft delete leaves it null too. That soft delete undoes an insert. It is not a removal (HEU-850).

`GetNodeByRemovalEvent` reads the row a given removal event stamped, scoped to one tree. When one event has stamped more than one row, the newest tombstone wins. `GetNodeIncludingRemoved` uses the same rule. On equal removal times, both return the highest id (HEU-803). The id order is defined for canonical hyphenated UUIDs. A redelivered removal arriving after the user was placed again is below the tree's projected version. On a tree with a projection row, `ProjectRemoval` refuses it, so no second stamp lands (HEU-857). A tree created before migration 000009 has no projection row, so nothing refuses the removal there. Suppose the engine refuses a removal, no row carries its stamp, and the user has no active row. The consumer then refuses it as unprojectable instead of converging (HEU-868). HEU-789 tracks the engine half, for when a caller redelivers old events.

The removal reconcile runs only when the engine refuses the removal with user-not-found. It asks this read first. A stamped row means an earlier delivery of this event already projected. The reconcile then converges without writing. With no stamp, it reads by tree and user. An active row means the removal never landed. A tombstone stamped by this event converges too. Another writer can project the removal between the two reads, and the user read is the later of the two. Any other tombstone, or no row at all, makes the consumer refuse the removal as unprojectable. A failed stamp read retries. It is never read as "no stamp". Reading it that way could report a transient read error as a permanent divergence.

The stamp says which event removed a row. It does not order several removals of the same user. HEU-789 asks that question.

## Tree Writes Go Through `TreeWriter`

`TreeWriter` appends every tree event (HEU-301). `mlmforge tree add-root`, `tree place`, `tree remove` and `tree reject-event` drive it, and `tree load` drives its `Load`. Code that appends to a tree stream without it breaks each guarantee below.

### One writer per tree at a time

Each write holds a per-tree Postgres advisory lock from before the load until projection returns. The event store's version check serialises appends. It does not serialise decisions. Two runs can each load a tree. Each can decide correctly against what it saw. Together they can still write a state neither would have allowed. The lock closes that gap.

- The lock uses the two-key form. The first key is a fixed namespace for tree writes. The second is FNV-1a over the tree ID's 16 bytes. Changing that function lets two binaries take different keys for one tree.
- Every tree ID is canonicalised before it names a stream or a lock. Two spellings of one ID would otherwise name two streams and two locks.
- The lock sits on a connection of its own, outside the stores' pool. On a one-connection pool, a lock holding the pool's only connection would deadlock the stores.
- The lock is session-scoped. A writer that crashes drops its connection. The lock goes with it.
- The lock needs a direct Postgres session. Behind a pooler in transaction mode, the lock and the unlock can run on different server sessions. Then the lock excludes nothing.
- The writer does not re-check the lock while it holds it. If the server ends the lock's session mid-write, another writer can take the lock. The first writer learns of it only at unlock, as `ReleaseErr`.
- A test that simulates a lost lock ends the lock's backend with `pg_terminate_backend(pid, timeout)`. The one-argument form returns once the signal is sent, not once the backend has exited. The waiting form returns false if the backend outlives the timeout.
- In the two HEU-784 removal tests, a store write that left a sponsor naming a removed user made the next write refuse to load the tree, and it appended nothing. That holds while every command reads the tree's rows fresh and preflights them, as the CLI does today.
- The wait is bounded, 30 seconds by default. The timeout names the tree and the wait. It does not say another process holds the lock, because the writer cannot see that.
- The timeout is a `TreeLockWaitError` wrapping the locker's own error. That error is often `context.DeadlineExceeded`. Match `TreeLockWaitError` with `errors.As` before testing for a context error. Otherwise a lock timeout reads as the caller's own deadline.
- The lock is cheap because a CLI invocation lasts seconds. A long-lived service that holds a connection for every tree operation re-examines it rather than inheriting it.

### The engine is scratch

Each invocation starts a worker. It rebuilds the tree in the worker from the tree's event stream, under the lock, starting from a stored snapshot when one is usable. It discards the worker on exit. The engine's jobs in a write are to check the mutation and to compute a removal's moved recruits. What HEU-789 and HEU-813 record as unreachable under the writer holds only while the engine is scratch. So does HEU-777's repair, which redelivers a removal to an engine rebuilt from the stream. A service that keeps an engine in memory reopens HEU-789, HEU-813 and that repair.

### The projected version

The store records one version per tree, the stream version its rows reflect, in `tree_projections` (migration 000009). `ProjectInsert`, `ProjectRemoval` and `ProjectRejection` check it in the same transaction as their row writes. An event below it is refused with `ProjectionRefusedError` and writes nothing. An event at it runs the existing redelivery logic, which writes nothing for an event the store already holds. `UndoRootProjection` requires the version to equal the event's, and otherwise returns a plain error and deletes nothing. `TreeStore` declares no write that skips the check (HEU-864). Trees created before migration 000009 have no projection row, and the writer refuses them once their stream passes version 1. `ProjectRemoval`, `ProjectRejection` and `UndoRootProjection` read the tombstone time with `clock_timestamp()` after taking the projection lock. A write that waited on the lock is not stamped with its transaction's start time (HEU-866).

### Before the append

- Version 1 of a tree's stream records the tree type. For a matrix it also records the width and spillover. Every later write reads the shape from there. A version 1 that is not a complete `root_added` refuses the write.
- `HandleEvent` takes the shape that version 1 records. The consumer refuses a placement or removal whose `tree_type` disagrees with it. That refusal is unprojectable and comes before any store or engine call. `reject-event` can then clear it. A removal with no `tree_type` still passes on a unilevel or binary tree. An event the store applied before HEU-942 is different. A mismatched removal stays stuck. So does a mismatched placement whose row passes the load's preflight, because only replay refuses it.
- The consumer refuses a `root_added`, `node_placed` or `node_removed` whose `user_id`, `parent_id` or `sponsor_id` is not a canonical UUID (HEU-933). Canonical means the value equals `uuid.Parse(value).String()`. That value is lowercase and hyphenated, with no `urn:uuid:` prefix and no braces. It also refuses the nil UUID in those fields, except as a root's `sponsor_id`. The refusal is unprojectable and comes before any store or engine call. `reject-event` can then clear it. Replay runs the same ID checks. An event the store applied before HEU-933 with a non-canonical ID now fails replay. Replay's refusal is not evidence. `reject-event` cannot clear such an event when replay is the only check that refuses it. A stored row whose user or sponsor ID is the nil UUID is still cleared, because the load preflight refuses it before replay. A tree whose usable snapshot already covers such an event still loads, because replay starts after the snapshot. Found by reading the code on 2026-10-20. No test pinned it then.
- `TreeWriter` does not take the label from its caller. It copies it from version 1. So a mismatched label can only come from an event appended without the writer.
- `add-root` on a stream that already has a version 1 is refused when its type, matrix width or spillover differs from what version 1 records. A matrix flag on a non-matrix tree is refused. Matrix flags left off the request match. On an empty stream the check runs again under the lock. A root that landed between the first check and the lock is what the second check compares against.
- Removing a tree's only root is allowed. A later `add-root` roots the tree again (Michael, 2026-09-23).
- The writer reads the tree's projected version and its active rows in one consistent read. It builds the tree from the stream through that version, then reads the stream's last version. It goes ahead only when the last version is the projected version or one past it. A tree with no projection row whose stream ends past version 1 is refused with `ProjectionMissingError`, unless the stream ends with a pending rejection. Any other mismatch is refused with `StreamMovedError`. Neither appends anything.
- The stream's last event is redelivered through `HandleEvent`. Under the lock it is the only event that can be unprojected. The one exception is the event a pending rejection names. Catch-up redelivers only `tree.root_added`, `tree.node_placed`, `tree.node_removed` and `tree.event_rejected`. A rejection is redelivered only when the store has applied it. A write or `tree load` on a stream ending in a rejection the store is one or two versions behind is refused with `RejectionPendingError`, which names `tree reject-event`. A wider gap is refused with `StreamMovedError`, or `ProjectionMissingError` when the tree has no projection row (HEU-850). A last event of any other type refuses the write before `HandleEvent` is called. `HandleEvent` returns nil for a type it does not project. Calling it would report that event as projected.
- `check_mutation` asks the engine whether the mutation would succeed. The worker runs the check functions the mutating ops call first. That is why no refusal rule is copied into Go.

### Three outcomes for an append

`Append` can commit and still return an error. On any append error other than a concurrency conflict or a validation refusal, the writer reads the version it tried. The read runs on a context detached from the caller's. What it finds decides the outcome:

- Its own event: appended.
- Another event: not appended.
- No event: treated as not appended. The error states only what the read found. A caller cancelled during COMMIT can see the commit fail while the server finishes it. The read may run before the commit is visible. Such an event shows up as the stream's last event. The next write or `tree load` redelivers it. When that event is a rejection, the write or load is refused instead, with `RejectionPendingError`.
- The read fails: unknown. The CLI exits 1.

A confirmed append is a success even when projection fails. A projection error does not show the store is behind. A placement commits its row and version before its engine call. So the writer reads the tree's projected version on a detached context and reports it as `Observed`. The CLI exits by what it saw:

- At or past the appended version, it exits 0 and states the version.
- Exactly one below, it exits 3, states what it saw, and says the next write or `tree load` redelivers the event. An appended version 1 with no projection row is one below.
- Further below, with no row and an appended version past 1, or when the read fails, it exits 3 and states what it saw.
- A projection the store refused as below its version observes a later version. It exits 0. Its warning names no redelivery.

What `WriteResult` carries:

- `Stream`, set on every path that reaches the lock.
- `EventID` and `Version`, set once the append is confirmed.
- `CaughtUp`, the redelivered last event.
- `ProjectionErr`, any failure after the append was confirmed.
- `Observed`, the projected version read after a projection error, or the read's error.
- `Warnings`, what the write observed and carried on past, in order: load fallbacks and snapshot outcomes.
- `ReleaseErr`, set on every path after the lock is taken, including when the write returns an error.

### Loading through the writer

`tree load` calls `TreeWriter.Load`. It takes the tree's lock and runs the same fence as a write. When the stream's last version is the projected version plus one, it redelivers that event through `HandleEvent`. When the stream ends with a rejection one or two past the store, it refuses with `RejectionPendingError` instead (HEU-850). When they are equal it redelivers nothing, so a load with nothing behind prints what it printed before HEU-777. After a redelivery it reads the projected version again and reports it when the redelivery did not advance it. The node count is the active row count from the load's read when nothing was redelivered, and a fresh read when something was. `LoadResult` carries `Warnings` the same way `WriteResult` does.

- Load's `--tree-type` and matrix flags must match version 1, with `add-root`'s rule. Matrix flags left off match.
- A fence refusal exits 1 and changes no store rows or projected version. The scratch engine may already be loaded, and is discarded.
- A failed read of version 1, or of the projected version and rows, comes before any engine call, so it is a `TreeLoadRejectedError` of kind `store_read_failed` and follows the retry rule. Anything after the engine load is not retried.
- A tree created before migration 000009 has no projection row, and load refuses it once its stream passes version 1.

### Building A Tree From Its Stream

Every load builds the tree in the engine from the tree's events, not from `tree_nodes`.

- The projected version and the active rows come from one consistent read, `TreeStore.ProjectedRows`. A writer that lost its lock can still project late (HEU-857). Two separate reads could pair an old version with newer rows.
- The store preflight still runs over the rows first, and still blocks the load with `TreeLoadRejectedError`. It is the only check on the rows themselves.
- With a usable snapshot, the load restores it and replays the events after it through the projected version. Otherwise it replays the whole stream. Replay stops at the projected version. `catchUp` keeps the stream's last event.
- Replay skips an event when the next event is a rejection naming it. Only a rejection at or below the projected version counts. One at the projected version plus one is a pending rejection, as before.
- Replay never calls `HandleEvent` and never writes the store. HEU-789 stays parked while only `TreeWriter` calls `HandleEvent`. Check it with `git grep -n 'HandleEvent(' -- '*.go' ':!*_test.go'`.
- After the build, the load compares the engine's root and downline with the rows on user, depth and enrolment second. It does not compare parent, position or sponsor, and it does not look at the matrix holding tank.
- On the snapshot path, an engine refusal during restore or replay drops the structure. So does a mismatch. The load then deletes the snapshot and replays the whole stream. It reports one warning. On the full path, either fails the load with `TreeReplayError` and attempts no drop. A transport or context failure fails the load with `TreeReplayError` and attempts no drop. So does a restore refused with `TREE_EXISTS`, because the load did not build that structure.
- The load deletes a snapshot and warns when any of these hold. The snapshot is above the projected version. It is of another tree type. Its bytes are not valid JSON. The next event is a rejection naming its version.
- A snapshot warning states what the load observed and what happened to the snapshot. The replay that follows reports its own result.
- A write whose projection succeeded takes a snapshot when the stream is 1000 events past the stored one. The write that crosses the threshold pays the whole snapshot cost while it holds the lock. A put that fails is a warning. Every later write tries again until one succeeds (HEU-914). A snapshot read or delete that keeps failing has the same effect.
- A full replay that fails after a drop leaves a partly built structure in the worker. That is safe only because each command owns its worker. A long-lived caller, such as HEU-298's reconciliation, must drop the structure before reusing the worker.
- A snapshot must fit in one `bytea` field, which caps at 1 GB. At about 230 bytes per matrix node, that is near 4.3M nodes.

### An error that wraps another reaches every `errors.As`

A wrapper with `Unwrap() []error`, or a `fmt.Errorf` with two `%w`, exposes both errors to every `errors.As` call down the chain. A dispatch written for one error type finds it inside the wrapper too.

- HEU-297 found this the hard way. A fence refusal was wrapped beside the failed build so the operator would see both. `rejectionEvidence` then found the build's preflight `TreeLoadRejectedError` inside the wrapper and counted it as evidence. `Reject` appended a rejection through the fence.
- Before adding a wrapper that carries a second error, list every `errors.As` dispatch the chain can reach. For the load error types, today that means `rejectionEvidence`, `treeLoadRetryable` and `treeLoadFailureMessage`. Confirm each one handles the wrapper on purpose.
- A wrapper that carries a build failure is never reject evidence. `rejectionEvidence` gives each such type its own early "no evidence" return, with a test. `RefusedAfterFailedBuildError` and the drop-stage `TreeReplayError` have one.
- One replay-stage `TreeReplayError` is evidence. It is replay refusing the stream's last event with a code in `rejectionRefusalCodes`, when that event is a `node_placed` (HEU-930). The rule reads the outermost `TreeReplayError`. A drop-stage wrapper around a replay refusal is therefore never evidence. A rejection tombstones the row that placement committed. A removal is excluded, because a rejection cannot undo a removal the store applied.

### A `json.RawMessage` parameter fails before the worker sees it

`EngineClient` marshals each request before it writes to the worker. A `json.RawMessage` field holding bytes that are not valid JSON fails that marshal. The error is a client-side encoding error, not an `EngineError`, so code that dispatches on engine refusals does not see a refusal.

- The writer test fakes never marshal, so a unit test over them cannot show this. HEU-297's snapshot restore hit it: a stored snapshot whose bytes were not JSON failed every load of the tree with no fallback.
- Validate the bytes with `json.Valid` before the call when they come from storage. The load treats a snapshot that fails the check as unusable and deletes it.

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

## Reply Errors Do Not Close The Transport

Inside a call, `StdioTransport` marks itself closed only when the call's context
ends. An unmarshal failure or a response-ID mismatch returns an error and leaves
the transport open. The worker may already have run the request. The stream may
now be out of step.

So an error that is not an `*EngineError` says nothing about whether the client
is usable. Only an `*EngineError` comes from a reply that was read in step.

## The Three Consuming Branches In The Level Walk

The Shared Walk Module section above says the walk records a step at each of the
three sites that consume a level, and why three is not four. This is what those
three are.

Anchored by content rather than by line number. These have rotted three times.

| Branch | Condition |
| -- | -- |
| `saturating_add` with the `// forfeit level` comment | Node is ineligible and was not compressed |
| `saturating_add` under the `max_earning_depth` check | Per-distributor depth cap from active leg tiers |
| the trailing `saturating_add` at the bottom of the node loop, **when `rate` is zero** | Eligible, uncompressed, but the rate table has no entry for this rank at this level |

**The third is the one that reads as safe, and the ticket, the plan and the
implementation brief all missed it.** The others `continue`, so they look like
the exceptional paths. The third is the unconditional increment every surviving
node reaches, and it forfeits only when the rate lookup falls back to
`unwrap_or(AuthoredDec::ZERO)`.

Missing it leaves `steps.len()` short by one for every zero-rate node. That
compiles, passes every existing fixture, and silently breaks the one property
walk records exist to provide.

It is reachable rather than theoretical. Any plan whose `max_depth` exceeds the
deepest level in its rate table hits it, as does a rank the rate table does not
list. A test for it needs a plan with that shape or it cannot reach the case.

**Non-consuming skips record nothing**, correctly. Pass-up and both compression
branches never advance the counter, so omitting them keeps reconstruction exact.
(HEU-641)

### Why an older document may say four

A fourth branch stood here until HEU-609. A node absent from the snapshot map
forfeited a level when neither compression nor dynamic thresholds were
configured, and was skipped without consuming one when either was. It now
returns `UplineNotInSnapshot` before reaching either, so it consumes nothing and
records no step. Anything written before that branch merged counts one more than
this section does.

The condition matters to anyone reconstructing the old behavior per calculator.
Streamline never set compression and always set dynamic thresholds, so "when
compression was on" would send a reader to the wrong branch for the calculator
most likely to be asked about.

## Adding A Parameter To A `pub` Function Is A Breaking Change

Worth stating because a design document got this wrong and the error survived
several reviews before anyone checked it.

The provenance work threads a `&mut Vec<Walk>` collector into both instrumented
traversals rather than changing their return types. One stated reason was that an
out-parameter avoids breaking `count_generations_upward`'s public signature.
That is false. Adding a parameter breaks a `pub` item exactly as changing the
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

## A `debug_assert` Discriminator Makes A Test Pass In Release

A test that pins the order of two checks needs something that tells the orders
apart. When the only thing available is a `debug_assert`, the test passes in
release on the exact ordering it exists to reject.

`walk::validate_broad_pct` is the case that produced this. It asserts and
otherwise only logs, so it never returns an error. A test asserting that the
rank check runs before it reads:

- **debug:** rank check first returns `Err`. Moved below, the assert fires and
  the test fails. It discriminates.
- **release:** the assert compiles out. Moved below, the rank check still runs
  and still returns the same `Err`. The test passes on the broken order.

It does not skip and it does not error. It reports green.

Gate the test on the thing it depends on:

```rust
#[test]
#[cfg(debug_assertions)]
fn unknown_rank_is_rejected_before_the_broad_pct_guard() {
```

That turns a false pass into no test, which is honest. Say in the docblock why
the gate is there, or the next reader deletes it.

Prefer a discriminator that survives both profiles. Anything that returns a
distinguishable value works: a second `Result`-returning guard, an early `Ok`
with no earnings, a different error variant. Reach for the `debug_assert` only
when the function under test has nothing else, and add a release-safe pin
alongside it rather than instead of it.

CI does not currently catch this. `.github/workflows/ci.yml` runs the suite
in debug, so a debug-only pin is live there. The protection is a convention
rather than a guard, and the failure is silent if that convention changes.
(HEU-608)

## Narrowing a struct field breaks assertions at run time, not compile time

`testify`'s `assert.Equal` compares types as well as values. An untyped constant
lands as `int`, so `assert.Equal(t, 1, x)` fails the moment `x` becomes
`uint32`. It still compiles, and `go build` and `go vet` both stay clean.

Sweep the package for comparisons against any field you narrow. `assert.Len`
and `assert.True` are not type-sensitive. Struct literals with untyped constants
convert at compile time and are fine. What breaks is `assert.Equal` and an
explicitly typed literal such as `map[string]int{...}`.

HEU-606 narrowed 14 wire DTO fields. Two breaks were compile errors and six were
run-time assertion failures, so building was not enough to find them.

## What `validate_restored` Still Does Not Check

All four arms above the arena check their own structures against each other.
HEU-750 closed the last of them.

The arena proves one rooted tree. Parent and child edges name each other, and
so do sponsor and sponsored edges. Exactly one live node has no parent, and it
is the root. Every sponsor chain ends. Binary and matrix also prove each
parent's occupied slots, in slot order, equal its `children`. HEU-732, HEU-712,
HEU-744 and HEU-734 added these.

A cycle through `parent` edges never needed its own check. The depth pass
requires a child to sit one below its parent, so a parent chain cannot return.

Two gaps remain. Both are recorded rather than fixed.

The arena's free list is checked one way. Every free-list entry must name a
tombstoned slot. No tombstone has to appear in the free list. A tombstone left
out is leaked. `node_count` counts it live. Its reach today is three `Debug`
impls.

A restored board that is already full is accepted. A cascade that reaches
`max_cascade_depth` leaves a full board standing until a later operation
cycles it, so rejecting one would reject engines the crate produces. Restore
does not cycle it.

Read a `validate_restored` docblock as the boundary of what that arm proves. An
arm that proves less than its name suggests is the failure this file already
describes one section up: a guard on the wrong property reports coverage it does
not have, and reads as proof.

## A Counting Method That Tracks The Truth Loosely Is Worse Than One Obviously Wrong

`grep -c '#\[error("'` over `snapshot.rs` was used to count error variants. It
returned 25 when the answer was 34, because nine variants wrap the message onto
the next line and the pattern requires the quote on the same line as the
attribute.

The number was then handed to a reviewer as part of its brief.

After three variants were added, the same grep returns 26 against a true 37. It
moved in the right direction and stayed wrong. A method that drifts with the
truth reads as working, which is what makes it harder to catch than one that
returns something absurd.

Count enum variants by pairing each attribute to the identifier that follows it,
or by counting the identifiers directly. Confirm only one enum in the file
carries the attribute, or you are counting two things and reporting one.

## The MSRV Is Not Enforced, And Knowing That Does Not Protect You

`rust-version = "1.85.0"` is declared in both engine crates. CI installs
`dtolnay/rust-toolchain@stable`. Nothing compiles the workspace against the
declared version, so a syntax feature newer than 1.85 reaches a commit and
passes every gate. `clippy::incompatible_msrv` covers std APIs, not syntax.

Let-chains, `if let Some(x) = y && cond`, are the ones that keep landing. They
need 1.88.

Awareness is not the mitigation. The ticket documenting this gap was written,
and the same fault was reproduced in a new guard about two hours later on the
same branch, caught by reading rather than by any gate. Write the nested `if`.
HEU-745 holds the fix.

## A Reviewer Pointed At A Worktree Reports On The Worktree

A long-lived branch drifts behind its base. A file the branch never touches can
be corrected upstream while the branch keeps the old copy.

A reviewer reading that branch quotes the old copy accurately and reports a real
defect. Someone checking from the main checkout quotes the corrected copy,
accurately, and concludes the reviewer misread. Both are right about their own
tree and the disagreement is about which tree, not about the sentence.

This cost a round trip on HEU-706. The finding was a version banner that was
stale on the branch and already fixed on `main` by two commits the branch's base
predated. The rebase resolved it with no commit.

When a finding cites a file, check it in the tree the finding came from, and say
which tree in the answer.

## The Board Plan Validator's Walk Order

`BoardPlanEngine::validate_restored` runs its walks in order. Each depends
on the ones before it having returned. The comment in the function names the two
that are load-bearing. The full list is here, because it is a paragraph and a
paragraph does not belong above a function.

The steps, in source order, and what each gives the ones after it.

| Step | Walk | What it gives the steps after it |
|---|---|---|
| 1 | Dimensions and the cached position count | A board size recomputed from width and height rather than trusted |
| 2 | Map key against `Board.id` | A key later steps can report as an identity an operator can look up |
| 3 | One pass over `boards`, doing sizing and duplicate detection together | A mis-sized board is skipped, so its occupants never reach the seat maps. Then exactly one board per occupant in those maps |
| 4 | Membership, forward | Every case where the index names a board is already reported |
| 5 | Membership, reverse | Left with the one case the forward pass does not own: a seat the index names nowhere |
| 6 | `displaced_members` | Runs after step 4, so a user the index puts on a board that does not exist is reported by step 4 instead |
| 7 | Gap check: every board fills from position 0 | Nothing. No step depends on it |
| 8 | Board `seq`: the counter is at most half the u64 range, every `seq` is below it, and no two boards share one | Nothing. No step depends on it |

Step 1 is a pair of `if`s. Step 8 opens with an `if` and then one `for`. Every
other step is one `for`. Sizing and duplicate detection share step 3 because
both read the same positions, and the two membership passes are separate loops
over different maps.

Two of these change behavior rather than message wording if moved. Step 2 before
anything that treats a key as an identity, and step 3's duplicate half before
step 5.

Reordering the rest changes which fault a payload reports. Every fault reaches
the wire as `INCONSISTENT_SNAPSHOT`, so no fixture pins a different code. What a
fixture pins is the message, by substring.

## A Validator And Its Producer Are One Change

`MemberOnTwoBoards` rejects a restored engine that seats one user on two boards.
It was correct and it could not ship alone.

`add_member` produced that state legitimately. A displaced member is absent from
`member_boards` by design, so the duplicate check missed them, the displaced pool
seated them, and the enrolment seated them again. The call returned `Ok`.

Ship the guard alone and a live engine starts writing snapshots it can never
load back. A silent seating bug becomes an unrecoverable board plan.

So when a guard rejects a state, find out whether anything in the crate produces
it. If something does, that producer is the other half of the same change, not a
follow-up ticket. Drive the public API hard enough to find out, because the state
that matters is the one the engine reaches on its own.

## Three Classes Of Restore Defect

A restored structure can be wrong in three ways, and they need different checks.

| Class | What is wrong | Closed by | Still open |
|---|---|---|---|
| Structures disagree | Two structures in one type describe different states | HEU-750 | |
| The graph is wrong | An edge, a root, or a cycle | HEU-732, HEU-712, HEU-744, HEU-734 | |
| A shape is violated | One structure breaks a rule the engine's own mutators keep | HEU-760, for a gap above an occupant | |

The third is the one that hides. Nothing disagrees with anything, so a
structure-to-structure check passes and the payload looks consistent.

HEU-706 closed two instances before anyone named the class. `total_positions` is
recomputed from width and height rather than trusted, because a cached count is a
value the engine keeps correct and a caller does not. `next_stream_id` must be
past the highest live key, for the same reason.

The question to ask of any field on a restorable type: does a mutator maintain a
property of this that the type system does not? If so, a restore does not run
that mutator.

## A Test Helper That Does Not Call The Thing Under Test

`assert_board_invariants` checked three properties by hand: map key equals board
id, no occupant on two boards, every occupant indexed. Three of the guards in
`validate_restored` check the same three things.

It never called `validate_restored`.

So the constraint that the validator must accept engine output was asserted by
nothing, and three of the guards had never run against a real engine at all.
The helper looked like coverage and reported on something else.

If a helper exists to prove a function behaves on real data, it has to call that
function. Restating its checks by hand proves the restatement.

## A Protocol Bump Is More Sites Than It Looks, And The Last Bump's Commit Is A Bad Guide To Them

Moving `PROTOCOL_VERSION` means changing every site in one commit. A partial
move rejects a worker that is otherwise correct, because `engine_client.go`
compares the version by exact equality.

Treat the sites below as a starting point rather than a checklist. The list
goes stale the first time a site is added or dropped, and it will do so quietly,
because nothing checks the list against the tree.

The literals live in `network-engine-worker/src/protocol.rs`,
`network-engine-worker/tests/worker_integration.rs`,
`engine/testdata/contracts/ping.json`, `internal/networkengine/engine_client.go`,
and more than once in `internal/networkengine/transport_test.go`.

The version history lives in the Version allocation table of
`contracts/ndjson-engine-protocol.md` in the hub repository, not this one. The
`Moved to N by ...` block above the Go constant is not extended. The prose to
check is the ticket list in
`content/design-rationale/017-commission-calculation-architecture.md`, when the
change is one that note tracks.

The other `protocol_version` literals in `transport_test.go` are stub-worker
payloads that never reach the handshake. They stay.

Deriving the list from the files the previous bump touched gives a wrong answer.
The version-8 commit also changed `DEVELOPMENT.md` and `019-ndjson-protocol.md`,
for key-order wording rather than the number. Grep the tree for the outgoing
number instead.

### The field that justified the bump has to be required on decode

A bump is a claim that the wire means something new. If the new field decodes to
a usable zero value when it is missing, nothing enforces the claim, and a worker
that dropped the field looks like a worker with nothing to say.

`encoding/json` leaves a `[]T` field nil both for an absent key and for an empty
list. Those are different claims. An empty list says the worker looked and found
nothing, which a caller can act on. A missing key says nothing at all. Decode
such a field into a `*[]T` and reject nil, so silence fails loudly instead of
becoming the one answer that requires no work.

This is the same technique the `ping` handshake uses on `protocol_version`, and
it is worth reaching for on any field whose presence is the point.

## A Canned Mock Response Answers Every Op, Including The One You Just Changed

`recordingTransport` in `tree_consumer_test.go` holds a single `response` field
and returns it for every operation. It is `{"ok":true}` unless a test sets it.

That makes a consumer test read as coverage of whatever the engine call returns,
when it is really coverage of one fixed payload. A response field added to one
operation is absent from every one of those tests and nothing says so. The
removal test decoded a reply with no `responsored` key and passed, until strict
decode turned the silence into a failure.

When you add a field to one operation's response, set the response on the tests
that exercise that operation. A green suite is not evidence that they carry it.

## A Matrix Fixture Cannot Put A Recruit Outside Its Sponsor's Subtree With `add_node`

`add_node` picks the slot by spillover search starting at the sponsor's own
index, so the new node always lands below the sponsor. No argument changes that.

A test that needs a recruit whose placement sits outside the sponsor's subtree
has to build it with `add_node_at`, which takes the parent and slot explicitly.
The case matters because it is the one where a holding-tank removal takes the
recruit but not the sponsor, which is what makes the multi-step sponsor walk
load-bearing.

Fixtures written on `add_node` for this case are not merely awkward. They cannot
express the state, and they look like they do.

## A Matrix Removal Projects As One Transaction In Three Steps

A matrix removal can tombstone a whole subtree and move nodes that were not removed. The worker reports both. `removed` and `moved_to_tank` name who left. `relocated` names every survivor whose parent, slot or depth changed, and where it now sits. `responsored` names the recruits whose sponsor the removal changed, and their new sponsor.

`ProjectRemoval` writes all of it in one transaction, in this order:

1. Tombstone and stamp every removed user's row.
2. Repoint every re-sponsored row.
3. Move every relocated row.

The order matters. `idx_tree_nodes_tree_parent_position_active` is a unique index that is not deferrable, and Postgres checks it row by row. A promoted node takes the slot its removed parent held. That slot is free only after step 1.

Inside step 3 the row order is undefined. So a relocation that changes parent or slot has to target a slot that is free after step 1 and that no other relocation vacates. A relocation that changes only depth keeps its own slot, and no other relocation targets it. The Rust property test in `matrix_properties.rs` checks that against a model of the slot map. `TestTreeConsumer_SeededMatrixRemovalsKeepEveryRowOnTheEngine` checks it against the real index.

Each step is one set-based statement, to keep a large removal inside the store's write timeout.

The store does not show the holding tank. The engine rebuilt by replay or from a snapshot does.

The engine refuses to place a user who is in the holding tank. `add_root`, `add_node`, `add_node_at` and a `check_mutation` for `add_root` or `add_node_at` answer `USER_IN_HOLDING_TANK` (HEU-930). `place_from_tank` is the only way out of the tank, and the event path has no event for it.

## Rounding A Rounded Quotient Can Invent A Tie

`checked_div` already rounds, at whatever scale its 96-bit coefficient allows. Rounding that result again at 13 places gives the right answer in almost every case. It goes wrong when the first rounding lands exactly on a 13-place midpoint. The true quotient was a little above or below that midpoint, but the second rounding sees a tie and breaks it to even. That can pick the wrong neighbour.

E2 sends any quotient that lands on a 13-place midpoint to the wide path, which rounds once from the exact quotient (HEU-969).

When testing a rounding step that follows another rounding, build fake ties in both directions and both signs. A true value just above the midpoint and one just below must round apart.

## `arbitrary_precision` Changes Some Serde Error Text

The engine enables serde_json's `arbitrary_precision` feature, so it can read numbers exactly. That changes some error messages:

- A bare number where an internally tagged enum is expected reports ``missing field `type` ``. Before the feature was on, the message named the enum (HEU-988).
- serde_json re-spells `1e15` as `1e+15` before `AuthoredDec` sees the text. A refusal then quotes `1e+15`, which the caller never wrote (HEU-985).

Assert the text you observe.
