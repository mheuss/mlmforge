# Development Notes

Implementation patterns and conventions for active subsystems. Read the relevant file before working on a subsystem.

These notes cover *how* to work within a subsystem. For *why* the subsystem is designed the way it is, see [`content/design-rationale/`](../../content/design-rationale/INDEX.md).

| Area | File | Related design rationale |
|------|------|--------------------------|
| Network Engine | [network-engine.md](network-engine.md) | [003](../../content/design-rationale/003-network-engine-design.md), [007](../../content/design-rationale/007-unilevel-tree-implementation.md), [017](../../content/design-rationale/017-commission-calculation-architecture.md), [020](../../content/design-rationale/020-tree-topology-separation.md) |
| Config Types (Go/Rust alignment) | [config-types.md](config-types.md) | [028](../../content/design-rationale/028-commission-config-from-validated-state.md) |
| Postgres Stores (Go/Postgres seams) | [postgres-stores.md](postgres-stores.md) | [027](../../content/design-rationale/027-provenance-as-primary-data.md) |
