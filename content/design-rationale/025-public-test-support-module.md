# 025: Public Test Support Module

## The Problem

Rust integration tests in `engine/network-engine/tests/` compile as separate crates. They cannot import helpers from `commission::test_helpers` because that module is behind `#[cfg(test)]` and scoped inside the commission package. This led to duplicated plan builders, duplicated `uuid_from_index()`, and repeated `DistributorSnapshot` and `RankDefinition` boilerplate.

The obvious fix is to expose shared helpers from the library. That introduces a risk: a test-only helper module can look like supported production API if it is published without explanation.

## The Decision

Add `network_engine::test_support` as a small public module. Keep it explicitly documented as test-only support, not stable runtime API.

Use it for:
- shared plan construction
- deterministic UUID generation
- default snapshot constructors
- rank constructors with standard defaults

Hide it from generated docs with `#[doc(hidden)]` at the module export site, and document the tradeoff in code and here.

## The Reasoning

Integration tests need a library-visible module. There is no cleaner way for `tests/` to share helpers with unit tests without either:
- widening an internal test module in an arbitrary feature area, or
- duplicating helpers in both `src/` and `tests/`.

A dedicated `test_support` module is the least awkward option. It gives one obvious home for cross-test helpers and keeps the decision local to the crate root instead of tying integration tests to `commission/`.

The public surface risk is real, so the mitigation is part of the design:
- keep the module narrowly scoped
- keep the defaults boring and obvious
- avoid runtime behavior or business logic here
- document that the module exists for tests and has no stability guarantees for external callers

## Revisit Trigger

Revisit this if `network-engine` becomes a genuinely external crate with third-party consumers. At that point, move shared test helpers behind a dedicated crate feature or a separate internal test-support crate.
