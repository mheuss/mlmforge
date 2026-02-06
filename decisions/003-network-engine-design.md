# 003: Network Engine Design

## The Problem

The commission engine is the core of an MLM platform. It walks tree structures that can contain millions of nodes, applies qualification rules at every level, and calculates commissions that must be auditable to the penny. The legacy PHP system did this work in interpreted code with database queries at every tree level. It worked for thousands of distributors but could not scale beyond that.

The engine also needs to support multiple tree types (binary, unilevel, matrix, stairstep, streamline), multiple ranking systems, and compensation plans that vary wildly between companies.

## Decisions

### Rust for the Engine

The commission engine is written in Rust. The rest of the application is Go. The Rust engine is called from Go via FFI (in-process) or gRPC (distributed).

Commission calculation is the one place where performance is non-negotiable. Walking a binary tree of 500,000 nodes, checking rank qualifications at each level, and computing commissions with exact decimal arithmetic is compute-bound work. Rust's zero-cost abstractions and predictable performance matter here. Go's garbage collector would introduce latency spikes during the exact operations where consistent performance is most important.

Go is the right choice for 90% of the application. HTTP API, business logic, and database CRUD are Go's strengths. Two languages add complexity, so Rust is confined to where it earns its keep.

### Currency-Free Volume

The Network Engine works entirely in CV (Commissionable Volume) points. It never sees dollars, euros, or yen.

The pipeline:
1. **Commerce** owns regional product catalogs. Each product has a price in local currency and a pre-assigned CV value. A product might cost $49.99 in the US and ¥5,500 in Japan. Both carry 40 CV.
2. **Network Engine** receives CV points from Commerce and routes them to tree structures based on compensation plan rules. All calculations happen in CV.
3. Commission amounts are produced in the company's base currency.
4. **Financial** converts base-currency amounts to payout currency at disbursement time.

Making the engine currency-neutral keeps financial logic out of the performance-critical path. CV points are assigned at product configuration time. The engine does not need to know that a product costs different amounts in different markets. See [005 Multi-Currency](005-multi-currency.md) for the full picture.

### Generalized Tree Positions

All tree types use position-indexed operations instead of named legs. A binary tree has positions 0 (left) and 1 (right). A matrix tree has positions 0 through width-1. A unilevel tree has unbounded positions indexed by child order.

```go
BranchCounts  map[int]int     // Binary: {0: 150, 1: 143}
BranchVolumes map[int]float64 // CV volume per position
OpenPositions []int           // Unfilled child slots
```

The operations are the same across tree types. "Get the subtree under position N" works for binary, matrix, and unilevel. "Count descendants in branch N" is the same query regardless of tree type. A single `TreeNavigator` interface handles all of them. Consumers do not need to know which tree type they are querying.

We considered separate types per tree (BinaryNode, MatrixNode, UnilevelNode) with a common interface. The position-indexed approach is simpler and avoids type assertion overhead.

### Rank Groups

Organizations can have multiple ranking systems running at the same time. Some have a single global rank. Others have different ranks per structure. Some evaluate rank on current-period volume only. Others use cumulative all-time volume or highest-ever achieved rank.

```go
type RankGroup struct {
    ID             string
    Scope          string // "global" or "structure_specific"
    StructureID    string // If structure-specific
    EvaluationMode string // "current_period_only", "cumulative", "highest_ever"
}
```

The `RankGroup` concept allows all of these to coexist in a single compensation plan. All `RankProvider` methods are scoped by rank group.

### Holding Tanks

When a user is enrolled, they may qualify for placement on multiple tree structures. Some structures (binary, matrix) require a specific position choice. Rather than forcing this decision at enrollment time, qualified users who need a position choice are placed in a per-structure holding tank.

The sponsor or an admin later places the user from their back office dashboard.

Holding tanks are per-structure. A user can be placed on the unilevel immediately (auto-placement) while sitting in the binary holding tank waiting for their sponsor to choose left or right. The legacy system had a single holding tank, which meant a user stuck in the binary tank also could not be placed on the matrix. Per-structure tanks allow independent placement timelines.

### Cross-Structure Queries

The `QueryTree` method supports filtered queries that span multiple structures. This handles questions like "find all users in my binary downline who are also in my unilevel downline and have achieved Gold rank."

These queries are common in compensation plans (bonuses paid on cross-structure relationships) and in reporting (downline performance across all structures).

The Rust engine can evaluate cross-structure conditions in a single tree walk. Doing this from Go would require fetching the full downline from each structure and intersecting the results in memory. Pushing the query into the engine keeps the heavy lifting where the data lives.

## What We Considered

**Go for everything.** Simpler toolchain but commission calculations would hit GC pause issues under load.

**Currency-aware engine.** Let the engine handle multi-currency directly. This mixes financial concerns with computational concerns. The engine should be a pure calculation machine.

**Single global ranking system.** Simpler but does not match real-world compensation plan diversity. Companies with multiple structures almost always have different ranking criteria per structure.

## What This Enables

- **Performance where it matters.** Rust handles the compute-bound work. Go handles everything else.
- **CV as abstraction layer.** Any change to how CV is assigned is a product configuration change, not an engine change.
- **Flexible tree model.** The generalized position model handles all known compensation plan types through a single interface.
- **Query power at the boundary.** Cross-structure queries run in the engine where the data lives, not in Go where it would need to be round-tripped.
