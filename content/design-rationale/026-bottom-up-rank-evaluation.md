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

## The Decision

`evaluate_ranks` walks every tree bottom-up:

- `evaluation_order_for_users` returns the distributors deepest-first. For each
  user it takes the maximum depth across every tree the user appears in, then
  sorts by depth descending. The tiebreak is `user_id` ascending.
- The evaluation loop carries an accumulating `already: HashMap<Uuid,
  EvaluatedRank>`. Each `evaluate_distributor` call reads descendants' computed
  ranks from `already`, then writes its own result back into it.
- Within one tree, deepest-first ordering evaluates a distributor's descendants
  before the distributor itself. They are strictly deeper in that tree, so
  their ranks are in `already` when the distributor is evaluated. The
  multi-tree case needs a caveat. See the known limitation below.
- The result is moved into a `BTreeMap<Uuid, EvaluatedRank>` so serialization
  emits user-id keys in a fixed order.

Within one distributor, `evaluate_distributor` ascends the rank ladder. It
iterates ranks lowest ordinal first and keeps the highest one that passes. A
failed rank does not short-circuit, so a distributor who satisfies rank N+1 but
not rank N still reaches N+1.

## The Reasoning

**Why bottom-up.** A predicate that reads descendant ranks has a strict data
dependency. The descendant must be evaluated first. Within a tree,
deepest-first ordering is the one walk order that satisfies that dependency for
every ancestor at once. It also gives the recursive senior-title case a base
case for free. A leaf has no descendants, so it evaluates against an empty
descendant context. Each level above it then sees fully resolved ranks below.
No predicate has to recurse.

**Why an accumulating map.** Each distributor's rank is computed exactly once.
A predicate looking down reads `already` rather than re-running rank evaluation
for the subtree. A descendant's rank, including its own leg-quality result, is
resolved once and reused by every ancestor above it.

**Why determinism is enforced.** The engine must produce byte-identical output
for identical input. Two places could leak nondeterminism. Iteration order over
the user set is pinned by the depth-descending, `user_id`-ascending sort.
Serialization order is pinned by the `BTreeMap`. A plain `HashMap` would surface
its arbitrary iteration order in the output.

**Why predicates see descendants, not ancestors.** When a distributor is
evaluated, `already` holds every node processed before it. Within one tree that
set contains all of the distributor's descendants and never an ancestor. An
ancestor is shallower, sorts later, and is absent. This is a deliberate limit.
The design supports "qualify on what is below me" and not "qualify on what is
above me."

**Known limitation: ordering across multiple trees.**
`evaluation_order_for_users` gives each distributor a single position from its
maximum depth across all registered trees. That is exact for a plan with one
structure tree. With several structure trees it is only a heuristic. A
distributor that is shallow in one tree but deep in another is ordered by its
deepest appearance, which can place it before a same-tree descendant that is
shallower everywhere. A predicate walking the shallower tree then reads that
descendant as not-yet-evaluated and undercounts. Tracked in HEU-460.

## Revisit Trigger

Revisit this if a rank predicate ever needs an *ancestor's* evaluated rank. The
bottom-up walk cannot supply it. That would need a second, top-down pass with a
separate context map, run after the bottom-up pass completes. No current
predicate needs it, and adding one should be a conscious design change rather
than a quiet extension of `already`.
