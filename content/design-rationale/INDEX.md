# Design Rationale

These documents explain how MLMForge is designed and why. Each one covers a focused topic with the problem, the decision, and the reasoning.

Written for developers, architects, and evaluators who want to understand the system without reading the full development history.

MLMForge's architecture is informed by years of building and operating a production MLM platform. These documents reference that predecessor as "the legacy system." The decisions here address problems we encountered firsthand.

## Numbering

This folder uses its own numbering sequence. DEVELOPMENT.md has a separate ADR sequence. The two are independent. Some numbers appear in both and refer to different decisions.

The two sequences cover different scopes. DEVELOPMENT.md ADRs document high-level architectural decisions like language choice, persistence strategy, and modularity. The `content/design-rationale/` folder documents detailed per-domain design decisions like tree implementation, compensation config, and interface contracts.

Code comments that reference "ADR-NNN" use the DEVELOPMENT.md numbering.

## Documents

| # | Topic | Summary |
|---|-------|---------|
| [001](001-bounded-contexts.md) | **Bounded Contexts** | Why 8 contexts, what each owns, how they depend on each other |
| [002](002-context-boundaries.md) | **Context Boundaries** | Why contexts can't reach into each other and how mutations flow |
| [003](003-network-engine-design.md) | **Network Engine Design** | Why Rust, currency-free volume, generalized trees, rank groups, holding tanks |
| [004](004-interface-contracts.md) | **Interface Contracts** | How contexts communicate through interfaces, events, and the extraction path |
| [005](005-multi-currency.md) | **Multi-Currency** | Regional product catalogs, CV points, and the three-context currency chain |
| [006](006-enrollment-orchestration.md) | **Enrollment Orchestration** | The saga pattern, configurable payment failure, and structure placement |
| [007](007-unilevel-tree-implementation.md) | **Unilevel Tree Implementation** | Arena storage, UUID user IDs, iterative BFS, position-indexed model |
| [008](008-common-compensation-config.md) | **Common Compensation Config** | Periods, volume, ranks, eligibility, bonuses, payout, caps, placement, audit |
| [009](009-unilevel-compensation-config.md) | **Unilevel Compensation Config** | Rate table, compression, pass-up variant, donated placement |
| [010](010-binary-compensation-config.md) | **Binary Compensation Config** | Pairing bonus, volume-after-payout modes, carry-forward, cycle/step, spillover |
| [011](011-matrix-compensation-config.md) | **Matrix Compensation Config** | Width/height, forced placement, completion bonus, position bonus, board plan |
| [012](012-stairstep-compensation-config.md) | **Stairstep Compensation Config** | Breakaway threshold, differential overrides, generation counting |
| [013](013-generation-compensation-config.md) | **Generation Compensation Config** | Boundary modes, generation rates, empty generations, combined level+generation |
| [014](014-streamline-compensation-config.md) | **Streamline Compensation Config** | Dynamic compression, streams, rank expansion, freeze on demotion, monoline |
| [015](015-compensation-plan-schema-and-wire-format.md) | **Schema and Wire Format** | One wire format, JSON Schema validation, serde renames, structural translations |
| [016](016-eventstore-design.md) | **EventStore Design** | Unified store, JSON envelope, category-ID streams, optimistic concurrency, pgx v5 |
| [017](017-commission-calculation-architecture.md) | **Commission Calculation Architecture** | Snapshot vs rules separation, flat earnings output, prep+walk phases, no premature abstraction |
| [018](018-config-pipeline.md) | **Config Pipeline** | Five-stage validation, two-pass commission parsing, Commission marker interface, severity model |
| [019](019-ndjson-protocol.md) | **NDJSON Protocol** | Request-response envelope, RawValue params, error code taxonomy, panic recovery, context cancellation |
| [020](020-tree-topology-separation.md) | **Tree Topology Separation** | Trees enforce shape, callers decide placement, position validation vs placement logic |
| [021](021-sponsor-vs-placement-in-commission.md) | **Sponsor vs. Placement in Commission** | Placement edges determine commission flow, sponsor edges determine personal qualification |
| [022](022-shared-commission-walk.md) | **Shared Commission Walk** | Generic functions over TreeNavigator, no CommissionCalculator trait, callback injection for plan-specific behavior |
| [023](023-snapshot-persistence.md) | **Snapshot Persistence** | Serde-based serialization for all tree types, JSON format, Go-managed storage |
| [024](024-generation-calculator-reuse.md) | **Generation Calculator Reuse** | Why the standalone generation calculator reuses `count_generations_upward()` with a semantic mismatch instead of extracting a shared interface |
| [025](025-public-test-support-module.md) | **Public Test Support Module** | Why `network_engine::test_support` is public for integration tests, and why it is still treated as internal-only support code |
| [026](026-bottom-up-rank-evaluation.md) | **Bottom-Up Rank Evaluation** | Why rank evaluation iterates to a fixpoint over an accumulating descendant-rank map, so predicates read downline ranks even with multiple structure trees |

## Reading Order

Start with [001 Bounded Contexts](001-bounded-contexts.md). It establishes the vocabulary for everything else. Then read [002 Context Boundaries](002-context-boundaries.md) and [004 Interface Contracts](004-interface-contracts.md) to understand how the contexts communicate. The rest can be read in any order.
