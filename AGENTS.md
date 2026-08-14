# mlmforge

## provides
- engine: NDJSON over stdin/stdout subprocess (StdioTransport, current)
- grpc: planned for distributed deployment
- http: planned (REST + GraphQL API)

## requires
- infra: postgresql:5432
- services: none (self-contained in monolith mode)

## local
- start: not yet implemented (no HTTP server; engine runs as subprocess via Go test harness)
- test (Go): `go test ./...`
- test (Rust): `cargo test`

## codegraphcontext
- note: mixed-language root indexing is unreliable. Prefer language-specific subtrees over the repository root.
- rust scope: `<repo-root>/engine`
- go scope: `<repo-root>/cmd`
- go scope: `<repo-root>/internal`
- sql/schema scope: `<repo-root>/schemas`
- usage: when running CGC queries, target the subtree that contains the file or function you are inspecting

## upstream
- (distributor portal. React/Next.js frontend. planned)
- (admin portal. React/Next.js frontend. planned)
- (third-party integrations via API. planned)

## downstream
- postgresql:5432 (all context schemas)

## contexts (monolith mode. all in-process)

### network-engine
- language: Rust
- provides: commission calculation, tree management, rank qualification, bonus computation
- schema: network_engine
- events publishes: commission.calculated, rank.changed, bonus.awarded, volume.updated

### commerce
- language: Go
- provides: product catalog, cart, checkout, autoship, fulfillment
- schema: commerce
- events publishes: order.completed, order.refunded, autoship.processed

### financial
- language: Go
- provides: payment processing, billing, wallet, accounting
- schema: financial
- events publishes: payment.processed, payment.failed, payout.completed

### identity
- language: Go
- provides: user management, enrollment, authentication, authorization
- schema: identity
- events publishes: enrollment.completed, user.updated, user.deactivated

### engagement
- language: Go
- provides: email campaigns, events, CRM, media management
- schema: engagement
- events publishes: email.sent, event.registered

### operations
- language: Go
- provides: reporting, customer service, CMS, configuration, i18n
- schema: operations

### portals
- language: Go
- provides: admin API routes, distributor back office API routes
- note: thin API consumer layer, no own schema

### platform
- language: Go
- provides: runtime, jobs, config, event persistence, encryption, sessions, audit
- schema: platform
- events publishes: job.completed, job.failed
