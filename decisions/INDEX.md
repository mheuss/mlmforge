# Architecture Decisions

These documents explain how MLMForge is designed and why. Each one covers a focused topic with the problem, the decision, and the reasoning.

Written for developers, architects, and evaluators who want to understand the system without reading the full development history.

MLMForge's architecture is informed by years of building and operating a production MLM platform. These documents reference that predecessor as "the legacy system." The decisions here address problems we encountered firsthand.

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

## Reading Order

Start with [001 Bounded Contexts](001-bounded-contexts.md). It establishes the vocabulary for everything else. Then read [002 Context Boundaries](002-context-boundaries.md) and [004 Interface Contracts](004-interface-contracts.md) to understand how the contexts communicate. The rest can be read in any order.
