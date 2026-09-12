# 030: Sponsor Continuity on Removal

> **Status: describes the branch that introduces it.** Not `main`. The repair,
> the refusal and the two error codes are implemented. The `remove_node`
> response field, the Go projection change and the protocol move land on the
> same branch and are not on `main` at the time this was written. Read a
> present-tense claim here as a claim about that branch.

## The Problem

Trees store two edge types. Placement edges are `parent` and `children`. Sponsor edges are `sponsor` and `sponsored`. Decision 021 rules on which calculation reads which. Neither 020 nor 021 says what happens to a sponsor edge when the sponsor is removed.

Every removal path dropped the dying node from its own sponsor's `sponsored` list. None walked the dying node's `sponsored` list, so each person it recruited kept a `sponsor` index pointing at a slot about to be cleared.

That surfaced two ways.

- `Arena::check_edge` rejects an edge whose target holds a nil `user_id`, so the snapshot would not restore. A tree that had lost a recruiter could not be loaded from its own snapshot.
- `Arena::alloc_slot` pops the free list before extending it. Add one more node after the removal and the freed slot goes to the new arrival, while the survivor still names it. The survivor is then sponsored by a live unrelated person. That state restores without error, because the slot is no longer a tombstone.

The second is the worse one. The restore failure is the defect announcing itself. One more `add_node` and it stops announcing.

## The Decision

A removal repairs the sponsor edges pointing at it, at removal time.

| Case | Outcome |
|---|---|
| The dying node has recruits who outlive the removal, and a sponsor who outlives it | Recruits are promoted to the nearest surviving sponsor |
| The dying node has recruits who outlive it, and no sponsor does | The removal is refused |
| The dying node has no recruits who outlive it | The removal proceeds, with or without a sponsor |

The third row is the one that is easy to get wrong. A sponsorless node with nothing pointing at it dangles nothing, so refusing it would break removals that are correct today.

"Nearest surviving sponsor" is one step in four of the five repair sites, which is the grandsponsor. Only the matrix holding tank removes a whole subtree at once, and there a node's sponsor can be dying in the same call, so the walk continues upward until it reaches someone who is not.

Five repair sites cover six tombstone sites. The holding tank tombstones in two places and takes one repair over its whole removed set.

### The repair runs at removal, not at restore

A validator cannot be the fix. Once a freed slot is reused the stale index names a live node, and nothing distinguishes it from a real sponsor edge. There is no later point at which the information still exists.

### The engine is not the only holder of a sponsor edge

`tree_nodes.sponsor_id` holds one too. The Go store soft-deletes the removed row and touches no other row, and the startup bulk load selects only rows where `removed_at` is null. So a repair that lives only in the engine leaves the store naming a user the load will not return, and the tree stops rebuilding after a restart.

Repairing one holder and not the other is worse than repairing neither, because the two then disagree and nothing reconciles them.

**The engine must therefore report which recruits it moved, and the projection must write them.** That is what the `remove_node` response field and the accompanying protocol move are for. Both land with this decision on the same branch.

The ordering that requires is not the one ADR-021 states. ADR-021 puts store projection before engine projection so a failed engine call leaves the table consistent. The moved-recruit list only exists after the engine has run, so both store writes move after the engine call and commit together. The stated ordering inverts and the property it protects improves: a failed engine call now leaves the table untouched rather than half-updated.

That inversion is deliberate and is not a concession. A reader meeting it will assume something was given up, and nothing was.

`ADR-NNN` here means a section inside `DEVELOPMENT.md`, not a file in this directory. Both series start at 000 and overlap, so ADR-021 is Tree Persistence as Event Projection while decision 021 is Sponsor vs Placement in Commission Calculations.

## What We Considered

Clear the edge to `None`. Smaller in the arena and larger everywhere else. It makes an unreachable error path in `StreamlineEngine::remove_member` reachable, and it widens what a null sponsor means on the wire: today it means the node is the root, and it would also mean the sponsor left. A caller reading null as "this is the root" would be silently wrong on any tree that has had a removal. That costs a protocol move.

Keep the edge and mark it. The only option that loses nothing: the sponsor tree would record that the recruiter left without crediting anyone else. It is a schema change on `Node`, which is why it was not taken.

Promote to the nearest surviving sponsor. Chosen. It costs nothing on the wire and leaves the streamline path and the property tests untouched. It moves the survivor up, in the same direction `PruningMode::PromoteEarliest` moves a placement child, by a different mechanism.

### This diverges from 021, deliberately

Decision 021 says sponsor edges determine personal qualification, and that sponsoring is a recruiting action while placement is a structural outcome. `count_active_legs` reads `get_sponsored`.

Promotion credits a grandsponsor with a leg they did not personally recruit, on the input to a payout gate. That is in tension with 021's rule and it was named before the choice was made, not discovered afterward. The mechanical cost of the alternatives decided it.

A reader who finds the contradiction should find it here rather than concluding it was missed.

## Three Components, Three Answers

This decision governs the three tree types directly. Two other components decide the same question differently, and the populations do not overlap, so one removal gets one answer.

| Component | On removal, a recruit whose recruiter left |
|---|---|
| Unilevel, binary, matrix | Promoted to the nearest surviving sponsor |
| `StreamlineEngine::remove_member` | Re-added under whichever node the compaction has reached, which becomes its sponsor |
| `BoardPlanEngine` | Keeps pointing at the departed recruiter, permanently and on purpose |

Streamline's compaction removes and re-adds every descendant, so it overwrites anything this decision wrote for that population. This decision governs only recruits placed outside the removed subtree, which streamline never touches.

Board plan keeps its `sponsor_map` entries after removal for re-entry routing, and `validate_restored` skips that map by design. That is a requirement the tree types do not have.

Whether these should converge is HEU-772. Nothing here preempts its answer.

## What This Enables

- A tree that has lost a recruiter restores from its own snapshot.
- A survivor is never silently re-pointed at whoever reuses a freed slot.
- A removal that cannot be repaired is refused with a named error rather than producing a tree that fails later somewhere else.

## Known Limits

Re-sponsoring a live node is not possible. There is no operation for it, so a refused removal has no operator remedy. HEU-771 carries that.

A sponsorless node whose recruits sit outside its subtree is refused by both pruning modes and cannot be removed at all. HEU-775 carries that.

A holding-tank round trip moves a recruit placed outside the subtree onto a surviving sponsor and does not move it back. The tank entry holds the returning node's own sponsor and nothing about who that node recruited, so re-placing it cannot restore the relationship.

The `remove_node` response names the recruits a removal moved, so a caller can see the move happen. It has no way to learn that re-placing from the tank did not undo it. HEU-776 carries that.

The engine has to answer before the store can be written, so a removal whose reply is lost leaves the engine and the store disagreeing and the retry cannot recover it. That window is a consequence of the ordering this decision requires. HEU-777 carries it.

The soft delete inside that store write is the one statement with no row-count check, where every re-sponsor beside it has one. Left lenient deliberately rather than by oversight. HEU-778 carries it.
