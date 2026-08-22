# Design Rationale

These documents explain how MLMForge is designed and why. Each one covers a focused topic with the problem, the decision, and the reasoning.

Written for developers, architects, and evaluators who want to understand the system without reading the full development history.

MLMForge's architecture is informed by years of building and operating a production MLM platform. These documents reference that predecessor as "the legacy system." The decisions here address problems we encountered firsthand.

## Numbering

This folder uses its own numbering sequence. [DEVELOPMENT.md](../../DEVELOPMENT.md) has a separate ADR sequence. The two are independent. Some numbers appear in both and refer to different decisions.

The two sequences cover different scopes. DEVELOPMENT.md ADRs document high-level architectural decisions like language choice, persistence strategy, and modularity. The `content/design-rationale/` folder documents detailed per-domain design decisions like tree implementation, compensation config, and interface contracts.

Code comments that reference "ADR-NNN" use the DEVELOPMENT.md numbering. Open that file, not the same-numbered document here. Both files ship, so every citation resolves.

Watch the overlap. ADR-014 through ADR-020 happen to line up with the file of the same number in this folder. Every other number points at a different decision. ADR-020 is Tree Topology Separation in both. ADR-011 is Reporting Ownership in DEVELOPMENT.md and Matrix Compensation Config here.

## Status

Some of these documents describe code that ships. Others describe a target the
codebase has not reached. The **Status** column below says which, and the seven
that are not Current carry a status banner at the top explaining exactly what is
missing.

| Value | Means |
|-------|-------|
| Current | Checked against the code. What it describes is what is built. |
| Design intent | The decision stands, but named interfaces or workflows in it do not exist yet. Read the banner first. |
| Not built | None of it exists in the codebase. |
| Partial | Some of the decision has landed and the document says which parts. |

Last checked 2026-08-22. "Current" means the symbols, file paths, counts, and
behavior claims in the document resolve against the code as of that date. It is
not a promise that every configurable option described in 008 through 015 has a
working implementation behind it, only that the names and structures are real.


## Documents

| # | Topic | Status | Summary |
|---|-------|--------|---------|
| [000](000-architecture-overview.md) | **Architecture Overview** | Current | Four mermaid diagrams: context wiring, the Go to Rust seam, a commission calculation, and where data lives |
| [001](001-bounded-contexts.md) | **Bounded Contexts** | Design intent | Why 8 contexts, what each owns, how they depend on each other |
| [002](002-context-boundaries.md) | **Context Boundaries** | Design intent | Why contexts can't reach into each other and how mutations flow |
| [003](003-network-engine-design.md) | **Network Engine Design** | Partial | Why Rust, currency-free volume, generalized trees, holding tanks, wire type separation |
| [004](004-interface-contracts.md) | **Interface Contracts** | Design intent | How contexts communicate through interfaces, events, and the extraction path |
| [005](005-multi-currency.md) | **Multi-Currency** | Design intent | Regional product catalogs, CV points, and the three-context currency chain |
| [006](006-enrollment-orchestration.md) | **Enrollment Orchestration** | Not built | The saga pattern, configurable payment failure, and structure placement |
| [007](007-unilevel-tree-implementation.md) | **Unilevel Tree Implementation** | Current | Arena storage, UUID user IDs, iterative BFS, position-indexed model |
| [008](008-common-compensation-config.md) | **Common Compensation Config** | Current | Periods, volume, ranks, eligibility, bonuses, payout, caps, placement, audit |
| [009](009-unilevel-compensation-config.md) | **Unilevel Compensation Config** | Current | Rate table, compression, pass-up variant, donated placement |
| [010](010-binary-compensation-config.md) | **Binary Compensation Config** | Current | Pairing bonus, volume-after-payout modes, carry-forward, cycle/step, spillover |
| [011](011-matrix-compensation-config.md) | **Matrix Compensation Config** | Current | Width/height, forced placement, completion bonus, position bonus, board plan |
| [012](012-stairstep-compensation-config.md) | **Stairstep Compensation Config** | Current | Breakaway threshold, differential overrides, generation counting |
| [013](013-generation-compensation-config.md) | **Generation Compensation Config** | Current | Boundary modes, generation rates, empty generations, combined level+generation |
| [014](014-streamline-compensation-config.md) | **Streamline Compensation Config** | Current | Dynamic compression, streams, rank expansion, freeze on demotion, monoline |
| [015](015-compensation-plan-schema-and-wire-format.md) | **Schema and Wire Format** | Current | One wire format, JSON Schema validation, serde renames, structural translations |
| [016](016-eventstore-design.md) | **EventStore Design** | Current | Unified store, JSON envelope, category-ID streams, optimistic concurrency, pgx v5 |
| [017](017-commission-calculation-architecture.md) | **Commission Calculation Architecture** | Current | Snapshot vs rules separation, flat earnings output, prep+walk phases, no premature abstraction |
| [018](018-config-pipeline.md) | **Config Pipeline** | Current | Five-stage validation, two-pass commission parsing, Commission marker interface, severity model |
| [019](019-ndjson-protocol.md) | **NDJSON Protocol** | Current | Request-response envelope, RawValue params, error code taxonomy, panic recovery, context cancellation |
| [020](020-tree-topology-separation.md) | **Tree Topology Separation** | Current | Trees enforce shape, callers decide placement, position validation vs placement logic |
| [021](021-sponsor-vs-placement-in-commission.md) | **Sponsor vs. Placement in Commission** | Current | Placement edges determine commission flow, sponsor edges determine personal qualification |
| [022](022-shared-commission-walk.md) | **Shared Commission Walk** | Current | Generic functions over TreeNavigator, no CommissionCalculator trait, callback injection for plan-specific behavior |
| [023](023-snapshot-persistence.md) | **Snapshot Persistence** | Current | Serde-based serialization for all tree types, JSON format, Go-managed storage |
| [024](024-generation-calculator-reuse.md) | **Generation Calculator Reuse** | Current | Why the standalone generation calculator reuses `count_generations_upward()` with a semantic mismatch instead of extracting a shared interface |
| [025](025-public-test-support-module.md) | **Public Test Support Module** | Current | Why `network_engine::test_support` is public for integration tests, and why it is still treated as internal-only support code |
| [026](026-bottom-up-rank-evaluation.md) | **Bottom-Up Rank Evaluation** | Current | Why rank evaluation iterates to a fixpoint over an accumulating descendant-rank map, so predicates read downline ranks even with multiple structure trees |
| [027](027-provenance-as-primary-data.md) | **Provenance as Primary Data** | Current | Why commission provenance is stored as primary data rather than a rebuildable event projection, and where the four kinds of commission data each live |
| [028](028-commission-config-from-validated-state.md) | **Commission Config From Validated State** | Current | Why commission handlers read the plan and structure config from `WorkerState` instead of request params, so the `load_plan` validation gate cannot be bypassed |
| [029](029-commission-provenance-on-the-wire.md) | **Commission Provenance on the Wire** | Partial | Why provenance is emitted per walk rather than per earning, why the walk index is the correlation key rather than `(earner_id, source_id)`, and why the outcome taxonomy is still provisional |

## Reading Order

Start with [000 Architecture Overview](000-architecture-overview.md). It is the map, and its diagrams link out to everything else. Then read [001 Bounded Contexts](001-bounded-contexts.md), which establishes the vocabulary. After that, [002 Context Boundaries](002-context-boundaries.md) and [004 Interface Contracts](004-interface-contracts.md) cover how the contexts are meant to communicate. The rest can be read in any order.

000 is Current. 001, 002, and 004 are Design intent. Those three give you the model the system is built toward, which is what makes them the right place to start, but do not treat the interfaces they name as code you can call. Read each banner before the body. For what is implemented today, 000's diagrams and documents 007 and up are the reliable picture.
