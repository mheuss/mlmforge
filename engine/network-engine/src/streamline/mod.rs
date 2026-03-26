//! Streamline tree structure — linear chains with rank-based stream expansion.
//!
//! Each stream is a width-1 UnilevelTree. The engine manages multiple streams
//! per structure, supporting rank-based expansion, freeze on demotion, and
//! multiple placement modes (sponsor_stream, round_robin, explicit choice).

pub mod engine;
pub mod error;
pub mod types;

pub use engine::StreamlineEngine;
pub use error::StreamlineError;
