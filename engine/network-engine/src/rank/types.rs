//! Public types for rank evaluation inputs, outputs, and errors.

use std::collections::{BTreeMap, HashMap};

use serde::{Deserialize, Serialize};
use thiserror::Error;
use uuid::Uuid;

use crate::commission::types::VolumeSource;

/// Inputs to a rank evaluation pass.
///
/// Mirrors the shape of commission inputs: per-distributor primitives plus
/// volume sources keyed by source distributor. Tree-derived quantities (GV,
/// leg volumes, downline counts) are computed by the evaluator from the
/// trees in worker state.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EvaluationInputs {
    /// Per-distributor facts, keyed by user_id.
    pub distributors: HashMap<Uuid, DistributorPrimitives>,

    /// Volume events for the period. Used to compute GV and leg volumes.
    pub volume_sources: Vec<VolumeSource>,
}

/// Point-in-time facts about one distributor for rank evaluation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DistributorPrimitives {
    /// Personal volume generated this period.
    pub personal_volume: f64,

    /// Volume generated specifically from retail customers.
    /// Used by `StructureQualification.min_retail_volume`.
    pub retail_volume: f64,

    /// Distributor's status this period (e.g., "active", "suspended").
    pub status: String,

    /// Whether the distributor placed at least one order this period.
    pub has_order_in_period: bool,

    /// Active product enrollments held by the distributor.
    /// Used by `RankQualification.required_products`.
    pub active_products: Vec<String>,
}

/// Result of evaluating one distributor against the rank ladder.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum EvaluatedRank {
    /// Distributor qualifies for this named rank.
    Qualified { rank: String, ordinal: u16 },

    /// Distributor does not qualify for any rank in the ladder.
    Unranked,
}

/// Aggregate evaluation result for the period.
///
/// `ranks` is a `BTreeMap` so JSON serialization emits user_id keys in
/// ascending order — design §1.1 doc comment and NFR #2.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EvaluationResult {
    /// Per-distributor rank, keyed by user_id. Stable ordering by user_id
    /// when serialized.
    pub ranks: BTreeMap<Uuid, EvaluatedRank>,
}

#[derive(Debug, PartialEq, Error)]
pub enum EvaluationError {
    /// A rank's qualification references a structure not present in the plan.
    #[error("rank '{rank}' references unknown structure '{structure}'")]
    UnknownStructure { rank: String, structure: String },

    /// A `DistributorCountRequirement.min_rank` or `LegPredicate::ContainsRank.min_rank`
    /// references a rank not in the plan.
    #[error("rank '{rank}' references unknown min_rank '{referenced}'")]
    UnknownMinRank { rank: String, referenced: String },

    /// An input distributor is not present in a tree the evaluator must walk.
    #[error("distributor {0} not found in tree '{1}'")]
    DistributorNotInTree(Uuid, String),
}

#[cfg(test)]
mod tests {
    use super::*;
    use uuid::Uuid;

    #[test]
    fn evaluated_rank_qualified_serializes_with_kind_tag() {
        let rank = EvaluatedRank::Qualified {
            rank: "silver".to_string(),
            ordinal: 2,
        };
        let json = serde_json::to_string(&rank).unwrap();
        assert!(json.contains(r#""kind":"qualified""#));
        assert!(json.contains(r#""rank":"silver""#));
        assert!(json.contains(r#""ordinal":2"#));
    }

    #[test]
    fn evaluated_rank_unranked_serializes_with_kind_tag() {
        let rank = EvaluatedRank::Unranked;
        let json = serde_json::to_string(&rank).unwrap();
        assert_eq!(json, r#"{"kind":"unranked"}"#);
    }

    #[test]
    fn evaluation_result_round_trips_per_distributor_ranks() {
        let mut ranks = std::collections::BTreeMap::new();
        ranks.insert(
            Uuid::nil(),
            EvaluatedRank::Qualified {
                rank: "associate".to_string(),
                ordinal: 1,
            },
        );
        let result = EvaluationResult { ranks };
        let json = serde_json::to_string(&result).unwrap();
        let restored: EvaluationResult = serde_json::from_str(&json).unwrap();
        assert_eq!(restored.ranks.len(), 1);
    }

    #[test]
    fn evaluation_result_serializes_keys_in_user_id_order() {
        // Stable ordering invariant: NFR #2 (design §Non-Functional Requirements).
        let mut ranks = std::collections::BTreeMap::new();
        // Insert in reverse order — BTreeMap keeps them sorted.
        ranks.insert(Uuid::from_u128(3), EvaluatedRank::Unranked);
        ranks.insert(Uuid::from_u128(1), EvaluatedRank::Unranked);
        ranks.insert(Uuid::from_u128(2), EvaluatedRank::Unranked);
        let result = EvaluationResult { ranks };
        let json = serde_json::to_string(&result).unwrap();

        let pos1 = json.find(&Uuid::from_u128(1).to_string()).unwrap();
        let pos2 = json.find(&Uuid::from_u128(2).to_string()).unwrap();
        let pos3 = json.find(&Uuid::from_u128(3).to_string()).unwrap();
        assert!(
            pos1 < pos2 && pos2 < pos3,
            "user_id keys must serialize in ascending order"
        );
    }
}
