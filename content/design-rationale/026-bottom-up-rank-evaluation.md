# 026: Bottom-Up Rank Evaluation

## The Problem

Rank qualification predicates need the *evaluated ranks* of a distributor's
descendants. `DistributorCountRequirement` counts downline distributors whose
evaluated rank meets a threshold. The `ContainsRank` form of `leg_quality`
checks whether a leg's subtree contains a node of a given rank or better.
Senior titles compound this. A President rank can require legs containing a
Director, and Director can itself require legs containing a Manager.

A top-down pass cannot answer any of this. Evaluating an ancestor before its
descendants leaves the descendants unranked, so every count comes back zero.
The recursive senior-title case is worse. It has no base case in a top-down
walk.

A single bottom-up pass does not solve it either. One pass needs an order that
places every descendant before its ancestor in every structure tree at once.
In a multi-structure plan no such order need exist. A distributor can be
shallow in one tree and deep in another. Two distributors can even have
inverted ancestry. A is above B in one tree, B is above A in another. That is a
circular dependency, and no pass order resolves it.

## The Decision

`evaluate_ranks` evaluates the rank ladder by iterating to a fixpoint.

- `iterate_to_fixpoint` runs evaluation passes over an accumulating
  `already: HashMap<Uuid, EvaluatedRank>`. Each `evaluate_distributor` call
  reads descendants' computed ranks from `already`, then writes its own result
  back. Passes repeat until a full pass changes no rank.
- Evaluation starts from an empty `already` map. A distributor not yet in the
  map is treated as `Unranked`.
- `evaluation_order_for_users` orders distributors deepest-first. It takes the
  maximum depth across every tree, tiebroken by `user_id` ascending. This is a
  performance heuristic, not a correctness guarantee. It lets the common
  single-tree case settle in one effective pass. The fixpoint is correct for
  any order.
- A pass-count guard bounds the loop at `users * (ranks + 1) + 1`. Exceeding it
  returns `EvaluationError::RankEvaluationDidNotConverge`.
- The result is moved into a `BTreeMap<Uuid, EvaluatedRank>` so serialization
  emits user-id keys in a fixed order.

Within one distributor, `evaluate_distributor` ascends the rank ladder. It
iterates ranks lowest ordinal first and keeps the highest one that passes. A
failed rank does not short-circuit, so a distributor who satisfies rank N+1 but
not rank N still reaches N+1.

## The Reasoning

**Why fixpoint iteration.** A predicate that reads descendant ranks has a data
dependency on those descendants. A single ordered pass satisfies that
dependency only when one order works for every tree at once. Multi-structure
plans break that assumption, and inverted cross-tree ancestry makes it
impossible in principle. Iteration sidesteps ordering. Each pass propagates
freshly computed ranks into `already`. Passes repeat until nothing changes.

**Why it converges.** Every predicate that reads `already` is monotone. It
tests "descendants with rank at least X." Raising a descendant's rank can only
make such a predicate pass, never fail. Evaluation as a whole is therefore a
monotone function over a finite lattice, with `Unranked` at the bottom.
Iterating a monotone function from the bottom reaches its least fixpoint in a
finite number of steps. The least fixpoint is unique, so the result does not
depend on the within-pass order. The order only affects how many passes it
takes.

**Why the least fixpoint is the right answer.** Starting from all-`Unranked`
and only ever raising ranks computes the least fixpoint. That is the
conservative reading. A distributor earns a rank only on non-circular grounds.
Circularly dependent distributors do not bootstrap each other into a rank that
neither has independently earned.

**Why an accumulating map.** Each distributor's rank is computed and then read
from `already` rather than recomputed by every ancestor. A descendant's rank,
including its own leg-quality result, is resolved once and reused.

**Why determinism is enforced.** The engine must produce byte-identical output
for identical input. The fixpoint result is order-independent, so determinism
does not rest on the heuristic order. Serialization order is pinned by the
`BTreeMap`. A plain `HashMap` would surface its arbitrary iteration order in
the output.

**Why predicates see descendants, not ancestors.** Every current rank predicate
walks downward. `distributor_count` and `leg_quality` call `get_downline`, so
they read the ranks of nodes below the subject and never above it. This is a
convention of the predicate code, not a limit the loop imposes. After the first
pass `already` holds every distributor's rank, ancestors included, so a
predicate that walked upward could read one. None does. The design supports
"qualify on what is below me." Adding "qualify on what is above me" would be a
deliberate change.

## Revisit Trigger

Revisit this if a new descendant-reading predicate is not monotone in
descendant ranks. Convergence depends on monotonicity. Every predicate that
reads `already` must be such that raising a descendant's rank can only make the
predicate easier to pass. A predicate like "no leg may contain a rank above X"
would break that. `iterate_to_fixpoint` would fail to converge and return
`RankEvaluationDidNotConverge`. Adding a non-monotone predicate is a conscious
design change, not a quiet extension.

Revisit this too if a rank predicate is added that reads an *ancestor's*
evaluated rank. The loop can already supply one. `already` holds every rank
after the first pass, so no separate top-down pass is needed. Such a predicate
must still meet the monotonicity requirement above. It also widens the design
from "qualify on what is below me" to "qualify on any relative," which is a
conscious model change. No current predicate reads upward.
