# 023: Snapshot Persistence

## The Problem

Tree types in the network engine reconstruct from domain event replay. For the board plan engine, which manages many small boards with cycling history, full replay becomes expensive over time. Server outages and hardware failures require fast recovery.

## Decision

All tree-layer data structures must remain serde-serializable. Snapshot persistence uses `serde_json`. Go handles storage and scheduling.

`take_snapshot` and `restore_snapshot` work on one named structure at a time, not on the whole worker. The handler reads a `structure` param and serializes that entry of `WorkerState.trees`. The loaded plan and every other structure are outside the snapshot, so recovering a worker means restoring each structure and calling `load_plan` again. Go stores the latest snapshot of each tree in `tree_snapshots`, and a write takes a new one every 1000 events (HEU-297).

### Constraint

Any future field addition to Arena, Node, UnilevelTree, BinaryTree, MatrixTree, StreamlineEngine, Stream, BoardPlanEngine, or Board that introduces a non-serializable type (function pointers, file handles, runtime-only state) breaks snapshot persistence. This is a breaking change.

### Format

JSON via `serde_json`. Debuggable and adequate for the data sizes involved. Format can be swapped to bincode or MessagePack by changing one line — serde derives are format-agnostic.

### Recovery Flow

1. A write whose projection succeeded takes a snapshot once the stream is 1000 events past the stored one.
2. Go stores the snapshot bytes with the stream version they were taken at.
3. A load restores the snapshot and replays the events after it, through the tree's projected version.
4. A load that cannot use a snapshot deletes it and replays the whole stream.

## What This Enables

- Fast crash recovery without full event replay
- Point-in-time state inspection for debugging
- Board plan viability (many boards with cycling history would make replay impractical)
