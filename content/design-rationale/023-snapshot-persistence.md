# 023: Snapshot Persistence

## The Problem

Tree types in the network engine reconstruct from domain event replay. For the board plan engine, which manages many small boards with cycling history, full replay becomes expensive over time. Server outages and hardware failures require fast recovery.

## Decision

All tree-layer data structures must remain serde-serializable. Snapshot persistence uses `serde_json`. Go handles storage and scheduling.

`take_snapshot` and `restore_snapshot` work on one named structure at a time, not on the whole worker. The handler reads a `structure` param and serializes that entry of `WorkerState.trees`. The loaded plan and every other structure are outside the snapshot, so recovering a worker means restoring each structure and calling `load_plan` again. Snapshot scheduling and a Go-side snapshot store are not built.

### Constraint

Any future field addition to Arena, Node, UnilevelTree, BinaryTree, MatrixTree, StreamlineEngine, Stream, BoardPlanEngine, or Board that introduces a non-serializable type (function pointers, file handles, runtime-only state) breaks snapshot persistence. This is a breaking change.

### Format

JSON via `serde_json`. Debuggable and adequate for the data sizes involved. Format can be swapped to bincode or MessagePack by changing one line — serde derives are format-agnostic.

### Recovery Flow

1. Go takes periodic snapshots (after commission runs or every N operations)
2. Go stores snapshot bytes alongside the event sequence number
3. On recovery: restore snapshot, replay events after that sequence number
4. Snapshot scheduling and storage strategy are Go/infrastructure decisions

## What This Enables

- Fast crash recovery without full event replay
- Point-in-time state inspection for debugging
- Board plan viability (many boards with cycling history would make replay impractical)
