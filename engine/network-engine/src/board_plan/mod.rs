//! Board plan engine — cycling matrix variant.
//!
//! Manages multiple small boards (flat BFS-ordered position arrays).
//! When a board fills, the top position cycles out and earns a commission.
//! The board splits into new boards headed by second-level members.

pub mod board;
pub mod engine;
pub mod error;
pub mod types;

pub use board::Board;
pub use engine::BoardPlanEngine;
pub use error::BoardPlanError;
pub use types::*;
