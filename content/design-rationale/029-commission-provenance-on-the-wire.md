# 029: Commission Provenance on the Wire

> **"ADR-NNN" in this document refers to the `DEVELOPMENT.md` sequence, not to
> this folder's numbering.** The two are independent. See
> [Numbering](INDEX.md#numbering).
>
> **Partial status.** The result shape, the walk index as correlation key, plan
> identity on the response, and the rank rule are decided. **The outcome
> taxonomy is provisional.** Always-on, fully-stated provenance does not fit a
> single NDJSON response at scale, and a spike is settling whether the engine
> should emit classifications or just the traversed upline. See "The volume
> problem" below.

## The Problem

[027](027-provenance-as-primary-data.md) settled where commission provenance
lives. It is primary data in a dedicated table, not a rebuildable projection,
because retention would otherwise delete it.

It did not settle what the engine emits, or in what shape. That has to be
answered first. No storage decision can record a field the engine never sends.

The gap is concrete. `CommissionEarning` carries six fields. Design 003 commits
to commissions being auditable to the penny, and audit needs five inputs per
earning. Two are present. Rank at calculation time is read from the snapshot
and thrown away. The walked path is collapsed into `level`, which cannot be
reversed. Compression decisions are not emitted at all.

There is a second gap. The worker never says which plan it has loaded. An
auditor cannot check a decision without knowing the config that produced it.

## The Decision

The commission calculators return an object, not an array.

This supersedes [017](017-commission-calculation-architecture.md)'s
`Vec<CommissionEarning>` return contract. Only the envelope changes. The
earnings list inside the object keeps 017's shape exactly: flat,
self-contained, one entry per earner-per-source, no grouping and no nesting.

```text
{ "earnings": [ … ], "walks": [ … ], "plan": { … } }
```

A **walk** is one traversal. It carries an index, the source, a kind, optional
context, an ordered list of steps, and how it ended. Each step names a node, an
outcome, and whether it advanced the walk's counter.

Each earning gains a `walk` field holding the index of the walk that produced
it. The field is nullable, and the null case is real: stairstep Walk 2 earnings
carry `null` because that traversal is not instrumented. Nullable means "no
walk recorded," never "no traversal happened."

The reference is many-to-one. A walk produces zero or more earnings, and an
earning belongs to at most one walk. `(earner_id, source_id)` does not identify
an earning and cannot be used to join the two, which is the whole reason the
index exists.

Provenance is emitted **per walk, not per earning**. Two of the three traversal
mechanics are instrumented. The response states which plan the engine used.
Verification data is assembled from what already exists rather than duplicated
onto every step. Rank is emitted only where the calculator reads it.

## The Reasoning

### Per walk, because a path is a property of the walk

One volume source produces one traversal that pays several ancestors. The
walked path and the decisions along it belong to that traversal, not to any
single earning.

Attaching a path to each earning repeats the shared prefix on every earning
above it. In a walk paying ten ancestors, the tenth carries the first nine
again. The duplication grows with depth, and depth is not bounded by
`max_depth`. Skips do not consume a level, and `get_upline` walks to root, so
the number of nodes visited is bounded by the tree, not the plan.

Emitting per walk means a node appears once. That is the difference between an
output that grows with visited nodes and one that grows with earnings times
depth.

### The obvious correlation key does not work

The natural way to attach a parallel record to an earning is
`(earner_id, source_id)`. That pair is not unique.

Stairstep runs two walks. Walk 1 pays group commissions and Walk 2 pays
overrides on breakaway volume, and one ancestor can earn from both for the same
source. Multi-tier overrides can also select the same ancestor for several
tiers. `sort_earnings` says so in its own doc comment, which is why it carries
a level tiebreaker.

A walk index is unique **within one calculator response**. Walks are collected,
sorted into a total order, and then numbered, so the index is stable across
runs over identical input but means nothing outside the response that produced
it. Storage has to qualify it with the run and structure that produced it, which
is what HEU-46's per-walk key does.

This is worth stating because the wrong key looks correct until stairstep runs.

### There are three traversals, and only two are instrumented

`walk_level_commissions` is the shared level walk described by
[022](022-shared-commission-walk.md). It is not the only traversal that
produces earnings.

`count_generations_upward` walks generation boundaries. It serves the
generation calculator in both boundary modes. It counts differently: it skips
non-breakaways entirely without touching the counter, and a breakaway that
fails the boundary check may or may not consume a generation depending on
config.

`walk_single_overrides` has a third mechanic hiding in it. When generation
overrides are configured it delegates to `count_generations_upward`. When they
are not, it runs its own raw upline scan where the first qualifying ancestor
earns.

Both instrumented mechanics cover unilevel, matrix, streamline, stairstep
Walk 1, and generation. Stairstep Walk 2 is excluded, and its earnings carry a
null walk reference.

### Why Walk 2 is excluded rather than described

Two consecutive design revisions described stairstep Walk 2 incorrectly. The
first said multi-tier emits one walk per tier. The second said
`count_generations_upward` serves both override strategies. Neither is true.

An earlier draft blamed this on missing fixtures. That was wrong.
`stairstep.rs` carries 36 unit tests covering all three paths in detail. The
behavior was pinned and available both times it was described incorrectly. The
honest reason is that reading three interleaved dispatch paths and getting them
right is harder than it looks, and confidence did not track accuracy.

A null walk reference with a recorded reason is honest. A confident description
that is wrong is worse than an admitted gap, and it is worse specifically
because the record is durable. This follows the precedent in
`commission_detail.go`, which states a known limitation on `binaryPairingDetail`
rather than implying coverage it does not have.

Walk 2 provenance is its own work, and it starts by writing fixtures.

### Outcomes are named after behavior, not after config

`CompressionMode::SkipInactive` is implemented as `!node_eligible`. It
compresses any ineligible distributor, including an active one who lacks
personal volume or a required order.

The outcome is therefore `compressed_ineligible`, not `compressed_inactive`.
Naming it after the config variant would put a claim in a durable audit record
that the code does not make. Steps also carry which eligibility condition
failed, because "ineligible" without a reason does not answer a dispute.

### Verification is assembled, not duplicated

Checking a skip decision independently needs three things: the snapshot facts
the decision was made from, the config that set the thresholds, and the
decision itself.

Only the third is new. The snapshot facts arrived in the request and are
invariant for a run, since one run is one period and `DistributorSnapshot` is
per distributor per period. Storing them once per run and joining by user ID
gives an auditor everything, and it makes persisting the run's snapshot set a
requirement on the storage half.

Echoing rank, personal volume, status, and order flag onto every step would be
the single largest thing that could be done to output size, in exchange for
data the caller already holds.

Two outcomes escape this. `depth_cap` derives from personally sponsored child
counts and `pass_up` from sponsor relationships and enrollment order. Neither
is in `DistributorSnapshot`. Both read the worker's tree, which is mutable and
carries no version a stored run could reference.

That exception is the interesting one. The tree at calculation time is the only
input that is genuinely unrecoverable, and a walk's ordered node list is that
tree along the path. Everything else the engine could say about a node is
recomputable from the upline, the snapshots, and the plan.

The node list is not enough for these two, though. Sponsored-child counts and
enrollment order are not recoverable from an ordered list of ancestor IDs. So
either the engine states these outcomes and the reader trusts them, or the
inputs behind them get recorded somewhere. HEU-556 settles which, and no phase
ships a `depth_cap` or `pass_up` outcome as independently verifiable until it
does.

## The volume problem

The taxonomy below assumes the engine states its classifications and that the
result fits one response. The second assumption does not hold.

Generation SameRank runs one traversal per distinct rank per volume source, and
both mechanics walk to root regardless of `max_depth` because non-consuming
steps do not advance the counter. Ten ranks, a thousand sources, and a
two-hundred-deep chain is on the order of two million step objects, which is
well past any reasonable single-response budget before earnings are counted.
The worker also materializes a `serde_json::Value` and then a `String`, so peak
memory is worse than the wire size.

This is arithmetic, not a measurement risk, and it is why the taxonomy is
provisional. The open question is whether the engine emits classifications at
all. If the traversed upline is sufficient, and the reasoning above suggests it
nearly is, the record collapses to an ordered node sequence and the classifier
moves to whoever reads it.

The counter-argument is that a derived classification can drift from the engine
that produced it. A reader recomputing compression from a plan hash and a
snapshot set is reimplementing `walk.rs`, and a reimplementation that disagrees
is worse than no record. That tension is what the spike resolves.

### The engine must say which plan it used

`CommissionRun.PlanHash` is computed by the Go caller. It records what the
caller believes was loaded.

Since [028](028-commission-config-from-validated-state.md) moved handlers onto
`WorkerState`, `require_plan` returns whatever plan was loaded last. A
`load_plan` between run creation and calculation changes the config without
changing the recorded hash. HEU-614 flags the same hazard for board.

So the response names the plan the engine actually had: name, version, and a
hash the worker computes over the raw `load_plan` bytes it received. The format
is `sha256:<64 lowercase hex>`, reusing `internal/networkengine/plan_hash.go`
rather than inventing a second representation. That function hashes stage-5
pipeline output, which is the same byte sequence `load_plan` receives, and the
`commission_runs` CHECK already enforces the prefix.

Reporting it is not enough on its own. The caller compares the returned
identity against the run's expected hash before persisting results, because a
mismatch means the payouts were computed under a plan the run does not record.
An identity nobody checks is decoration.

Without this, every decision in the walk is unverifiable no matter how
carefully it is recorded, because the rule it was checked against is unknown.

### Rank means "caused this payout"

Rank is emitted where the calculator reads it. Level and generation walks read
it, for rate table lookup, compression thresholds, and boundary detection.

Binary does not. `binary.rs` never touches `snapshot.rank`. Its eligibility is
personal volume, order presence, and lifecycle status, and its payout is
matched leg volume times a configured percent.

Board does not either. `board_calculate_commissions` receives no snapshots at
all.

Widening either request so a rank could be echoed would emit a value the
calculation never used. In an audit record a rank implies rank affected the
payout. Omitting it is honest. Including it is misleading.

The field is `earner_rank`, not `rank`, because Walk 2's differential resolves
its rate from both the ancestor's rank and the breakaway's rank. Those earnings
are out of scope, and the explicit name stops the field being widened by
assumption when they arrive.

This is an explicit exception to [027](027-provenance-as-primary-data.md),
which lists rank at calculation time among the facts every earning's provenance
carries. That requirement holds wherever a rank was read. For binary and board
there is no such rank, and 027's list should be read as scoped to the
calculators that have one.

Binary's real gap is different. `binaryPairingDetail` cannot say which mode
produced a row. That is closed by emitting mode discriminants, not a rank.

### Breaking the shape now rather than later

Changing the result from an array to an object breaks five ops.
[019](019-ndjson-protocol.md) is why that is the right time to do it. The wire
format is the expensive thing to change once callers exist.

Today there are none. Nothing is deployed, the Go and Rust sides ship from one
tree, and HEU-592's commission runner is not built.

The one real hazard is a stale worker binary, which has silently backed the Go
test suite before. `ping` returns a bare `"pong"` with no version
today. Phase A changes it to report a protocol version that moves on every
change to wire semantics, not only on shape changes. The Go client reads that
version at startup and refuses to run against a worker whose version it does
not match, naming both in the error. Rejection is exact, not a range: a scalar
version cannot express which feature combinations a worker actually has. A worker that emits partial provenance and a client that
expects complete provenance are incompatible even though both speak the same
schema, and a version tracking only shape would let that pair through.

## Revisit Trigger

Three things would reopen this.

**Provenance does not fit a single NDJSON response.** Both mechanics walk to
root and non-consuming steps do not advance the counter, so one walk can span
the full tree depth regardless of `max_depth`. A deep tree with sparse
qualifying boundaries is the worst case, and generation SameRank multiplies
walk count by the number of distinct ranks on top of it. Measurement gates the
phase that lands it. If the ceiling is exceeded, the answer is capping recorded
steps with an explicit truncation marker or moving provenance off the
single-response path. It is not going back to per-earning paths, which are
strictly larger.

**HEU-46 stores provenance per earning.** Normalizing the wire only pays off at
rest if storage is normalized too. Writing each earning's walk slice into its
`detail` JSONB reproduces the duplication this decision avoids, and the wire
shape would then be carrying complexity for a benefit that stops at the seam. A
per-walk table keyed by `(run, structure, source, walk)` preserves it. That is
HEU-46's call.

**Stairstep Walk 2 gets fixtures.** The exclusion here is a consequence of
having no pinned behavior, not a judgment that override provenance does not
matter. Once all three paths are pinned, the null walk reference should become
a real one.

## What This Means

- Commission provenance is emitted per walk, correlated by walk index. Do not
  key it by `(earner_id, source_id)`.
- `walk_level_commissions` and `count_generations_upward` emit walks. A new
  traversal that produces earnings must emit them too, or carry an explicit
  null and say why.
- Stairstep Walk 2 earnings carry a null walk. That is a recorded gap, not an
  oversight, and it is not evidence that no traversal occurred.
- The `outcome`, `stop`, and mode strings are persisted by HEU-46. Nothing
  persists them today, so the provisional names cost nothing to change while
  HEU-556 is open. Once HEU-46 lands, changing one orphans every row carrying
  the old value, the same way the `kind` strings in `commission_detail.go` do.
- Counter reconstruction is a count of consumed steps. Any new skip or forfeit
  path must record a step, or the count silently drifts.
- The storage half must persist the run's snapshot set. Steps name nodes, and
  verifying a decision needs the values it was made from.
- Binary and board earnings carry no rank, deliberately. Anyone adding one must
  establish that the calculation reads it.
- Walk indexes come from a defined total order, never from hash iteration.
- 027 still owns where provenance lives. This decision owns what crosses the
  seam and in what shape.
