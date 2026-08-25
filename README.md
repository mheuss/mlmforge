# MLMForge

An open source platform for network marketing companies. The goal is software that works out of the box for a startup. Scales to millions of distributors for enterprise deployments.

## What It Does

MLMForge handles core operations for a network marketing company.

- Compensation plans, commission calculations, rank qualification, and bonuses
- Product catalog, e-commerce, autoship, and order fulfillment
- Payment processing, wallets, invoicing, and disbursement
- Distributor enrollment, team structures, and placement
- Messaging, templates, and communication workflows
- Customer service ticketing and reporting
- Admin panel and distributor back office

Compensation plans are defined in YAML. Unilevel, binary, matrix, stairstep, generation, streamline, and combinations of these run simultaneously. Different product classes route to different compensation structures. The commission engine is event-sourced. Every calculation is reproducible, auditable, and replayable against proposed plan changes before they go live.

## Architecture

A modular monolith. Eight bounded contexts in a single Go binary with the commission engine in Rust. Each context owns its own PostgreSQL schema and communicates through defined interfaces.

| Component | Language | Purpose |
|-----------|----------|---------|
| Application layer | Go | HTTP API, business logic, all contexts except the commission engine |
| Network Engine | Rust | Tree walking, volume aggregation, commission calculation |
| Database | PostgreSQL | Event store, relational data, one schema per context |
| Frontend | React/Next.js | Reference admin panel and distributor back office |

### Deployment Spectrum

The same codebase supports multiple topologies.

- **Single binary.** One process, one database. Clone, build, run.
- **Engine separation.** The Rust commission engine runs as a subprocess communicating via NDJSON over stdin/stdout (StdioTransport). A gRPC transport can replace this for distributed deployment.
- **Full decomposition.** Each bounded context runs as its own service. Same code, different wiring.

Context boundaries are enforced through interfaces and schema isolation. Extraction is a deployment decision, not a rewrite.

### Extensibility

Three layers of customization.

- **YAML configuration.** Compensation plans, ranks, bonuses, and qualification criteria as structured data.
- **WASM plugins.** Custom logic inside the Rust engine, sandboxed and in-process.
- **Webhooks and NATS.** Synchronous extension points and async event integrations.

## Compensation Structure Guides

New to MLMForge? The [compensation structure guides](content/guides/INDEX.md) explain what each structure does, how commissions flow, and what options you have. Written for plan designers. No code, no config syntax. Just how things work.

## Design Decisions

The reasoning behind the architecture is documented in [content/design-rationale/](content/design-rationale/INDEX.md). Why 8 contexts, how they communicate, the currency-free commission engine, and the trade-offs along the way. Start there to understand the *why* before the *how*.

## Status

Active development. The commission engine handles all six compensation structure types: unilevel, binary, matrix, stairstep breakaway, generation, and streamline. Each has a dedicated Rust calculator with compression, active leg tiers, rate table lookup, and structure-specific mechanics (pairing/carry-forward for binary, breakaway/differential overrides for stairstep, boundary modes for generation, dynamic compression for streamline). Board plan cycling is implemented as a separate engine. Tree persistence uses an event-sourced adjacency table with startup bulk-load. A migration framework handles schema evolution. The Go/Rust integration boundary uses NDJSON over stdin/stdout with contract test coverage. 1018 Rust tests and 580 Go tests. 30 design rationale documents. Next up: HTTP API layer, placement service wiring, and additional persistence for board plan and streamline structures.
