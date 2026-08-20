# Network Engine Use-Cases

Use-cases for the Network Engine bounded context.

## Table of Contents

- [UC-NET-001: Pass-up skip set precomputation](#uc-net-001-pass-up-skip-set-precomputation)
- [UC-NET-002: Snapshot persistence constraint](#uc-net-002-snapshot-persistence-constraint)
- [UC-NET-003: Generation boundary counting via breakaway_set](#uc-net-003-generation-boundary-counting-via-breakaway_set)
- [UC-NET-004: Per-earner thresholds via filter-before-emit](#uc-net-004-per-earner-thresholds-via-filter-before-emit)
- [UC-NET-005: Bottom-up rank evaluation with accumulating descendant context](#uc-net-005-bottom-up-rank-evaluation-with-accumulating-descendant-context)
- [UC-NET-006: Per-leg structural rank qualification](#uc-net-006-per-leg-structural-rank-qualification)
- [UC-NET-007: Integer-keyed BTreeMap inside an internally-tagged serde enum](#uc-net-007-integer-keyed-btreemap-inside-an-internally-tagged-serde-enum)
- [UC-NET-008: Per-period rank result persistence](#uc-net-008-per-period-rank-result-persistence)
- [UC-NET-009: Windowed and tenure rank-qualification gates](#uc-net-009-windowed-and-tenure-rank-qualification-gates)
- [UC-NET-010: Periodic rank-evaluation driver](#uc-net-010-periodic-rank-evaluation-driver)
- [UC-NET-011: Cross-language integer width contract](#uc-net-011-cross-language-integer-width-contract)
- [UC-NET-012: Preflight validation before an irreversible mutation](#uc-net-012-preflight-validation-before-an-irreversible-mutation)
- [UC-NET-013: Dependency-aware replay ordering over multiple edge types](#uc-net-013-dependency-aware-replay-ordering-over-multiple-edge-types)
- [UC-NET-014: Pre-projection event gate with database backstop](#uc-net-014-pre-projection-event-gate-with-database-backstop)
- [UC-NET-015: Immutable run registry with a visibility-flip results store](#uc-net-015-immutable-run-registry-with-a-visibility-flip-results-store)
- [UC-NET-016: Removing a wire field without a red interval](#uc-net-016-removing-a-wire-field-without-a-red-interval)
- [UC-NET-017: Reading a nil caller collection as empty](#uc-net-017-reading-a-nil-caller-collection-as-empty)

---

### UC-NET-001: Pass-up skip set precomputation

**Added:** 0.x (HEU-23)
**Files:** `engine/network-engine/src/commission/walk.rs`

**Problem:** Australian X-Up requires skipping distributors in the commission walk for volume from their first N sponsored recruits. The skip decision depends on both the current node AND the volume source, unlike compression which is per-node only.

**Solution:** `build_pass_up_context()` precomputes a `PassUpContext` containing `HashMap<Uuid, HashSet<Uuid>>` — maps each distributor to the set of source IDs that should trigger skipping. Enrollment order is determined by sorting `get_sponsored()` results by `Node.enrolled_at` with UUID tiebreak. When `includes_commissions = true`, the skip set expands to include the full subtree via `get_downline(recruit, 0)`.

**Usage:**
```rust
let ctx = walk::build_pass_up_context(tree, &pass_up_config, &participant_ids);
// Pass to LevelWalkConfig as pass_up: Some(&ctx)
// Walk loop checks ctx.skip_sets.get(&node.user_id).contains(&source.source_id)
```

**Notes:** The generation calculator (HEU-11) does not use pass-up. It passes `pass_up: None` to the level walk config because generation plans define boundaries by rank, not enrollment order. When testing pass-up with plan-level config, avoid linear chain trees — every node's single child gets passed up, causing cascading skips. Use wider trees with multiple children per node.

---

### UC-NET-002: Snapshot persistence constraint

**Added:** 0.x (HEU-30)
**Files:** `engine/network-engine/src/tree/`, `engine/network-engine/src/board_plan/`

**Problem:** Board plan engines manage many small boards with cycling history. Full event replay becomes expensive over time. All tree-layer data structures must support serialization for snapshot persistence.

**Solution:** All types in the tree layer (Arena, Node, UnilevelTree, BinaryTree, MatrixTree, BoardPlanEngine, Board) derive `serde::Serialize` and `serde::Deserialize`. Snapshot persistence uses `serde_json`. Go handles storage scheduling and recovery flow (restore snapshot, replay events after snapshot sequence number).

**Usage:**
```rust
// Serialize full engine state
let snapshot = serde_json::to_string(&board_plan_engine)?;
// Deserialize on recovery
let engine: BoardPlanEngine = serde_json::from_str(&snapshot)?;
```

**Notes:** Adding a non-serializable type (function pointers, file handles, runtime-only state) to any tree-layer struct breaks snapshot persistence. This is a breaking change. See design-rationale 023.

---

### UC-NET-003: Generation boundary counting via breakaway_set

**Added:** 0.x (HEU-11)
**Files:** `engine/network-engine/src/commission/generation.rs`

**Problem:** The standalone generation calculator needs to walk upward through the tree counting rank boundaries. The `count_generations_upward()` utility already does this for stairstep Walk 2 generation overrides, but its `breakaway_set` parameter is named for the stairstep context.

**Solution:** Reuse `count_generations_upward()` directly. Map boundary-rank nodes to the `breakaway_set` parameter. The `boundary_check` closure controls whether ineligible nodes create boundaries (`ineligible_creates_boundary` flag). For ThresholdRank mode, one boundary set serves all sources. For SameRank mode, a separate boundary set is built per unique `(rank_name, ordinal)` pair, and results are filtered to earners at exactly that ordinal. The rank name is preserved alongside the ordinal so the per-walk termination depth can resolve via `earner_max_generations` (see UC-NET-004).

**Usage:**
```rust
// ThresholdRank: one boundary set for all sources, one walk per source.
// Walk depth is the deepest configured cap (walk_depth helper); per-earner
// filtering happens after the walk (see UC-NET-004).
let boundary_set: HashSet<Uuid> = snapshots.iter()
    .filter(|(_, snap)| rank_ordinals.get(snap.rank.as_str()).copied().unwrap_or(0) >= threshold)
    .map(|(id, _)| *id)
    .collect();

let entries = count_generations_upward(tree, source_id, &boundary_set, &boundary_check, walk_depth(cfg), empty_consumes);

// SameRank: per-(rank_name, ordinal) walks with exact-ordinal filtering.
// Each walk's termination depth is the per-rank cap (earner_max_generations).
for &(rank_name, ordinal) in &unique_ranks {
    let walk_max = earner_max_generations(rank_name, cfg);
    let boundary_set = /* nodes >= ordinal */;
    let entries = count_generations_upward(tree, source_id, &boundary_set, &check, walk_max, empty_consumes);
    let filtered = entries.into_iter().filter(|e| earner_ordinal(e) == ordinal).collect();
}
```

**Notes:** The `breakaway_set` name is a semantic mismatch documented in design-rationale 024. If a third consumer appears (HEU-288 infinity commission mode), extract a shared interface with a cleaner name. The project's three-case abstraction threshold applies. Update: a third consumer arrived in HEU-428 — `walk_multi_tier_overrides` in `commission/stairstep.rs` uses `count_generations_upward` with `|_| true` for boundary detection. The abstraction trigger is technically met but the semantic-mismatch payoff is muted for this consumer (its boundary set IS the breakaway set). Defer the rename until HEU-288 or a future consumer makes the rename pay. The `(rank_name, ordinal)` dedup key relies on rank ordinals being unique across the rank ladder; HEU-440 tracks adding upstream validation.

---

### UC-NET-004: Per-earner thresholds via filter-before-emit

**Added:** 0.x (HEU-425)
**Files:** `engine/network-engine/src/commission/generation.rs`

**Problem:** Some plans configure per-earner thresholds on a shared walk (e.g., per-rank generation depth where silver earners cap at 2 generations and diamond earners cap at 7). Repeating the walk per earner is wasteful and complicates the call graph.

**Solution:** Walk once to the deepest configured cap, then filter the emitted entries against each earner's own cap before commission emission. Two helpers cooperate:
- `earner_max_generations(rank, cfg)` returns the per-earner cap with default fallback
- `walk_depth(cfg)` returns the deepest cap any earner needs (max of default and all per-rank values)

The filter sits between the walk primitive and `emit_*_earnings`. Look up the earner's rank in the snapshot map, resolve the cap via the helper, admit entries where `entry.generation <= cap`. The walk primitive (`count_generations_upward`) is unchanged — the filter is purely at the call site.

**Usage:**
```rust
// ThresholdRank: walk once to the deepest cap, then filter per-earner
let gen_entries = count_generations_upward(
    tree, source.source_id, &boundary_set, &boundary_check,
    walk_depth(cfg), cfg.empty_generation_consumes_number,
);
let filtered: Vec<_> = gen_entries.into_iter()
    .filter(|entry| {
        let rank = snapshots.get(&entry.earner_id).map(|s| s.rank.as_str()).unwrap_or("");
        entry.generation <= earner_max_generations(rank, cfg)
    })
    .collect();
emit_generation_earnings(&filtered, source, cfg, /* ... */);
```

**When to use this pattern:**
- The walk is shared across earners (one walk per source, not per earner)
- Per-earner thresholds vary based on earner attributes (rank, status, etc.)
- The threshold is a one-sided cap (admit if `value <= cap`), not a complex predicate

**When NOT to use this pattern:**
- The walk can be naturally partitioned by the threshold attribute (e.g., SameRank already walks per rank ordinal — thread the cap into walk termination instead, like UC-NET-003 SameRank example)
- The threshold requires walking deeper than otherwise necessary AND most earners have low caps (you'll do extra walk work for entries that get trimmed)

**Notes:** The defensive `unwrap_or("")` for missing snapshots is unreachable in current code (the boundary set is built from snapshot keys), but is forward-compatible. If an internal invariant is broken upstream, prefer `expect("...")` to fail loudly. Empty per-rank maps must produce identical results to the absent-field case — regression-guarded by `calculate_generation_empty_per_rank_map_preserves_*_behavior` tests.

---

### UC-NET-005: Bottom-up rank evaluation with accumulating descendant context

**Added:** 0.x (HEU-443), fixpoint iteration added in HEU-460
**Files:** `engine/network-engine/src/rank/evaluator.rs`, `engine/network-engine/src/rank/mod.rs`, `engine/network-engine/src/rank/predicates.rs`

**Problem:** Per-period rank evaluation needs a `DistributorCountRequirement` predicate that counts downline distributors whose evaluated rank meets a threshold (`min_rank`). The descendants haven't been evaluated yet when the ancestor is processed top-down, so the count is always zero.

**Solution:** `evaluate_ranks` iterates to a fixpoint. `iterate_to_fixpoint` re-runs evaluation passes over an accumulating `already: HashMap<Uuid, EvaluatedRank>` until a pass changes no rank. Each `evaluate_distributor` call reads descendants' computed ranks from `already` and writes its own result back. Rank evaluation is monotone, so iteration from an empty map converges to the least fixpoint — a unique result independent of pass order, including for plans with circular cross-tree ancestry. `evaluation_order_for_users` (deepest-first, max depth across trees, UUID tiebreak) is retained as a heuristic that minimizes pass count. The final HashMap is moved into a `BTreeMap<Uuid, EvaluatedRank>` so JSON serialization emits user-id keys in ascending order (NFR2).

**Usage:**
```rust
// engine-side
let result = network_engine::rank::evaluate_ranks(&plan, &trees, &inputs)?;
// result.ranks: BTreeMap<Uuid, EvaluatedRank>
//   - Qualified { rank: "silver", ordinal: 2 }
//   - Unranked

// Go-side via EngineClient
result, err := client.EvaluateRanks(ctx, networkengine.EvaluateRanksRequest{...})
// result.Ranks: map[string]EvaluatedRankDTO with kind-tagged JSON
```

**When to use this pattern:**
- A predicate needs to inspect already-computed values of descendants (rank, status, etc.)
- The walk order is deterministic and the function returns must be deterministic (NFR2)

**When NOT to use this pattern:**
- The predicate only inspects the user's own primitives (use a simpler top-down pass)
- The result needs to depend on ancestors' evaluations (no current predicate reads upward, so the pattern does not serve this today — but the fixpoint `already` map does hold ancestor ranks, so downward-only walking is a predicate convention, not a loop limitation)

**Notes:** The ladder-ascent semantics inside `evaluate_distributor` iterate every rank in ascending ordinal and pick the highest passing one. A failed rank does NOT short-circuit — higher ranks may still pass and be selected. This handles ladder gaps where a distributor satisfies rank N+1 but not rank N (e.g., missing a required product unique to N).

The handler at `engine/network-engine-worker/src/handlers/rank.rs` only registers tree navigators for structures referenced by at least one rank's `qualification.structures`. An empty list yields an empty result map — see the network-engine development note "Rank Evaluation: Empty `qualification.structures`" for the workaround pattern in tests.

---

### UC-NET-006: Per-leg structural rank qualification

**Added:** 0.x (HEU-444)
**Files:** `engine/network-engine/src/rank/predicates.rs`, `engine/network-engine/src/config/rank.rs`

**Problem:** A rank can require N frontline legs that each contain a qualifying node. For example "3 legs each containing a Gold distributor" or "2 legs each with a 200+ personal-volume distributor". `DistributorCountRequirement` (UC-NET-005) counts the flat downline and cannot express a per-leg structural requirement.

**Solution:** `leg_quality_meets` evaluates a `Vec<LegQualityRequirement>` against a distributor's frontline legs. A "leg" is a direct child plus that child's entire subtree. For each requirement it counts the legs whose subtree contains a node matching the requirement's `LegPredicate`. It passes only when every requirement's leg count is met, so requirements are AND-combined. Three single-responsibility functions split the work: `leg_quality_meets` does per-requirement leg counting, `leg_contains_match` scans one leg's subtree and short-circuits on the first match, and `node_matches` tests one node against `ContainsRank` or `ContainsPersonalVolume`. It is wired into `satisfies` next to `distributor_count`, and is the sibling predicate of `distributor_count_meets`.

**Usage:**
```rust
// StructureQualification.leg_quality: Vec<LegQualityRequirement>
// LegQualityRequirement { count: u16, predicate: LegPredicate }
// LegPredicate::ContainsRank { min_rank } | LegPredicate::ContainsPersonalVolume { min_personal_volume }
//
// satisfies() calls leg_quality_meets alongside distributor_count_meets.
// Both AND-combine into the structure's qualification result.
```

**When to use this pattern:**
- A rank criterion is about the shape of individual legs, not the flat downline total
- The criterion is "at least N legs that each satisfy a per-leg condition"

**Notes:** `ContainsRank` reads each node's evaluated rank from the `already` map, so it depends on UC-NET-005's fixpoint rank evaluation having populated that map. An empty `leg_quality` is an exact no-op. It makes no tree calls, which preserves pre-feature behaviour (BR5). `ContainsRank`'s `min_rank` is validated in the Go layer (`validateRanks` in `rules.go`) for an undefined or non-lower-ordinal reference. `node_matches` raises `UnknownMinRank` as defense in depth.

---

### UC-NET-007: Integer-keyed BTreeMap inside an internally-tagged serde enum

**Added:** 0.x (HEU-428)
**Files:** `engine/network-engine/src/config/stairstep.rs`

**Problem:** When a Rust type with `#[serde(tag = "type")]` nests another type whose field is `BTreeMap<u8, _>` (or any integer-keyed map), `serde_json`'s string-to-int key coercion is stripped by the internally-tagged enum's `Content` intermediate buffer. JSON like `{"1": 0.05}` no longer deserializes into the map. The inner type passes its own tests in isolation; the bug only appears at the tagged-enum boundary.

**Solution:** Add a `#[serde(deserialize_with = "...")]` helper on the field that goes through `BTreeMap<String, _>` first and parses the keys:

```rust
fn deserialize_u8_keyed_rates<'de, D>(d: D) -> Result<BTreeMap<u8, f64>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let raw: BTreeMap<String, f64> = BTreeMap::deserialize(d)?;
    raw.into_iter()
        .map(|(k, v)| k.parse::<u8>().map(|k| (k, v)).map_err(serde::de::Error::custom))
        .collect()
}
```

The same `deserialize_with` precedent exists in `engine/network-engine/src/config/bonus.rs`.

**When to use this pattern:**
- A type owns a `BTreeMap<Int, _>` field that needs to accept JSON string keys.
- That type is nested inside an internally-tagged enum (`#[serde(tag = "type")]`).

**Notes:** Discovered in HEU-428 Task 2 when restructuring `BreakawayConfig` into an `OverrideStrategy` tagged enum. Without the helper, the existing `deserialize_breakaway_config` test broke even though the JSON shape was the migrated equivalent of the original. Rejected alternatives: pulling `serde_json` into main deps (heavier surface change), a `Value`-based manual deserialize (requires the dep), and a hand-rolled `Visitor` (verbose).

---

### UC-NET-008: Per-period rank result persistence

**Added:** v0.0.1 (HEU-445)
**Files:** `internal/networkengine/qualification_history_store.go`,
`internal/networkengine/qualification_history_store_postgres.go`,
`internal/networkengine/engine_client.go`

**Problem:** Per-period rank evaluation results need durable storage so windowed and tenure predicates can read history across periods, without changing the Rust engine's stateless wire protocol.

**Solution:** Opt-in `WithPersistence(periodID, store)` option on `EngineClient.EvaluateRanks`. Writes use DELETE-then-`pgx.CopyFrom` inside one transaction so re-evaluation completely replaces a period (BR5). PK is `(period_id, user_id)`; secondary `(user_id, period_id)` index serves multi-period reads.

**Usage:**
```go
store := networkengine.NewPostgresQualificationHistoryStore(pool)
result, err := client.EvaluateRanks(ctx, req,
    networkengine.WithPersistence("2026-05", store))
```

**Notes:** `period_id` is opaque and compared lexicographically. Callers must zero-pad so widths sort correctly. Missing rows mean "not evaluated"; rows with `rank IS NULL` mean "evaluated, did not qualify" (Unranked). On store-write failure the engine result is still returned alongside a wrapped error (NFR4: prior period rows survive). The `qualificationHistoryCopySource` is the reference adapter for future bulk-write callers in this codebase. HEU-516 added `GetByUsersAndPeriodRange`, a batched read that returns a distributor set's rows across a period range in one index-served query.

---

### UC-NET-009: Windowed and tenure rank-qualification gates

**Added:** v0.0.1 (HEU-446)
**Files:** `engine/network-engine/src/rank/predicates.rs`, `engine/network-engine/src/config/rank.rs`, `engine/network-engine/src/rank/evaluator.rs`, `internal/networkengine/history_window.go`, `internal/config/rules.go`

**Problem:** A rank can require sustained past performance, not just current-period criteria. Two shapes: "achieved >= rank R in N of the last M periods" (windowed, G2) and "held >= rank R for X consecutive periods" (tenure, G13). The current-period evaluator (UC-NET-005) only sees the current snapshot.

**Solution:** Two optional, additive gates on `RankQualification` — `window: Option<RankQualificationWindow>` and `tenure: Option<TenureRequirement>` — read a caller-supplied prior-period achieved-rank history. Go's `BuildHistoryWindow` fetches the window from the `QualificationHistoryStore` (UC-NET-008), pivots it per-distributor, and passes it into the stateless `evaluate_ranks` op as `history_window` (most-recent-first axis) plus `history`. The Rust evaluator reads both through an `EvalCtx` bundle. `windowed_meets` counts at-or-above-threshold periods in the M most-recent and passes at N; `tenure_meets` requires the X most-recent to be consecutively at-or-above. Both AND-combine with base criteria in `satisfies`.

**Usage:**
```rust
// RankQualification.window: Option<RankQualificationWindow { threshold_rank, qualifying_periods: u8, window_periods: u8 }>
// RankQualification.tenure: Option<TenureRequirement { threshold_rank, periods: u8 }>
// satisfies() AND-combines window + tenure with the structure/base criteria.
```
```go
// axis is most-recent-first (period_id DESC); BuildHistoryWindow pivots store rows to per-distributor history.
_, hist, _ := networkengine.BuildHistoryWindow(ctx, store, []uuid.UUID{u}, axis)
res, _ := client.EvaluateRanks(ctx, networkengine.EvaluateRanksRequest{ /* ... */ HistoryWindow: axis, History: hist })
```

**When to use this pattern:**
- A rank gate depends on prior-period achieved rank, not just the current snapshot.
- The gate is "N of the last M" (windowed) or "X consecutive" (tenure).

**Notes:** A missing history key or an Unranked (`null`) period both count as below-threshold (BR6). An insufficient axis (shorter than the window) fails the gate (BR5). The threshold rank may be equal or higher ordinal than the current rank — unlike `min_rank`, there is no lower-only rule. Validation (N>=1, M>=1, N<=M, threshold existence) lives in Go `validateRanks`; `MaxHistoryDepth` sizes the axis. The fail-loud empty-axis guard (`TimeGateWithoutHistory`, BR9) is engine-side because the Go client has no plan. Builds on UC-NET-008 (persistence). The periodic driver that generates `period_id`s is HEU-501. `BuildHistoryWindow`'s fetch is now a single bounded `GetByUsersAndPeriodRange` over the requested distributors and axis range (HEU-516), replacing the original per-period `GetByPeriod` fan-out, which fetched every distributor's row for each period and discarded all but the requested distributors'.

---

### UC-NET-010: Periodic rank-evaluation driver

**Added:** v0.0.1 (HEU-501)
**Files:** `internal/networkengine/rank_driver.go`,
`internal/networkengine/period_input_provider.go`,
`internal/period/period.go`

**Problem:** Windowed and tenure gates (UC-NET-009) need per-period rank history to exist — something must evaluate and persist each period in order, supply the prior-period axis, and backfill a range, without the Rust engine (which is stateless and has no clock).

**Solution:** `RankDriver` composes the existing pieces for one period: `config.MaxHistoryDepth` sizes the axis, `period.Sequence.PriorLabels` builds the strictly-prior DESC axis, `BuildHistoryWindow` (UC-NET-008) fetches it, and `EvaluateRanks(WithPersistence)` evaluates and persists. `Backfill` loops the same call oldest-first so each period reads the rows the earlier ones just wrote. Per-period distributor inputs come from an injected `PeriodInputProvider` (the seam for a real volume/order source). The pure `internal/period` package turns a plan's `PeriodConfig` into ordered, sortable `period_id` labels via date-only civil-date math (week, semi-month, month, quarter).

**Usage:**
```go
driver, _ := networkengine.NewRankDriver(client, store, plan, provider)
// One period (asOf falls anywhere inside it):
_, _ = driver.EvaluatePeriod(ctx, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
// Backfill a contiguous range, oldest-first, accumulating history:
_ = driver.Backfill(ctx, from, to)
```

**When to use this pattern:**
- You need to populate or backfill `qualification_history` so windowed/tenure gates have an axis to read.
- You need ordered, sortable `period_id`s from a plan's period config.

**Notes:** The driver normalizes nil `distributors`/`volume_sources`/`active_products` to `{}`/`[]` before the engine call. Since HEU-626 the engine reads an explicit `null` as empty on all three, so that normalization is belt and braces rather than a requirement — it only ever bound Go callers, and a non-Go client sending `null` is now fine (see UC-NET-017 and `docs/development/network-engine.md`). A no-gate plan (depth 0) sends no history axis. `Backfill` is fail-stop: the first failing period stops the run and is named; earlier periods stay persisted and re-running replaces them (full-replacement `SaveResult`), so retry is safe. Evaluating a period before the plan's start is rejected (BR9). Single-plan/context assumption: `qualification_history` PK is `(period_id, user_id)` with no plan/tenant scope (multi-plan scoping is HEU-506). Builds on UC-NET-008 (persistence) and UC-NET-009 (gates).

---

### UC-NET-011: Cross-language integer width contract

**Added:** v0.0.1 (HEU-513)
**Files:** `engine/testdata/config_contract/width_manifest.json`,
`internal/config/width_contract_test.go`,
`engine/network-engine/tests/config_width_contract.rs`,
`internal/config/genfixtures_test.go`

**Problem:** The compensation-plan config contract is hand-maintained in three places — `schemas/compensation-plan.schema.json`, Go `internal/config`, and the Rust engine config. A Go `int` mirroring a Rust `u8` accepts 300, then fails deep inside the engine or truncates. Nothing caught the drift, and nothing stopped a new field from reintroducing it.

**Solution:** `width_manifest.json` is the single list of every narrow-mirror field. Each entry carries `go_type`, `rust_type`, `over_max`, and three RFC 6901 pointers — `schema_pointer` (schema), `go_pointer` (authoring shape), `engine_pointer` (post-`translateToEngine` wire shape) — so each layer mutates one field of a real payload in its own shape. Five tests read it. Go: `TestConfigContract_FieldsMatchAndRejectOverMax` (the declared Go type still matches the manifest, and `over_max` is rejected by the real two-pass decode), `TestConfigContract_SchemaMaxWithinType` (the schema's `maximum` fits the type's capacity), `TestConfigContract_NoUntypedIntFields` (package-wide AST scan — any signed int field in `internal/config` must be manifested or allow-listed with a reason), and `TestConfigContractFixturesMatchPipeline` (live `translateToEngine` output byte-equals each committed fixture). Rust: `rust_config_rejects_over_max_widths` deserializes each engine fixture pristine first, then sets one field to `over_max` and requires serde to reject.

**Usage:**
```bash
# After adding a config field that mirrors a narrow Rust type:
# 1. Add a manifest entry (or an allow_list entry with a reason).
# 2. Regenerate the engine fixtures:
REGEN_FIXTURES=1 go test ./internal/config/ -run TestGenerateConfigContractFixtures
# 3. Run both halves of the contract (cargo must run from engine/ — no root Cargo.toml):
go test ./internal/config/ && (cd engine && cargo test --test config_width_contract)
```

**When to use this pattern:**
- You are adding a config field that mirrors a narrow Rust type.
- A new integer field fails `TestConfigContract_NoUntypedIntFields`.

**Notes:** Completeness is enforced Go-side only — the Rust test pins known fields, and the AST scan is what stops new drift. That scan is AST-only, so a signed int hidden behind a named type (`type Depth int32`) or an embedded field slips past; catching those would need a `go/types`-based scan. Commission fields sit behind `CommissionRaw` and only decode into their typed struct during `resolveCommissions`, which is why the Go test uses the real two-pass decode — unmarshalling the plan alone never exercises them. The decode stops before business validation deliberately, so a rejection proves the field's *type* rejected the value rather than some business rule capping it. Both boundary tests fail loudly on an unresolvable pointer instead of passing vacuously. `TestConfigContractFixturesMatchPipeline` is what keeps the Rust half honest: the fixtures are only REGEN-written, never compared, so without the golden check a `translate.go` regression would desync them from real output while the Rust test read stale files and stayed green. A field with `omitempty` (e.g. `board_cycling.max_cascade_depth`) must keep a non-default value in its fixture or its `engine_pointer` will not resolve. Go struct names do not map to Rust struct names — `UnilevelCommission.CommissionableDepth` is Rust `LevelCommissionConfig.max_depth`, and `BoardCyclingConfig` lives in two Rust modules — which is why the Rust side deserializes the whole plan instead of mapping per-struct. `REGEN_FIXTURES` must be exactly `1`. The parallel scan for wire DTO fields in `wire_types.go` is not built yet (HEU-544). See `docs/development/config-types.md`.

---

### UC-NET-012: Preflight validation before an irreversible mutation

**Added:** Unreleased (HEU-534, refined HEU-566)
**Files:** `internal/networkengine/tree_loader.go`

**Problem:** `LoadTree` replays a persisted tree into the Rust worker one node at a time, but the worker has no operation to remove a structure — so a replay that fails partway leaves a half-built tree that cannot be dropped or retried until the process restarts.

**Solution:** Prove the input is reconstructable *before* the first call to the external system, in two phases split by what they read. `validateTreeConfig` checks configuration — tree type, matrix width and spillover — and reads no rows, so it runs before the store query and a misconfigured load costs no query. `validateNodes` then checks the node set — duplicate IDs, exactly one depth-0 root, root parent and position, self-references, reference existence, depth consistency, slot occupancy — and `orderForReplay` proves a workable order exists. Only then does the first mutation happen. Replay failures also report how far they got, because with no rollback that count is the operator's only recovery signal. Every exit after the create says what survived it, not just the replay loop: a failed `AddRoot` reports that the tree was created but left empty, since the worker has no operation to drop it (HEU-557) and only a process restart clears it.

**Usage:**
```go
// Configuration first. It reads no rows, so it runs above the query and an
// empty tree still reports a bad tree type or missing matrix params.
if err := validateTreeConfig(treeID, treeType, cfg); err != nil {
    return err
}
nodes, err := l.store.GetByTreeDepthOrdered(ctx, treeID)
if err != nil {
    return fmt.Errorf("load tree %s: %w", treeID, err)
}
if len(nodes) == 0 {
    return nil
}
// Then the node set. Every detectable fault fails here, engine untouched.
if err := validateNodes(treeID, treeType, cfg, nodes); err != nil {
    return err
}
ordered, err := orderForReplay(treeID, nodes)
if err != nil {
    return err
}
// First mutation only after both phases pass.
if err := l.engine.CreateMatrixTree(ctx, treeID, cfg.matrixWidth, cfg.matrixSpillover); err != nil {
```

**Notes:** The pattern generalises to any external system with no undo: a worker without a delete op, an API without a rollback, a third party that charges on first call. Two things make it worth the duplication of engine rules in Go. First, the validation must mirror what the remote actually enforces, so it is only as good as that audit — `TestTreePersistence_RejectedTreeLeavesEngineLoadable` proves the guarantee against the real worker rather than a stub, and asserts a *corrected retry succeeds*, which is the part no fake can demonstrate. Second, the mirroring drifts silently if the remote adds a rule; the Rust side keeps its own runtime checks, so preflight is a second line of defence rather than a replacement. Deliberately not covered: divergence between what the store recorded and what the remote actually did — both can be internally consistent, so no read-side validation can detect it. HEU-553 closed the write-side contract for placement; removal still diverges (HEU-582), and the type-label trust boundary (HEU-554) and redelivery idempotency (HEU-576) remain open. Related: UC-NET-013, which supplies the ordering half.

One ordering trap the split exists to avoid: an empty-input short circuit placed above the configuration checks hides configuration errors for as long as the input stays empty. HEU-566 found `LoadTree(ctx, id, "matrix")` with no `WithMatrixParams`, and `LoadTree(ctx, id, "streamline")` with an unsupported type, both returning nil on a zero-row tree. Configuration describes the structure, not its contents, so its validity never depends on row count. Put the config phase above both the query and the empty check.

---

### UC-NET-013: Dependency-aware replay ordering over multiple edge types

**Added:** Unreleased (HEU-534, HEU-561)
**Files:** `internal/networkengine/tree_loader.go`

**Problem:** Replaying stored nodes in depth order satisfies parents, because a parent is always shallower — but every tree type also resolves the *sponsor* at insert time and rejects one that is not present yet. Automatic spillover always places a recruit below their sponsor, so depth order happened to work; explicit placement removes that coincidence, and a node placed shallower than its own sponsor then fails mid-replay.

**Solution:** Kahn's algorithm over a min-heap, with an edge for each dependency kind. `orderForReplay` counts each node's unmet dependencies (parent *and* sponsor), seeds the heap with the zero-dependency nodes, and pops in `(depth, enrolled_at, user_id)` order so the result is deterministic regardless of how the store broke ties. If nodes remain unemitted, `cycleError` walks the residual graph to a concrete cycle and names it.

**Usage:**
```go
ordered, err := orderForReplay(treeID, nodes)
if err != nil {
    return err // names a real cycle, e.g. "z1 -> z2 -> z1"
}
root := ordered[0] // the only zero-dependency node, provably
for _, node := range ordered[1:] {
```

**Notes:** Two non-obvious parts. The root's stored sponsor must **not** be treated as a dependency — `AddRoot` takes no sponsor, so doing so makes the root wait on a node downstream of itself and rejects the whole tree as a cycle. And when reporting a cycle, do not print the stuck set: it is the cycle members *plus everything blocked behind them*, and the blocked nodes usually dominate, so any prefix names only bystanders. Walk to an actual cycle instead — every stuck node has an unmet dependency that is itself stuck, so following them from any stuck node closes a loop within `len(stuck)` steps, in O(n) and without a DFS. The counter formulation also makes duplicate edges harmless: double-counting appends to `dependents` twice, so both decrements land. Related: UC-NET-012, which supplies the validation half and guarantees the preconditions this relies on.

---
### UC-NET-014: Pre-projection event gate with database backstop

**Added:** Unreleased (HEU-553)
**Files:** `internal/networkengine/tree_consumer.go`, `migrations/000004_add_tree_nodes_slot_unique.up.sql`

**Problem:** A projection consumer writes one event into two targets, the adjacency store and then the engine. An event that cannot be applied faithfully must not land in either. A stored row the engine never honored is silent divergence, and some malformed rows make reload preflight refuse the whole tree. Per-event validation cannot see races between events, and redelivering an already-stored event must stay distinguishable from corruption.

**Solution:** Three layers. A gate at the top of the handler rejects everything checkable from the payload alone (stream identity, known `tree_type`, per-type position rules) before either projection, so a rejected event leaves no trace outside the EventStore. A partial unique index (`idx_tree_nodes_tree_parent_position_active`, migration 000004) arbitrates what the gate cannot see: two events claiming one slot resolve at the insert, loudly, with the store still reloadable. Postgres index order then gives redelivery a discriminator for free: `tree_nodes_pkey` (the row id is the event ID) fires for an already-stored event, the user index for a conflicting one. The layers do not make the two projections atomic. The store insert lands before the engine call, so an engine failure after a successful insert leaves a stored row the engine never applied (HEU-576).

**Usage:**
```go
// Gate before either projection: nothing lands anywhere on rejection.
if want := TreeStreamName(payload.TreeID); event.Stream != want {
    return fmt.Errorf(...)
}
if !supportedTreeTypes[payload.TreeType] {
    return fmt.Errorf(...)
}
// per-type position rules (switch with a loud default), then the store
// insert, then the engine dispatch — in that order.
```

**Notes:** Distinct from UC-NET-012, which preflights an irreversible bulk replay. Here each event is individually recoverable, so the gate stays thin (payload-checkable rules only) and the database owns cross-event races. The gate cannot check the matrix width bound because nothing persists width (HEU-554). The u8 ceiling is gated; the width..255 band is the documented residual. Redelivery is not yet idempotent (HEU-576). `MemoryTreeStore` mirrors all three constraints in the same check order, so the discriminator is unit-testable against the double.

---

### UC-NET-015: Immutable run registry with a visibility-flip results store

**Added:** v0.0.1 (HEU-555)
**Files:** `internal/networkengine/commission_run_store.go`,
`internal/networkengine/commission_run_store_postgres.go`,
`internal/networkengine/commission_run_store_memory.go`,
`migrations/000005_create_commission_tables.up.sql`

**Problem:** A batch job produces up to a million rows per period. The rows must be an audit record retained for years, a re-run must not destroy the prior one, and readers must never see a half-written batch. Holding a million-row write inside one transaction readers can see is not viable.

**Solution:** Split the run's *status* from its *rows*. A `commission_runs` row carries mutable status (`running` → `complete` → `voided`) and `superseded_by`; `commission_results` rows carry `run_id`. Completion is the visibility flip: `GetLiveResults` returns rows only for a completed, non-voided run, so the bulk write happens against a `running` run outside any replacement transaction and becomes visible atomically via one status update. Re-running calls `ReplaceRun`, which voids the old run and opens its replacement in one transaction — lock, void, insert, link, in that order, because the partial unique index forbids inserting before the void and the foreign key forbids linking before the insert. Writes replace per `(run_id, structure)`, so a retry after an uncertain commit leaves one copy.

**Usage:**
```go
store := networkengine.NewPostgresCommissionRunStore(pool)
runID, _ := store.CreateRun(ctx, "2026-01", planHash)   // planHash from networkengine.PlanHash
_ = store.SaveResults(ctx, runID, "unilevel", rows)     // invisible so far
_ = store.CompleteRun(ctx, runID, carryForward)         // now live
live, _ := store.GetLiveResults(ctx, "2026-01")

// Re-run the period. The old run is voided, not deleted, and keeps its results.
newID, _ := store.ReplaceRun(ctx, runID, newPlanHash)
```

**When to use this pattern:**
- A batch write too large to hold inside a transaction readers can see.
- Results that must be superseded without being destroyed.
- A re-run whose partial output must never be visible.

**Notes:** One active run per period comes from a partial unique index on `(period_id) WHERE status <> 'voided'`. The same-period rule for `superseded_by` is a composite foreign key against `UNIQUE (id, period_id)` — it does not need a trigger. Voiding preserves `completed_at` and `carry_forward`, which is why the completion CHECK is one-directional: a biconditional would make complete → voided impossible without erasing the audit fact the row exists to hold. Audit timestamps use `clock_timestamp()`, not `now()`, which is stamped at BEGIN and can predate a lock wait. Both implementations run one shared behavioral suite; see `docs/development/postgres-stores.md` for the Go/Postgres seams that suite exists to catch. The store has no production caller until HEU-592. HEU-595 adds `ListRuns` for walking the supersede chain, and HEU-596 owns the read-path paging contract.

---

### UC-NET-016: Removing a wire field without a red interval

**Added:** v0.0.2 (HEU-583), applied to board plan in HEU-603
**Files:** `engine/network-engine-worker/tests/worker_integration.rs` (`calculate_streamline_ignores_request_scoped_config`, `board_calculate_ignores_request_scoped_config`, `board_calculate_ignores_malformed_request_config`, `board_calculate_rejects_legacy_shape_without_structure`), `internal/networkengine/engine_client_test.go` (`TestEngineClient_CalculateStreamline_MockParams`, `TestEngineClient_CalculateBoardCommissions_MockParams`)

**Problem:** Deleting a field from an NDJSON request is a breaking change across two languages. Doing it in one commit leaves every intermediate commit red for one side or the other, and a caller still sending the field silently gets a different answer with nothing to catch it.

**Solution:** Three ordered steps, exploiting the fact that neither Rust crate sets `#[serde(deny_unknown_fields)]` — extra params are ignored, not rejected.

1. Migrate every caller to the new source **first**, leaving the old field in place. Both suites stay green against the unchanged worker.
2. Flip the handler to read from the new source. Callers still sending the old field are silently ignored, and unchanged expected results are what prove the migration was behavior-preserving.
3. Delete the field and its now-orphaned types.

This inverts the obvious order. HEU-583's plan deliberately deviated from its own design's delivery phases to get it, on two verified facts: no `deny_unknown_fields` anywhere, and `setup`/`setup_raw` mutual exclusivity in the contract harness.

**Guarding it:** one test sends the *legacy* wire shape with a hostile value and asserts the payout still comes from the new source. Make the two halves of the payload differ deliberately:

- a **valid** old-shaped field catches "re-added and honored" — the calculation pays the hostile value.
- a **deliberately malformed** one catches "re-added at all" — it fails deserialization before anything reads it, which is the leading indicator, since a field usually reappears unused before something wires it up.

Without that test, reintroducing the field leaves the entire suite green. Verified by mutation: re-adding the field and preferring it failed exactly one test out of seventy.

**When to use this pattern:** any handler still taking request-scoped config. Both handlers that were on the wrong side of it have moved — `calculate_streamline` in HEU-583, `board_calculate_commissions` in HEU-603. The other five commission handlers never carried request config. The remaining case is the create door, `handle_create_board_plan` (HEU-607): not a commission handler, but the same shape, config off the request and never validated.

One asymmetry showed up on the board application. Streamline carried two legacy fields, so a single request could hold a valid-hostile value in one and a malformed value in the other. Board carried only `config`, and one field cannot be both at once, so the two halves of the guard became two tests instead of one request. Count the legacy fields before assuming one test covers both halves.

**Notes:** The silence that makes the migration safe is also unobservable — nothing reports that a caller sent an ignored field. HEU-613 covers that. Do not add `deny_unknown_fields` until callers have migrated, or the red interval this pattern avoids comes straight back.

---

### UC-NET-017: Reading a nil caller collection as empty

**Added:** Unreleased (HEU-626)
**Files:** `engine/network-engine/src/serde_helpers.rs` (`null_as_empty`), `engine/network-engine-worker/src/handlers/commission.rs`, `engine/network-engine-worker/src/handlers/streamline.rs`, `engine/network-engine-worker/src/handlers/board_plan.rs`, `engine/network-engine/src/rank/types.rs`, `internal/networkengine/engine_client_test.go` (the `_NilCollections` tests)

**Problem:** A nil Go map or slice marshals to JSON `null`, not `{}` or `[]`. On the Rust side `#[serde(default)]` covers an *absent* key; it does not cover a key present with a null value. So a caller that leaves a collection unset sends the one shape neither plain serde path accepts, and the whole request dies with `INVALID_PARAMS`.

The trap is that the failing call is usually the *natural* one — a first period with no prior counts, a period with no volume events, a plan with no history. It also only shows up from clients you do not control, which is why it sat unnoticed. The rank driver normalized nils away explicitly, so its caller could never send one; the commission methods have no non-test caller yet, so nothing exercised the shape at all.

**Solution:** One helper, applied by requiredness. `null_as_empty` deserializes through `Option<T>` and unwraps to `T::default()`, which widens null and nothing else.

| The field is | Attribute | Absent | Null |
|---|---|---|---|
| Required | `#[serde(deserialize_with = "null_as_empty")]` | error | empty |
| Optional | `#[serde(default, deserialize_with = "null_as_empty")]` | empty | empty |

The required row is the point. Widening null must not quietly widen absent, or a caller who forgets a field is paid zero instead of being told. Guard both halves, one absence test per required field — a single guard still passes if someone adds `default` to the other field.

**Usage:**

```rust
use network_engine::serde_helpers::null_as_empty;

// Required: absent stays a loud INVALID_PARAMS.
#[serde(deserialize_with = "null_as_empty")]
snapshots: HashMap<Uuid, DistributorSnapshot>,

// Optional: absent and null both mean empty.
#[serde(default, deserialize_with = "null_as_empty")]
carry_forward: HashMap<Uuid, LegVolumes>,
```

**Notes:** The reason this is catalogued rather than left in the dev guide: the repo grew **two** independently written copies of this helper before anyone noticed — `deserialize_null_default` in `config/`, then `null_as_default` added to `handlers/board_plan.rs` by HEU-603, which did the identical job. One helper, one name, one place.

The `T: Default` widening is the caveat. On a collection, `default` reads as "empty", which is what it is for. On a numeric field it would silently produce `0` — the name is chosen so that misuse reads wrong at the call site.

Do not reach for Go's `omitempty` on a required field to solve this. On its own it breaks the call: when the collection is empty the key vanishes, and a required field has no `default` to fall back on. Adding `default` to fix that is the actual hazard — a dropped field becomes indistinguishable from an empty one, which on a money path pays zero.

Widening `snapshots: null` to empty does not open a silent-zero path of its own. Every calculator that reaches its walk rejects volume naming a source with no snapshot — `walk::validate_source` for the level-based walks, and `binary.rs` for pairing, which resolves the owner first and reports that UUID instead. Streamline is the qualified case: volume for a source in no stream, or in a frozen one, is filtered out before the walk and returns ok with an empty result (HEU-611). That filter keys on stream membership, not snapshots, so the widening neither causes nor worsens it.

Still null-intolerant, tracked by HEU-632: `history`'s inner per-period map (a null there has no defined meaning — absent-key and `Some(None)` are the two documented states), `cycle_events[].new_boards`, and `board_compress_inactive`'s `member_ids`.

Cross-reference UC-NET-007, the other `deserialize_with` serde-edge entry, and `docs/development/network-engine.md` for the fuller treatment including the Go-side wire assertions.
