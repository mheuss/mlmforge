# 029: Commission Provenance on the Wire

> **"ADR-NNN" in this document refers to the `DEVELOPMENT.md` sequence, not to
> this folder's numbering.** The two are independent. See
> [Numbering](INDEX.md#numbering).
>
> **Partial status.** The result shape, the walk index as correlation key, plan
> identity on the response, and the rank rule are decided. The outcome taxonomy
> was provisional and HEU-556 has now settled it: neither always-on
> classifications nor a bare upline sequence fits, and the record emits interned
> paths instead. See "The volume problem" below. **The step-level outcome names
> are still unimplemented, so they remain free to change until phase C ships.**

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
enrollment order are not recoverable from an ordered list of ancestor IDs, and
the reason is sharper than "not in the snapshot": a walk records ancestors by
parent edge, and both of these read a node's children by sponsor edge.
`get_upline` follows parent, `get_sponsored` reads the node's own sponsored
list, and `add_node` takes the two separately. No amount of recorded path
closes it.

HEU-556 settled this. The engine states both outcomes, and the run records the
tree facts behind them once per run and per node: each visited node's sponsored
child ids in enrollment order, plus their enrollment timestamps where pass-up is
configured. Both are then recomputable by joining against the run's snapshot
set. This is the same assembled-not-duplicated move this document already makes
for snapshot facts, applied to a second kind of run-invariant fact, and it costs
200 entries against 1,820,000 step visits on the worst case measured.

**That makes both decisions reproducible, not verifiable.** The stored lists are
what the engine says it read. Nothing proves it read the tree as it stood at
calculation time, because the tree carries no snapshot identity. So a
`depth_cap` or `pass_up` record supports the claim "the engine decided this from
these inputs" and not the claim "these inputs were correct." Independent
verification waits on tree identity, which is separate work.

## The volume problem

This section assumed the engine states its classifications and that the result
fits one response. The second assumption did not hold.

A note on the first. This document names five outcome values in passing,
`compressed_ineligible`, `depth_cap`, `pass_up`, `boundary_reached` and
`compressed_inactive` as a rejected name, and never sets out a taxonomy. HEU-556
reconstructed twelve from the traversal code to have something to measure. Every
size figure below is therefore a floor: a fuller taxonomy is only larger, and
both floors already exceed the ceiling.

Generation SameRank runs one traversal per distinct rank per volume source, and
both mechanics walk to root regardless of `max_depth` because non-consuming
steps do not advance the counter. Ten ranks, a thousand sources, and a
two-hundred-deep chain is on the order of two million step objects, which is
well past any reasonable single-response budget before earnings are counted.
The worker also materializes a `serde_json::Value` and then a `String`, so peak
memory is worse than the wire size.

The arithmetic held. HEU-556 measured it on 2026-09-12 against the deep-sparse
case, 100k nodes at depth 200 with 1,000 sources and 10 distinct ranks:
1,820,000 step objects, 170.2 MiB on the wire, 2.66x the hard ceiling.

### The upline option was measured, and it fails too

An earlier version of this section said that if the traversed upline is
sufficient the volume problem largely dissolves, and that the reasoning above
suggested it nearly was.

**That is false, and it was measured rather than reasoned about.** The same run
reduced to a bare ordered node sequence is 73.9 MiB. Still 1.15x the 64 MiB hard
ceiling and 4.62x the 16 MiB warn ceiling. Dropping every classification buys
2.3x and does not clear the bar.

Recorded as a falsified claim rather than a superseded one, because the
reasoning that produced it is still in this document and still reads as
persuasive. It was wrong about the size, not about the semantics.

### Why both options failed, and what replaced them

Both are per-step shapes, so both scale with visited nodes and differ only in
the constant. The measured run traversed 1,820,000 nodes across **2 distinct
paths**, because every source hung off one spine and each walk recorded the same
sequence again.

That is the duplication this document already rejected one level down. Per-earning
paths were rejected here because the shared prefix repeats on every earning
above; the fix was to go per walk so a node appears once. The same duplication
reappeared across walks.

So the record emits **interned paths**. A response carries a `paths` array, each
walk names a `(path, offset, length)` slice rather than repeating nodes, stated
decisions are kept only for consuming steps, and non-consuming skips are derived
by the reader from the path plus the snapshot set plus the plan. Measured at
9.4 MiB, 0.15x the hard ceiling.

This was not on either option list. The Revisit Trigger below offered a
truncation cap or moving off the single-response path; HEU-556 offered those two
plus the upline sequence. Interning is a fourth answer the spike produced rather
than one it selected.

The counter-argument against deriving still stands and is now narrower. A reader
recomputing compression is reimplementing `walk.rs`, and a reimplementation that
disagrees is worse than no record. Under this shape the reader derives only
non-consuming skips, so a disagreement can be wrong about why a node was passed
over and never about who was paid what. Every decision that moved money is
stated by the engine.

### The memory half is a separate defect

Peak RSS on the worker's own path was 1,832.7 MiB, 28.6x the ceiling, against
151.2 MiB for the typed result before serialization. `Response.result` is a
`serde_json::Value` and `main.rs` calls `to_string` on it, so a `BTreeMap`-backed
tree and its output `String` are both live. Serializing the typed result directly
costs 319.9 MiB instead.

That is HEU-743 and it is independent of provenance. Every large response pays
it. Removing it does not resolve this section's problem on its own: the interned
shape still needs it to bring peak memory under the hard ceiling.

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
test suite before. `ping` used to return a bare `"pong"` with no version.
Phase A changed it to report a protocol version that moves on every
change to wire semantics, not only on shape changes. The Go client reads that
version at startup and refuses to run against a worker whose version it does
not match, naming both in the error. Rejection is exact, not a range: a scalar
version cannot express which feature combinations a worker actually has. A worker that emits partial provenance and a client that
expects complete provenance are incompatible even though both speak the same
schema, and a version tracking only shape would let that pair through.

## Naming The `should_stop` Exit

The shared level walk takes a caller-supplied `should_stop` predicate. When it
fires, the walk records `boundary_reached`.

Two alternatives were rejected, and the reasoning matters because HEU-46
persists these strings. A later rename orphans every row carrying the old value.

**`breakaway_reached` was rejected.** Breakaway is stairstep's word. The
predicate is caller-supplied and stairstep is its only non-trivial user today, so
naming the value after that one caller bakes one plan type's semantics into a
shared walk. That is the `compressed_inactive` trap described above: a name
accurate only while there is one caller, and misleading the moment there are two.

**`stop_requested` was rejected.** It is the most literally correct description of
what happened in the code, and it tells an auditor nothing about what happened to
the money. A provenance value the audit trail's own reader cannot interpret is
not doing its job.

`boundary_reached` describes the tree rather than the caller or the mechanism.

**Revisit condition.** If a second calculator passes a `should_stop` that is not a
boundary in any meaningful sense, this name stops being honest and must change
before that caller ships. Recorded rather than left to be noticed, because after
HEU-46 the cost of changing it is a data migration.

## Revisit Trigger

Four things would reopen this.

**Provenance does not fit a single NDJSON response. This one fired.** Measured
2026-09-12 by HEU-556 and resolved by interned paths, which was not among the
answers this trigger anticipated. See "The volume problem" above. The trigger is
kept because its reasoning about why the worst case is a deep tree with sparse
boundaries held exactly, and because a future shape change has to clear the same
ceiling. What it got wrong was the answer, not the diagnosis.

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

**A second `should_stop` caller appears that is not a boundary.** See "Naming The
`should_stop` Exit" above. The value would need renaming before that caller ships,
and after HEU-46 that is a migration rather than a rename.

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
- **This document already breaks that rule once.** A generation breakaway that
  fails the boundary check with `empty_generation_consumes_number` unset neither
  records nor consumes. The behavior is described above as "may or may not
  consume a generation depending on config" and was never given an outcome.
  Phase C must give it one.
- Walks reference interned paths rather than repeating node sequences. A shape
  that inlines a per-walk node list reintroduces the duplication measured at
  170.2 MiB against a 64 MiB ceiling.
- Stated decisions cover consuming steps. Non-consuming skips are derived by the
  reader, except `pass_up`, which names the recruit that caused it.
- The storage half persists the run's per-node sponsored-child facts alongside
  the snapshot set, where the plan enables active leg tiers or pass-up.
- `depth_cap` and `pass_up` records are reproducible, not verifiable. Do not
  describe them as independently verifiable until the tree carries a snapshot
  identity.
- The storage half must persist the run's snapshot set. Steps name nodes, and
  verifying a decision needs the values it was made from.
- Binary and board earnings carry no rank, deliberately. Anyone adding one must
  establish that the calculation reads it.
- Walk indexes come from a defined total order, never from hash iteration.
- 027 still owns where provenance lives. This decision owns what crosses the
  seam and in what shape.
