//! Rank definition and qualification types.

use serde::{Deserialize, Serialize};

use crate::serde_helpers::null_as_empty;

/// A single rank in the compensation plan ladder.
///
/// Ranks are the ladder distributors climb. Higher ranks unlock deeper
/// commission levels, higher percentages, and bonus eligibility. Ranks
/// are shared across the entire plan, not per-structure. A distributor
/// holds one rank that may qualify them for commissions on multiple
/// structures.
///
/// Ranks are evaluated lowest-to-highest ordinal. Failing rank N does
/// not short-circuit — higher ranks may still pass. The highest passing
/// rank wins, which preserves ladder-gap semantics where a distributor
/// satisfies rank N+1 but not rank N (e.g., missing a required product
/// unique to N).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RankDefinition {
    /// Human-readable rank name. Must be unique within the plan.
    pub name: String,

    /// Numeric position in the rank ladder. Higher is better.
    ///
    /// Must be unique across all ranks. The engine evaluates ranks in
    /// ascending ordinal order.
    pub ordinal: u16,

    /// Criteria the distributor must meet to achieve this rank.
    ///
    /// All criteria within the qualification are AND-combined. Failing
    /// any single criterion fails the entire rank.
    pub qualification: RankQualification,

    /// Structures the distributor may earn commissions on at this rank.
    ///
    /// In a hybrid plan (e.g., binary + unilevel), lower ranks might
    /// earn only on the unilevel while higher ranks unlock the binary.
    /// References structure names defined in the plan.
    #[serde(default, deserialize_with = "null_as_empty")]
    pub qualified_structures: Vec<String>,

    /// How demotion is handled when the distributor fails to maintain
    /// this rank.
    ///
    /// Either a grace period (time to recover before demotion) or
    /// promotion-only (ranks never decrease). These are mutually
    /// exclusive at the type level.
    pub demotion_policy: DemotionPolicy,
}

/// Qualification criteria for achieving a rank.
///
/// Combines per-structure volume and team requirements with product
/// enrollment requirements. All criteria are AND-combined.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RankQualification {
    /// Volume and team requirements evaluated on specific structures.
    ///
    /// A rank can require criteria on multiple structures simultaneously.
    /// For example: "Diamond requires 10,000 GV on the unilevel AND
    /// 50,000 total leg volume on the binary." All structure criteria
    /// must be met.
    pub structures: Vec<StructureQualification>,

    /// Product IDs the distributor must hold an active enrollment for.
    ///
    /// The distributor must maintain a current membership of at least
    /// one of the specified products. Used to tie rank eligibility to
    /// enrollment package tier.
    #[serde(default, deserialize_with = "null_as_empty")]
    pub required_products: Vec<String>,

    /// Optional windowed gate (G2): achieved >= threshold rank in N of the
    /// last M prior periods. Absent = no windowed requirement.
    #[serde(default)]
    pub window: Option<RankQualificationWindow>,

    /// Optional tenure gate (G3): achieved >= threshold rank for `periods`
    /// consecutive prior periods. Absent = no tenure requirement.
    #[serde(default)]
    pub tenure: Option<TenureRequirement>,
}

/// "Achieved >= threshold_rank in qualifying_periods of the last
/// window_periods prior periods." Invariant: qualifying_periods <=
/// window_periods, both >= 1 (validated by the Go config layer and schema).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RankQualificationWindow {
    pub threshold_rank: String,
    pub qualifying_periods: u8,
    pub window_periods: u8,
}

/// "Achieved >= threshold_rank for `periods` consecutive prior periods."
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TenureRequirement {
    pub threshold_rank: String,
    pub periods: u8,
}

/// Volume and team requirements for a single structure.
///
/// All criteria within a structure qualification are AND-combined.
/// Failing any criterion fails the rank.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StructureQualification {
    /// The structure these criteria apply to. References a structure
    /// name defined in the plan.
    pub structure: String,

    /// Minimum personal volume (PV) the distributor must generate
    /// this period on this structure.
    pub personal_volume: f64,

    /// Minimum group volume (GV) from the distributor and their
    /// entire downline on this structure, excluding breakaway groups.
    pub group_volume: f64,

    /// Maximum volume any single leg can contribute toward group volume.
    ///
    /// Prevents qualifying on one strong leg. Must be less than or
    /// equal to `group_volume`. For example, if group_volume is 10,000
    /// and this is 6,000, no single leg can contribute more than 60%.
    pub max_group_volume_per_leg: f64,

    /// Minimum volume from retail customers specifically.
    ///
    /// Ensures the rank is not achieved purely through self-purchases.
    /// Supports regulatory compliance by requiring genuine retail sales.
    pub min_retail_volume: f64,

    /// Team composition requirements on this structure.
    ///
    /// When present, the distributor must have a team of a certain
    /// size and quality to achieve the rank.
    pub distributor_count: Option<DistributorCountRequirement>,

    /// Per-leg structural requirements. Empty (the default) means no
    /// leg-quality criterion. All requirements are AND-combined with each
    /// other and with the rest of the qualification.
    #[serde(default)]
    pub leg_quality: Vec<LegQualityRequirement>,
}

/// Team composition requirements for rank qualification.
///
/// Defines how many distributors of a minimum rank must exist in the
/// downline, where to search for them, and volume floors for their
/// groups.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DistributorCountRequirement {
    /// Minimum number of distributors at `min_rank` or higher.
    pub count: u16,

    /// The minimum rank each qualifying distributor must hold.
    ///
    /// Must reference a rank with a lower ordinal than the rank
    /// being qualified for.
    pub min_rank: String,

    /// Where to search for qualifying distributors.
    ///
    /// `first_levels` rewards personal recruiting and close mentorship.
    /// `any_level` rewards deep building but makes qualification easier
    /// through spillover or inherited downlines.
    pub search_mode: SearchMode,

    /// How many levels deep to search when `search_mode` is `first_levels`.
    ///
    /// Required when search_mode is `first_levels`. Ignored when
    /// search_mode is `any_level`.
    pub search_depth: Option<u8>,

    /// Minimum total number of distributors at any rank on the same
    /// levels as the qualifying distributors.
    ///
    /// Ensures breadth. The team must have both leaders (counted by
    /// `count`) and a minimum overall size.
    pub total_count: u16,

    /// Minimum group volume each qualifying distributor's group must
    /// maintain.
    ///
    /// Ensures the leaders being counted are actually producing.
    /// A distributor with the right rank but no volume does not count.
    pub min_leg_group_volume: f64,
}

/// A per-leg structural requirement for rank qualification.
///
/// Requires at least `count` of the distributor's frontline legs to each
/// contain a node matching `predicate`. A "leg" is a direct child plus
/// that child's entire subtree.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LegQualityRequirement {
    /// Minimum number of frontline legs that must satisfy `predicate`.
    pub count: u16,
    /// The condition at least one node in a leg's subtree must meet.
    pub predicate: LegPredicate,
}

/// The condition a leg's subtree is tested against.
///
/// Internally tagged: serializes as `{"type": "contains_rank", ...}`. The
/// content is string-keyed, so the contract-test harness gotcha for
/// adjacently-tagged enums (see docs/development/network-engine.md) does
/// not apply.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum LegPredicate {
    /// Leg contains a node whose evaluated rank ordinal is at least the
    /// ordinal of `min_rank`.
    ContainsRank { min_rank: String },
    /// Leg contains a node whose personal volume is at least
    /// `min_personal_volume`.
    ContainsPersonalVolume { min_personal_volume: f64 },
}

/// Where to search for qualifying distributors in the tree.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SearchMode {
    /// Search only the first N levels below the distributor.
    ///
    /// Rewards personal recruiting and close mentorship. Requires
    /// `search_depth` to specify how many levels to search.
    FirstLevels,

    /// Search all levels below the distributor with no depth limit.
    ///
    /// Rewards deep team building. Qualification is easier to achieve
    /// passively through spillover or inherited downlines.
    AnyLevel,
}

/// A time window before demotion takes effect.
///
/// Protects distributors from immediate demotion after a bad period.
/// The distributor has `count` units of `unit` to regain qualification
/// before dropping to a lower rank.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GracePeriod {
    /// How many time units the grace period lasts.
    pub count: u16,

    /// The time unit for the grace period.
    pub unit: GraceUnit,
}

/// Time unit for grace period measurement.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum GraceUnit {
    /// Grace period measured in days.
    Days,

    /// Grace period measured in weeks.
    Weeks,

    /// Grace period measured in months.
    Months,
}

/// How rank demotion is handled when a distributor fails to maintain
/// qualification.
///
/// Grace and promotion-only are mutually exclusive. The enum encodes
/// this constraint at the type level. Grace gives time to recover.
/// Promotion-only means ranks never decrease.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum DemotionPolicy {
    /// A grace period before demotion takes effect.
    ///
    /// If a distributor misses qualification, they have the specified
    /// time to recover before dropping to a lower rank.
    Grace(GracePeriod),

    /// Ranks can only go up, never down.
    ///
    /// A distributor who achieves a rank keeps it forever, regardless
    /// of future performance. Common in plans where rank is more about
    /// recognition than commission gating.
    PromotionOnly,
}

/// Configuration for tracking the highest rank ever achieved.
///
/// When enabled, the system tracks two rank values per distributor:
/// the current qualified rank (can go up or down) and the achieved
/// rank (highest ever, never decreases). Required for rank advancement
/// bonuses with `pay_once_only`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RankTrackingConfig {
    /// Whether to track the highest rank ever achieved separately
    /// from the current qualified rank.
    ///
    /// The achieved rank is used for recognition, titles, leaderboards,
    /// and rank advancement bonuses. The qualified rank drives commission
    /// calculations.
    pub track_achieved_rank: bool,
}

/// Admin features for constraining or overriding distributor rank.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RankFeaturesConfig {
    /// Whether admins can set rank constraints (whitelists) on
    /// individual distributors.
    ///
    /// Rank constraints filter the qualified rank through a whitelist
    /// of allowed ranks. Used for rank ceilings ("cannot advance past
    /// Silver") and rank floors ("cannot drop below Gold").
    pub constraints_enabled: bool,

    /// Whether admins can force a specific rank on a distributor,
    /// bypassing all qualification criteria.
    ///
    /// Used for contractual guarantees when recruiting leaders from
    /// other companies. Overrides take priority over both normal
    /// qualification and constraints.
    pub overrides_enabled: bool,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_full_rank_definition() {
        let json = r#"{
            "name": "Silver",
            "ordinal": 2,
            "qualification": {
                "structures": [
                    {
                        "structure": "Primary",
                        "personal_volume": 100.0,
                        "group_volume": 5000.0,
                        "max_group_volume_per_leg": 3000.0,
                        "min_retail_volume": 50.0,
                        "distributor_count": {
                            "count": 3,
                            "min_rank": "Bronze",
                            "search_mode": "first_levels",
                            "search_depth": 5,
                            "total_count": 10,
                            "min_leg_group_volume": 2000.0
                        }
                    }
                ],
                "required_products": ["premium-starter-kit", "executive-starter-kit"]
            },
            "qualified_structures": ["Primary", "Binary Bonus"],
            "demotion_policy": {
                "grace": {
                    "count": 2,
                    "unit": "months"
                }
            }
        }"#;
        let rank: RankDefinition = serde_json::from_str(json).unwrap();
        assert_eq!(rank.name, "Silver");
        assert_eq!(rank.ordinal, 2);
        assert_eq!(rank.qualification.structures.len(), 1);

        let sq = &rank.qualification.structures[0];
        assert_eq!(sq.structure, "Primary");
        assert_eq!(sq.personal_volume, 100.0);
        assert_eq!(sq.group_volume, 5000.0);
        assert_eq!(sq.max_group_volume_per_leg, 3000.0);
        assert_eq!(sq.min_retail_volume, 50.0);

        let dc = sq.distributor_count.as_ref().unwrap();
        assert_eq!(dc.count, 3);
        assert_eq!(dc.min_rank, "Bronze");
        assert!(matches!(dc.search_mode, SearchMode::FirstLevels));
        assert_eq!(dc.search_depth, Some(5));
        assert_eq!(dc.total_count, 10);
        assert_eq!(dc.min_leg_group_volume, 2000.0);

        assert_eq!(
            rank.qualification.required_products,
            vec!["premium-starter-kit", "executive-starter-kit"]
        );
        assert_eq!(rank.qualified_structures, vec!["Primary", "Binary Bonus"]);
        assert!(matches!(
            rank.demotion_policy,
            DemotionPolicy::Grace(GracePeriod { count: 2, .. })
        ));
    }

    #[test]
    fn deserialize_demotion_policy_grace() {
        let json = r#"{
            "grace": {
                "count": 3,
                "unit": "weeks"
            }
        }"#;
        let policy: DemotionPolicy = serde_json::from_str(json).unwrap();
        match policy {
            DemotionPolicy::Grace(gp) => {
                assert_eq!(gp.count, 3);
                assert!(matches!(gp.unit, GraceUnit::Weeks));
            }
            _ => panic!("expected Grace variant"),
        }
    }

    #[test]
    fn deserialize_demotion_policy_promotion_only() {
        let json = r#""promotion_only""#;
        let policy: DemotionPolicy = serde_json::from_str(json).unwrap();
        assert!(matches!(policy, DemotionPolicy::PromotionOnly));
    }

    #[test]
    fn deserialize_search_modes() {
        let json_first = r#""first_levels""#;
        let mode: SearchMode = serde_json::from_str(json_first).unwrap();
        assert!(matches!(mode, SearchMode::FirstLevels));

        let json_any = r#""any_level""#;
        let mode: SearchMode = serde_json::from_str(json_any).unwrap();
        assert!(matches!(mode, SearchMode::AnyLevel));
    }

    #[test]
    fn deserialize_rank_tracking_config() {
        let json = r#"{
            "track_achieved_rank": true
        }"#;
        let config: RankTrackingConfig = serde_json::from_str(json).unwrap();
        assert!(config.track_achieved_rank);
    }

    #[test]
    fn deserialize_rank_features_config() {
        let json = r#"{
            "constraints_enabled": true,
            "overrides_enabled": false
        }"#;
        let config: RankFeaturesConfig = serde_json::from_str(json).unwrap();
        assert!(config.constraints_enabled);
        assert!(!config.overrides_enabled);
    }

    #[test]
    fn deserialize_grace_units() {
        for (json, expected) in [
            (r#""days""#, GraceUnit::Days),
            (r#""weeks""#, GraceUnit::Weeks),
            (r#""months""#, GraceUnit::Months),
        ] {
            let unit: GraceUnit = serde_json::from_str(json).unwrap();
            assert_eq!(unit, expected);
        }
    }

    #[test]
    fn leg_predicate_contains_rank_deserializes() {
        let json = r#"{"type": "contains_rank", "min_rank": "Gold"}"#;
        let pred: LegPredicate = serde_json::from_str(json).unwrap();
        match pred {
            LegPredicate::ContainsRank { min_rank } => assert_eq!(min_rank, "Gold"),
            _ => panic!("expected ContainsRank variant"),
        }
    }

    #[test]
    fn leg_predicate_contains_personal_volume_deserializes() {
        let json = r#"{"type": "contains_personal_volume", "min_personal_volume": 200.0}"#;
        let pred: LegPredicate = serde_json::from_str(json).unwrap();
        match pred {
            LegPredicate::ContainsPersonalVolume {
                min_personal_volume,
            } => assert_eq!(min_personal_volume, 200.0),
            _ => panic!("expected ContainsPersonalVolume variant"),
        }
    }

    #[test]
    fn leg_predicate_deserializes_go_payload_with_zero_nonselected_field() {
        // The Go config layer is a flat struct: it emits the non-selected
        // variant field as a zero value. Rust's internally-tagged enum must
        // ignore that extra field. This locks the Go->Rust half of the
        // leg_quality serialization contract.
        let contains_rank =
            r#"{"type": "contains_rank", "min_rank": "Gold", "min_personal_volume": 0.0}"#;
        let pred: LegPredicate = serde_json::from_str(contains_rank).unwrap();
        assert!(matches!(pred, LegPredicate::ContainsRank { .. }));

        let contains_pv =
            r#"{"type": "contains_personal_volume", "min_personal_volume": 200.0, "min_rank": ""}"#;
        let pred: LegPredicate = serde_json::from_str(contains_pv).unwrap();
        assert!(matches!(pred, LegPredicate::ContainsPersonalVolume { .. }));
    }

    #[test]
    fn structure_qualification_deserializes_leg_quality() {
        let json = r#"{
            "structure": "Primary",
            "personal_volume": 100.0,
            "group_volume": 5000.0,
            "max_group_volume_per_leg": 3000.0,
            "min_retail_volume": 50.0,
            "distributor_count": null,
            "leg_quality": [
                {"count": 3, "predicate": {"type": "contains_rank", "min_rank": "Gold"}}
            ]
        }"#;
        let sq: StructureQualification = serde_json::from_str(json).unwrap();
        assert_eq!(sq.leg_quality.len(), 1);
        assert_eq!(sq.leg_quality[0].count, 3);
        assert!(matches!(
            sq.leg_quality[0].predicate,
            LegPredicate::ContainsRank { .. }
        ));
    }

    #[test]
    fn structure_qualification_leg_quality_defaults_to_empty_when_absent() {
        // BR5: an absent `leg_quality` deserializes to an empty Vec via
        // `#[serde(default)]`. This is the "absent ≡ empty" half of BR5;
        // the "empty ≡ pre-feature" half is Task 4.
        let json = r#"{
            "structure": "Primary",
            "personal_volume": 100.0,
            "group_volume": 5000.0,
            "max_group_volume_per_leg": 3000.0,
            "min_retail_volume": 50.0,
            "distributor_count": null
        }"#;
        let sq: StructureQualification = serde_json::from_str(json).unwrap();
        assert!(sq.leg_quality.is_empty());
    }

    #[test]
    fn leg_quality_requirement_round_trips() {
        let json = r#"{"count": 12, "predicate": {"type": "contains_personal_volume", "min_personal_volume": 150.0}}"#;
        let req: LegQualityRequirement = serde_json::from_str(json).unwrap();
        let reserialized = serde_json::to_string(&req).unwrap();
        let restored: LegQualityRequirement = serde_json::from_str(&reserialized).unwrap();
        assert_eq!(restored.count, 12);
        assert!(matches!(
            restored.predicate,
            LegPredicate::ContainsPersonalVolume { .. }
        ));
    }

    #[test]
    fn rank_qualification_deserializes_window() {
        let json = r#"{"structures":[],"required_products":[],
            "window":{"threshold_rank":"Director","qualifying_periods":6,"window_periods":12}}"#;
        let q: RankQualification = serde_json::from_str(json).unwrap();
        let w = q.window.unwrap();
        assert_eq!(w.threshold_rank, "Director");
        assert_eq!(w.qualifying_periods, 6);
        assert_eq!(w.window_periods, 12);
    }

    #[test]
    fn rank_qualification_window_defaults_none_when_absent() {
        let json = r#"{"structures":[],"required_products":[]}"#;
        let q: RankQualification = serde_json::from_str(json).unwrap();
        assert!(q.window.is_none());
    }

    #[test]
    fn rank_qualification_deserializes_tenure() {
        let json = r#"{"structures":[],"required_products":[],
            "tenure":{"threshold_rank":"Director","periods":12}}"#;
        let q: RankQualification = serde_json::from_str(json).unwrap();
        let t = q.tenure.unwrap();
        assert_eq!(t.threshold_rank, "Director");
        assert_eq!(t.periods, 12u8);
    }

    #[test]
    fn rank_qualification_tenure_defaults_none_when_absent() {
        let json = r#"{"structures":[],"required_products":[]}"#;
        let q: RankQualification = serde_json::from_str(json).unwrap();
        assert!(q.tenure.is_none());
    }

    #[test]
    fn qualified_structures_absent_or_null_deserializes_to_empty() {
        // Go omits an empty qualified_structures via omitempty and may emit
        // null. serde(default) supplies the empty Vec when the field is absent,
        // null_as_empty handles an explicit null, so absent, null, and [] all
        // yield empty.
        for tail in [
            "",
            r#","qualified_structures":null"#,
            r#","qualified_structures":[]"#,
        ] {
            let json = format!(
                r#"{{"name":"R","ordinal":1,"qualification":{{"structures":[]}},"demotion_policy":"promotion_only"{tail}}}"#
            );
            let rank: RankDefinition = serde_json::from_str(&json).unwrap();
            assert!(rank.qualified_structures.is_empty(), "input: {json}");
        }
    }

    #[test]
    fn required_products_absent_or_null_deserializes_to_empty() {
        for tail in [
            "",
            r#","required_products":null"#,
            r#","required_products":[]"#,
        ] {
            let json = format!(r#"{{"structures":[]{tail}}}"#);
            let q: RankQualification = serde_json::from_str(&json).unwrap();
            assert!(q.required_products.is_empty(), "input: {json}");
        }
    }

    #[test]
    fn round_trip_rank_definition() {
        let rank = RankDefinition {
            name: "Gold".to_string(),
            ordinal: 3,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: "Primary".to_string(),
                    personal_volume: 200.0,
                    group_volume: 10000.0,
                    max_group_volume_per_leg: 6000.0,
                    min_retail_volume: 100.0,
                    distributor_count: None,
                    leg_quality: vec![],
                }],
                required_products: vec![],
                window: None,
                tenure: None,
            },
            qualified_structures: vec!["Primary".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        };
        let json = serde_json::to_string(&rank).unwrap();
        let deserialized: RankDefinition = serde_json::from_str(&json).unwrap();
        assert_eq!(deserialized.name, "Gold");
        assert_eq!(deserialized.ordinal, 3);
        assert!(matches!(
            deserialized.demotion_policy,
            DemotionPolicy::PromotionOnly
        ));
    }
}
