//! Per-period rank evaluation against the plan's rank ladder.

pub mod evaluator;
pub mod predicates;
pub mod types;

pub use types::{
    DistributorPrimitives, EvaluatedRank, EvaluationError, EvaluationInputs, EvaluationResult,
};
