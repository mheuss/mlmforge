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
    #[serde(deserialize_with = "crate::serde_helpers::null_as_empty")]
    pub distributors: HashMap<Uuid, DistributorPrimitives>,

    /// Volume events for the period. Used to compute GV and leg volumes.
    #[serde(deserialize_with = "crate::serde_helpers::null_as_empty")]
    pub volume_sources: Vec<VolumeSource>,

    /// Ordered period axis for time-gated evaluation, most-recent-first
    /// (period_id DESC). Caller-supplied; length >= the max window depth
    /// across the plan. Empty when no rank uses a time gate. Opaque ordered
    /// labels — the caller owns period semantics and ordering.
    #[serde(default, deserialize_with = "crate::serde_helpers::null_as_empty")]
    pub history_window: Vec<String>,

    /// Per-distributor achieved-rank ordinals keyed by period_id, for axis
    /// periods that have a persisted row. Absent key = not evaluated;
    /// Some(None) = Unranked. Both gate as "below threshold" (BR6).
    #[serde(default, deserialize_with = "crate::serde_helpers::null_as_empty")]
    pub history: HashMap<Uuid, HashMap<String, Option<u16>>>,
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
    #[serde(deserialize_with = "crate::serde_helpers::null_as_empty")]
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

    /// A window/tenure gate's `threshold_rank` references a rank not in the plan.
    #[error("rank '{rank}' references unknown threshold rank '{referenced}'")]
    UnknownThresholdRank { rank: String, referenced: String },

    /// A rank declares a window/tenure gate but the request supplied an empty
    /// history_window. Loud failure (BR9), distinct from insufficient history
    /// (BR5, where some axis exists but is shorter than the window).
    #[error("rank '{rank}' has a time gate but no history_window was supplied")]
    TimeGateWithoutHistory { rank: String },

    /// An input distributor is not present in a tree the evaluator must walk.
    #[error("distributor {0} not found in tree '{1}'")]
    DistributorNotInTree(Uuid, String),

    /// Fixpoint iteration in `evaluate_ranks` did not stabilize within the
    /// pass bound. Rank evaluation is monotone, so this is unreachable unless
    /// a non-monotone descendant-reading predicate was introduced — a bug,
    /// not bad input.
    #[error("rank evaluation did not converge within {passes} passes")]
    RankEvaluationDidNotConverge { passes: usize },
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

    #[test]
    fn rank_evaluation_did_not_converge_displays_pass_count() {
        let err = EvaluationError::RankEvaluationDidNotConverge { passes: 7 };
        assert_eq!(
            err.to_string(),
            "rank evaluation did not converge within 7 passes"
        );
    }

    #[test]
    fn evaluation_inputs_defaults_history_when_absent() {
        let json = r#"{"distributors":{},"volume_sources":[]}"#;
        let inputs: EvaluationInputs = serde_json::from_str(json).unwrap();
        assert!(inputs.history_window.is_empty());
        assert!(inputs.history.is_empty());
    }

    #[test]
    fn unknown_threshold_rank_displays_context() {
        let err = EvaluationError::UnknownThresholdRank {
            rank: "QD".into(),
            referenced: "Director".into(),
        };
        assert_eq!(
            err.to_string(),
            "rank 'QD' references unknown threshold rank 'Director'"
        );
    }

    #[test]
    fn time_gate_without_history_displays_context() {
        let err = EvaluationError::TimeGateWithoutHistory { rank: "QD".into() };
        assert_eq!(
            err.to_string(),
            "rank 'QD' has a time gate but no history_window was supplied"
        );
    }

    #[test]
    fn evaluation_inputs_deserializes_history() {
        let uid = Uuid::nil();
        let json = format!(
            r#"{{"distributors":{{}},"volume_sources":[],
                "history_window":["2026-05","2026-04"],
                "history":{{"{uid}":{{"2026-05":2,"2026-04":null}}}}}}"#
        );
        let inputs: EvaluationInputs = serde_json::from_str(&json).unwrap();
        assert_eq!(inputs.history_window, vec!["2026-05", "2026-04"]);
        assert_eq!(inputs.history[&uid]["2026-05"], Some(2));
        assert_eq!(inputs.history[&uid]["2026-04"], None);
    }

    // --- Null tolerance (HEU-626) ---
    //
    // A nil Go map or slice marshals to JSON `null`, not `{}` or `[]`.
    // `#[serde(default)]` covers an *absent* key but not a key present with a
    // null value, so the natural first-period call was rejected outright.
    //
    // Each required field gets both halves: null reads as empty, absent stays a
    // loud error. One absence guard per required field, so adding `default` to
    // any one of them fails here rather than silently paying zero.

    /// The minimum valid payload, used to isolate one field per test.
    const MINIMAL: &str = r#"{"distributors":{},"volume_sources":[]}"#;

    /// One distributor with every `DistributorPrimitives` field but
    /// `active_products`, which each caller appends.
    fn distributor_with(active_products: &str) -> String {
        format!(
            r#"{{"personal_volume":0.0,"retail_volume":0.0,"status":"active","has_order_in_period":false{active_products}}}"#
        )
    }

    #[test]
    fn evaluation_inputs_accepts_null_distributors() {
        let json = r#"{"distributors":null,"volume_sources":[]}"#;
        let inputs: EvaluationInputs = serde_json::from_str(json).unwrap();
        assert!(inputs.distributors.is_empty());
    }

    #[test]
    fn evaluation_inputs_accepts_null_volume_sources() {
        let json = r#"{"distributors":{},"volume_sources":null}"#;
        let inputs: EvaluationInputs = serde_json::from_str(json).unwrap();
        assert!(inputs.volume_sources.is_empty());
    }

    /// The nested field. It can only be reached through a populated
    /// `distributors` map — a null outer map leaves no distributor to carry it.
    #[test]
    fn evaluation_inputs_accepts_null_active_products() {
        let uid = Uuid::nil();
        let json = format!(
            r#"{{"distributors":{{"{uid}":{}}},"volume_sources":[]}}"#,
            distributor_with(r#","active_products":null"#)
        );
        let inputs: EvaluationInputs = serde_json::from_str(&json).unwrap();
        assert!(inputs.distributors[&uid].active_products.is_empty());
    }

    #[test]
    fn evaluation_inputs_accepts_null_history_window() {
        let json = r#"{"distributors":{},"volume_sources":[],"history_window":null}"#;
        let inputs: EvaluationInputs = serde_json::from_str(json).unwrap();
        assert!(inputs.history_window.is_empty());
    }

    #[test]
    fn evaluation_inputs_accepts_null_history() {
        let json = r#"{"distributors":{},"volume_sources":[],"history":null}"#;
        let inputs: EvaluationInputs = serde_json::from_str(json).unwrap();
        assert!(inputs.history.is_empty());
    }

    #[test]
    fn evaluation_inputs_still_requires_distributors() {
        let err = serde_json::from_str::<EvaluationInputs>(r#"{"volume_sources":[]}"#)
            .expect_err("an absent distributors must still fail");
        assert!(
            err.to_string().contains("distributors"),
            "the error should name the missing field, got: {err}"
        );
    }

    #[test]
    fn evaluation_inputs_still_requires_volume_sources() {
        let err = serde_json::from_str::<EvaluationInputs>(r#"{"distributors":{}}"#)
            .expect_err("an absent volume_sources must still fail");
        assert!(
            err.to_string().contains("volume_sources"),
            "the error should name the missing field, got: {err}"
        );
    }

    #[test]
    fn evaluation_inputs_still_requires_active_products() {
        let uid = Uuid::nil();
        let json = format!(
            r#"{{"distributors":{{"{uid}":{}}},"volume_sources":[]}}"#,
            distributor_with("")
        );
        let err = serde_json::from_str::<EvaluationInputs>(&json)
            .expect_err("an absent active_products must still fail");
        assert!(
            err.to_string().contains("active_products"),
            "the error should name the missing field, got: {err}"
        );
    }

    /// `MINIMAL` is the control for the five `accepts_null` tests: it proves
    /// they fail for the field under test rather than for a malformed payload.
    #[test]
    fn evaluation_inputs_minimal_payload_deserializes() {
        let inputs: EvaluationInputs = serde_json::from_str(MINIMAL).unwrap();
        assert!(inputs.distributors.is_empty());
        assert!(inputs.volume_sources.is_empty());
        assert!(inputs.history_window.is_empty());
        assert!(inputs.history.is_empty());
    }
}
