//! Types for commission calculation.

use crate::config::matrix::SpilloverDirection;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use thiserror::Error;
use uuid::Uuid;

/// Point-in-time facts about a distributor for a commission period.
///
/// Contains only observable data. The calculator derives all eligibility
/// and depth decisions from the compensation plan config.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DistributorSnapshot {
    /// Current rank name. Must match a rank in the plan's rank ladder.
    pub rank: String,

    /// Personal volume generated this period.
    pub personal_volume: f64,

    /// Distributor's current status (e.g., "active", "grace", "suspended").
    pub status: String,

    /// Whether the distributor placed at least one order this period.
    pub has_order_in_period: bool,
}

/// A volume event that triggers commission calculation.
///
/// Each volume source produces one upline walk. The walk pays
/// commissions to eligible ancestors based on the rate table.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct VolumeSource {
    /// The distributor who generated this volume.
    pub source_id: Uuid,

    /// Commission volume points generated.
    pub cv_amount: f64,
}

/// A single commission earning. One entry per earner per volume source.
///
/// The dollar amount formula:
/// `cv_amount * broad_commission_percent * volume_to_dollar_multiplier * rate`
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct CommissionEarning {
    /// The distributor who earned this commission.
    pub earner_id: Uuid,

    /// The distributor whose volume triggered the earning.
    pub source_id: Uuid,

    /// Level in the (possibly compressed) upline walk. 1-indexed.
    pub level: u8,

    /// Rate table value applied at this level for this rank.
    pub rate: f64,

    /// Input commission volume from the source.
    pub cv_amount: f64,

    /// Final payout amount in the plan's base currency.
    pub dollar_amount: f64,
}

/// Errors that halt the entire commission calculation.
///
/// These indicate data integrity problems in the caller's input.
/// Recoverable issues (missing upline snapshots) are handled
/// defensively within the calculation.
#[derive(Debug, PartialEq, Error)]
pub enum CalculationError {
    /// A volume source references a distributor not in the tree.
    #[error("volume source {0} not found in tree")]
    SourceNotInTree(Uuid),

    /// A volume source references a distributor with no snapshot data.
    #[error("volume source {0} not found in snapshot data")]
    SourceNotInSnapshot(Uuid),

    /// A volume source has a non-finite or negative cv_amount.
    #[error("volume source {0} has invalid cv_amount: {1}")]
    InvalidCvAmount(Uuid, f64),

    /// Commission config failed validation.
    #[error("config error: {0}")]
    ConfigError(String),

    /// The tree's topology does not match the structure config it pays against.
    /// Paying against a mismatched tree computes commissions on the wrong shape.
    #[error(
        "matrix tree for structure '{structure}' does not match config: \
         width {actual_width} vs expected {expected_width}, \
         spillover {actual_spillover:?} vs expected {expected_spillover:?}"
    )]
    TreeConfigMismatch {
        structure: String,
        expected_width: u8,
        actual_width: u8,
        expected_spillover: SpilloverDirection,
        actual_spillover: SpilloverDirection,
    },
}

/// Per-distributor leg volumes carried from the previous period.
///
/// Used as both input (carry-forward from prior period) and output
/// (post-payout state for the next period) of binary commission
/// calculation.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct LegVolumes {
    pub left: f64,
    pub right: f64,
}

/// A single binary pairing commission earning.
///
/// One entry per distributor who earned a pairing bonus. No entry for
/// zero matched volume or ineligible distributors.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct BinaryCommissionEarning {
    /// The distributor who owns this position and receives the payout.
    /// In single-position mode, same as position_id.
    pub earner_id: Uuid,

    /// The tree position (income center) that generated this earning.
    /// In single-position mode, same as earner_id.
    pub position_id: Uuid,

    /// Total volume in the left leg (current period + carry-forward).
    pub left_volume: f64,

    /// Total volume in the right leg (current period + carry-forward).
    pub right_volume: f64,

    /// Volume matched between legs: min(left, right).
    pub matched_volume: f64,

    /// Balance ratio applied. 1.0 for WeakerLeg, min/max for VolumeRatio.
    pub ratio: f64,

    /// The pairing percent from config.
    pub percent: f64,

    /// Final payout amount after multiplier and cap.
    pub dollar_amount: f64,

    /// True if cap_per_period reduced this earning.
    pub capped: bool,
}

/// Result of a binary pairing commission calculation.
///
/// Contains earnings for distributors who earned pairing bonuses
/// and updated carry-forward state for every distributor in the tree.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BinaryCalculationResult {
    /// Earnings for distributors who earned a pairing bonus.
    pub earnings: Vec<BinaryCommissionEarning>,

    /// Post-payout leg volumes for every distributor in the tree.
    /// Keyed by user_id. Includes non-earners (they accumulate volume).
    pub carry_forward: HashMap<Uuid, LegVolumes>,
}

/// Which traversal mechanic produced a walk.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum WalkKind {
    Level,
    Generation,
}

/// What happened to a node the walk visited.
///
/// Both variants are consumed. Non-consuming skips are not recorded in
/// protocol version 2, so a walk's `steps` is the consumed subset of the
/// path, not the full ordered node list.
///
/// `Forfeited` deliberately does not say why the level was forfeited. One
/// of its three branches is a per-distributor depth cap, which design 029
/// names `depth_cap` and forbids shipping as an outcome until HEU-556
/// settles whether it is independently verifiable. Naming that branch
/// correctly would break 029; naming it anything else would assert a
/// reason the code does not support.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum StepOutcome {
    /// The node earned.
    Paid,
    /// The node consumed a level without earning.
    Forfeited,
}

/// How a walk ended.
///
/// One variant per real exit. A traversal that cannot make one of these
/// claims emits no walk at all.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum WalkStop {
    /// The upline ran out. No node caused this, so `stopped_at` is absent.
    RootReached,
    /// The configured depth limit.
    MaxDepthReached,
    /// The caller's stop predicate.
    ///
    /// Named for the boundary rather than for stairstep's breakaway,
    /// because the predicate is caller-supplied and stairstep is only its
    /// current user. If a calculator ever passes a predicate that is not a
    /// boundary in any meaningful sense, this name stops being honest and
    /// must change before that caller ships.
    BoundaryReached,
    /// The configured generation limit.
    MaxGenerationsReached,
}

/// One visited node and what the walk decided about it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct WalkStep {
    pub node_id: Uuid,

    pub outcome: StepOutcome,

    /// Whether this step advanced the walk's level counter. Counter
    /// reconstruction is a count of consumed steps.
    pub consumed: bool,

    /// The rank the calculator read for this node.
    ///
    /// Named `earner_rank` rather than `rank` because stairstep Walk 2's
    /// differential resolves its rate from both the ancestor's rank and the
    /// breakaway's. The narrow name stops the field being widened by
    /// assumption when those earnings arrive.
    ///
    /// Absent where no rank was read, never null-and-present. A rank in an
    /// audit record implies a rank that affected the payout, so emitting one
    /// the calculation never used is misleading.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub earner_rank: Option<String>,
}

/// One traversal, and the decisions along it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Walk {
    /// Position in the response's total order. Earnings reference this.
    pub index: u32,

    /// The distributor whose volume triggered the traversal.
    pub source_id: Uuid,

    pub kind: WalkKind,

    /// Streamline context. Absent for every other calculator.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stream_id: Option<u32>,

    /// Generation SameRank context. Absent otherwise.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub rank: Option<String>,

    /// Visited nodes in order, consumed steps only.
    pub steps: Vec<WalkStep>,

    /// How the traversal ended.
    pub stop: WalkStop,

    /// The node that caused the stop.
    ///
    /// Present for every stop except `RootReached`, where the upline simply
    /// ran out and naming a node would be false. Absent means no node caused
    /// it, never that the node is unknown.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stopped_at: Option<Uuid>,
}

/// The plan the engine actually had when it calculated.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct PlanIdentity {
    /// The plan's display name, as `CompensationPlan.name`.
    pub name: String,

    /// The plan's schema version, as `CompensationPlan.version`. Not the
    /// NDJSON protocol version, which moves independently.
    pub version: u32,
    /// `sha256:<64 lowercase hex>`, over the raw `load_plan` bytes.
    pub hash: String,
}

/// What a commission calculator returns.
///
/// Supersedes design 017's bare `Vec<CommissionEarning>` return contract.
/// The earnings list inside keeps 017's shape exactly.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct CommissionCalculationResult {
    pub earnings: Vec<CommissionEarning>,
    pub walks: Vec<Walk>,
    pub plan: PlanIdentity,
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers::uuid_from_index;

    #[test]
    fn walk_serializes_with_absent_optional_context() {
        let walk = Walk {
            index: 7,
            source_id: uuid_from_index(1),
            kind: WalkKind::Level,
            stream_id: None,
            rank: None,
            steps: vec![WalkStep {
                node_id: uuid_from_index(2),
                outcome: StepOutcome::Paid,
                consumed: true,
                earner_rank: Some("member".to_string()),
            }],
            stop: WalkStop::RootReached,
            stopped_at: None,
        };
        let json = serde_json::to_value(&walk).expect("serialize walk");
        // Wire strings belong to every_stop_and_outcome_value_has_its_wire_name.
        // This test owns the omission rule only.
        assert!(
            json.get("stream_id").is_none(),
            "absent context must not serialize: {json}"
        );
        assert!(
            json.get("stopped_at").is_none(),
            "absent stopped_at must not serialize: {json}"
        );
        assert!(
            json.get("rank").is_none(),
            "absent rank must be omitted, not null: {json}"
        );
        // Distinct UUIDs above, so a serializer that swapped these two or
        // dropped one would fail here rather than pass on a shared nil.
        assert_eq!(json["index"], 7);
        assert_eq!(json["source_id"], uuid_from_index(1).to_string());
        assert_eq!(json["steps"][0]["node_id"], uuid_from_index(2).to_string());
    }

    #[test]
    fn a_walk_round_trips_through_its_own_serialized_form() {
        // The omitted-optional case is the common one: every level walk omits
        // stream_id and rank, and every root_reached walk omits stopped_at.
        //
        // This round-trips without `#[serde(default)]` because serde treats
        // `Option<T>` specially — an absent field deserializes to `None` on
        // its own. `default` would be required if any of these stopped being
        // an `Option` while keeping `skip_serializing_if`, which is the
        // change this test exists to catch.
        let walk = Walk {
            index: 3,
            source_id: Uuid::nil(),
            kind: WalkKind::Level,
            stream_id: None,
            rank: None,
            steps: vec![WalkStep {
                node_id: Uuid::nil(),
                outcome: StepOutcome::Forfeited,
                consumed: true,
                earner_rank: None,
            }],
            stop: WalkStop::RootReached,
            stopped_at: None,
        };
        let json = serde_json::to_string(&walk).expect("serialize walk");
        let back: Walk = serde_json::from_str(&json).expect("deserialize walk");
        assert_eq!(back, walk);
    }

    // The three functions below go through an exhaustive `match` rather than
    // a literal list. Adding a variant then fails to compile until someone
    // pins its wire string, which a bare array would not do.
    fn stop_wire_name(stop: WalkStop) -> &'static str {
        match stop {
            WalkStop::RootReached => "root_reached",
            WalkStop::MaxDepthReached => "max_depth_reached",
            WalkStop::BoundaryReached => "boundary_reached",
            WalkStop::MaxGenerationsReached => "max_generations_reached",
        }
    }

    fn outcome_wire_name(outcome: StepOutcome) -> &'static str {
        match outcome {
            StepOutcome::Paid => "paid",
            StepOutcome::Forfeited => "forfeited",
        }
    }

    fn kind_wire_name(kind: WalkKind) -> &'static str {
        match kind {
            WalkKind::Level => "level",
            WalkKind::Generation => "generation",
        }
    }

    #[test]
    fn every_stop_and_outcome_value_has_its_wire_name() {
        // These strings are persisted by HEU-46. Changing one orphans every
        // row carrying the old value, so pin them rather than trusting the
        // rename_all attribute.
        for stop in [
            WalkStop::RootReached,
            WalkStop::MaxDepthReached,
            WalkStop::BoundaryReached,
            WalkStop::MaxGenerationsReached,
        ] {
            assert_eq!(
                serde_json::to_value(stop).expect("serialize stop"),
                stop_wire_name(stop)
            );
        }

        for outcome in [StepOutcome::Paid, StepOutcome::Forfeited] {
            assert_eq!(
                serde_json::to_value(outcome).expect("serialize outcome"),
                outcome_wire_name(outcome)
            );
        }

        for kind in [WalkKind::Level, WalkKind::Generation] {
            assert_eq!(
                serde_json::to_value(kind).expect("serialize kind"),
                kind_wire_name(kind)
            );
        }
    }

    /// The presence rule for `stopped_at`, stated once so the test and any
    /// future constructor agree: absent for `RootReached`, present for every
    /// other stop.
    fn stop_names_a_node(stop: WalkStop) -> bool {
        match stop {
            WalkStop::RootReached => false,
            WalkStop::MaxDepthReached
            | WalkStop::BoundaryReached
            | WalkStop::MaxGenerationsReached => true,
        }
    }

    #[test]
    fn stopped_at_serializes_for_every_stop_that_names_a_node() {
        for stop in [
            WalkStop::RootReached,
            WalkStop::MaxDepthReached,
            WalkStop::BoundaryReached,
            WalkStop::MaxGenerationsReached,
        ] {
            let stopped_at = stop_names_a_node(stop).then(|| uuid_from_index(9));
            let walk = Walk {
                index: 0,
                source_id: uuid_from_index(1),
                kind: WalkKind::Level,
                stream_id: None,
                rank: None,
                steps: Vec::new(),
                stop,
                stopped_at,
            };
            let json = serde_json::to_value(&walk).expect("serialize walk");
            assert_eq!(
                json.get("stopped_at").is_some(),
                stop_names_a_node(stop),
                "stop {:?} got the wrong stopped_at presence: {json}",
                stop
            );
        }
    }

    #[test]
    fn a_populated_walk_round_trips_through_its_own_serialized_form() {
        // The all-None case is covered above. This is the one with more to go
        // wrong: every optional present, a non-Level kind, a non-root stop.
        let walk = Walk {
            index: 4,
            source_id: uuid_from_index(1),
            kind: WalkKind::Generation,
            stream_id: Some(2),
            rank: Some("silver".to_string()),
            steps: vec![WalkStep {
                node_id: uuid_from_index(3),
                outcome: StepOutcome::Paid,
                consumed: true,
                earner_rank: Some("gold".to_string()),
            }],
            stop: WalkStop::MaxGenerationsReached,
            stopped_at: Some(uuid_from_index(4)),
        };
        let json = serde_json::to_string(&walk).expect("serialize walk");
        let back: Walk = serde_json::from_str(&json).expect("deserialize walk");
        assert_eq!(back, walk);
    }

    #[test]
    fn plan_identity_carries_the_hash_format_plan_hash_go_produces() {
        // `internal/networkengine/plan_hash.go` emits "sha256:" plus 64
        // lowercase hex characters, and the commission_runs table has a CHECK
        // on that prefix. This pins the Rust side of that shared format.
        let identity = PlanIdentity {
            name: "Integration Test Plan".to_string(),
            version: 1,
            hash: format!("sha256:{}", "a1b2c3d4".repeat(8)),
        };
        let json = serde_json::to_value(&identity).expect("serialize identity");
        assert_eq!(json["name"], "Integration Test Plan");
        assert_eq!(json["version"], 1);

        let hash = json["hash"].as_str().expect("hash is a string");
        let hex = hash
            .strip_prefix("sha256:")
            .expect("hash must carry the sha256: prefix");
        assert_eq!(
            hex.len(),
            64,
            "expected 64 hex characters, got {}",
            hex.len()
        );
        assert!(
            hex.chars()
                .all(|c| c.is_ascii_digit() || c.is_ascii_lowercase()),
            "hash must be lowercase hex: {hash}"
        );
    }

    #[test]
    fn calculation_result_serializes_its_three_keys() {
        let result = CommissionCalculationResult {
            earnings: Vec::new(),
            walks: Vec::new(),
            plan: PlanIdentity {
                name: "Test".to_string(),
                version: 1,
                hash: format!("sha256:{}", "0".repeat(64)),
            },
        };
        let json = serde_json::to_value(&result).expect("serialize result");
        // Empty collections serialize as [], never null. A null here would
        // read to the Go client as "no walks recorded" rather than "none".
        assert_eq!(json["earnings"], serde_json::json!([]));
        assert_eq!(json["walks"], serde_json::json!([]));
        assert!(json["plan"].is_object());
    }

    #[test]
    fn tree_config_mismatch_message_names_structure_and_values() {
        let err = CalculationError::TreeConfigMismatch {
            structure: "Test".to_string(),
            expected_width: 3,
            actual_width: 2,
            expected_spillover: SpilloverDirection::BreadthFirst,
            actual_spillover: SpilloverDirection::DepthFirst,
        };
        let msg = err.to_string();
        assert!(
            msg.contains("Test"),
            "message must name the structure: {msg}"
        );
        assert!(msg.contains("does not match config"), "message: {msg}");
        assert!(msg.contains("width 2 vs expected 3"), "message: {msg}");
        assert!(
            msg.contains("spillover DepthFirst vs expected BreadthFirst"),
            "message must report actual-vs-expected spillover in order: {msg}"
        );
    }
}
