//! Network Engine — the core commission calculation engine for MLMForge.
//!
//! Handles tree structures, commission calculations, rank qualification,
//! and bonus computation. Fully event-sourced.

pub mod board_plan;
pub mod commission;
pub mod config;
pub mod rank;
pub mod serde_helpers;
pub mod streamline;
#[doc(hidden)]
pub mod test_support;
pub mod tree;
pub mod types;
