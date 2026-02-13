//! Compensation plan configuration types.
//!
//! These types define the contract between the Go application layer
//! and the Rust commission engine. The Go layer validates, stores,
//! and deserializes configuration. The engine receives fully typed
//! structs and produces commission results.
//!
//! Every type and field is documented with its business meaning.
//! This module IS the developer reference for the configuration
//! surface. See the design document for additional narrative context:
//! `docs/plans/2026-02-12-compensation-plan-config-design.md`

pub mod binary;
pub mod commission;
pub mod eligibility;
pub mod generation;
pub mod period;
pub mod rank;
pub mod stairstep;
pub mod streamline;
pub mod volume;
