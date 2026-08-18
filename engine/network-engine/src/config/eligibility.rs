//! Commission eligibility types.

use serde::{Deserialize, Serialize};

use crate::serde_helpers::null_as_empty;

/// Determines who receives commissions regardless of rank.
///
/// A distributor can hold Gold rank but earn nothing if they don't meet
/// eligibility. Eligibility is evaluated every period before commissions
/// are calculated. Rank determines commission rates and depth. Eligibility
/// determines whether the distributor receives anything at all.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CommissionEligibility {
    /// Minimum personal volume required to receive any commissions.
    ///
    /// Most plans set 100-150 PV. Zero disables the requirement.
    #[serde(rename = "min_personal_volume")]
    pub minimum_pv: f64,

    /// When true, the distributor must have at least one order in the
    /// current period.
    ///
    /// Stricter than `minimum_pv` alone. Any order counts, even if
    /// below the PV threshold. Used to enforce active participation.
    pub require_order_in_period: bool,

    /// Distributor statuses that can receive commissions.
    ///
    /// Typical values: "active", "grace". An empty list means all
    /// statuses are eligible. Status values reference the distributor
    /// lifecycle states defined by the application layer.
    pub eligible_statuses: Vec<String>,

    /// Tiered depth unlocking based on active frontline legs.
    ///
    /// More active frontline legs unlock deeper commission earnings.
    /// Tiers must be sorted by `min_active_legs` ascending. An empty
    /// list means no leg-based depth restrictions apply.
    #[serde(default, deserialize_with = "null_as_empty")]
    pub active_leg_tiers: Vec<ActiveLegTier>,
}

/// A single tier in the active leg depth-unlocking system.
///
/// More active frontline legs unlock deeper commission earnings.
/// Each tier defines a threshold of active legs and the maximum
/// commission depth it unlocks. Tiers are evaluated in order.
/// The highest tier the distributor qualifies for determines their
/// maximum earning depth.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ActiveLegTier {
    /// Number of personally sponsored frontline distributors who are
    /// themselves commission-eligible.
    ///
    /// "Active" means the frontline distributor independently meets
    /// the eligibility criteria in this same configuration.
    pub min_active_legs: u16,

    /// How many levels deep this distributor can earn commissions.
    ///
    /// Zero means unlimited depth. The engine stops paying commissions
    /// beyond this level regardless of the distributor's rank.
    pub max_commission_depth: u16,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_commission_eligibility() {
        let json = r#"{
            "min_personal_volume": 100.0,
            "require_order_in_period": true,
            "eligible_statuses": ["active", "grace"],
            "active_leg_tiers": [
                {
                    "min_active_legs": 2,
                    "max_commission_depth": 3
                },
                {
                    "min_active_legs": 5,
                    "max_commission_depth": 7
                },
                {
                    "min_active_legs": 10,
                    "max_commission_depth": 0
                }
            ]
        }"#;
        let elig: CommissionEligibility = serde_json::from_str(json).unwrap();
        assert_eq!(elig.minimum_pv, 100.0);
        assert!(elig.require_order_in_period);
        assert_eq!(elig.eligible_statuses, vec!["active", "grace"]);
        assert_eq!(elig.active_leg_tiers.len(), 3);

        assert_eq!(elig.active_leg_tiers[0].min_active_legs, 2);
        assert_eq!(elig.active_leg_tiers[0].max_commission_depth, 3);

        assert_eq!(elig.active_leg_tiers[1].min_active_legs, 5);
        assert_eq!(elig.active_leg_tiers[1].max_commission_depth, 7);

        assert_eq!(elig.active_leg_tiers[2].min_active_legs, 10);
        assert_eq!(elig.active_leg_tiers[2].max_commission_depth, 0);
    }

    #[test]
    fn deserialize_active_leg_tier() {
        let json = r#"{
            "min_active_legs": 3,
            "max_commission_depth": 5
        }"#;
        let tier: ActiveLegTier = serde_json::from_str(json).unwrap();
        assert_eq!(tier.min_active_legs, 3);
        assert_eq!(tier.max_commission_depth, 5);
    }

    #[test]
    fn deserialize_eligibility_minimal() {
        let json = r#"{
            "min_personal_volume": 0.0,
            "require_order_in_period": false,
            "eligible_statuses": [],
            "active_leg_tiers": []
        }"#;
        let elig: CommissionEligibility = serde_json::from_str(json).unwrap();
        assert_eq!(elig.minimum_pv, 0.0);
        assert!(!elig.require_order_in_period);
        assert!(elig.eligible_statuses.is_empty());
        assert!(elig.active_leg_tiers.is_empty());
    }

    #[test]
    fn active_leg_tiers_absent_or_null_deserializes_to_empty() {
        // Go omits an empty active_leg_tiers via omitempty, but a nil slice can
        // also reach serde as JSON null; null_as_empty accepts absent,
        // null, and [] identically (the [] case is covered above).
        let base = r#"{"min_personal_volume":50.0,"require_order_in_period":true,"eligible_statuses":["active"]"#;
        for tail in ["}", r#","active_leg_tiers":null}"#] {
            let json = format!("{base}{tail}");
            let elig: CommissionEligibility = serde_json::from_str(&json).unwrap();
            assert!(elig.active_leg_tiers.is_empty(), "input: {json}");
        }
    }

    #[test]
    fn round_trip_commission_eligibility() {
        let elig = CommissionEligibility {
            minimum_pv: 150.0,
            require_order_in_period: true,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![
                ActiveLegTier {
                    min_active_legs: 3,
                    max_commission_depth: 5,
                },
                ActiveLegTier {
                    min_active_legs: 8,
                    max_commission_depth: 0,
                },
            ],
        };
        let json = serde_json::to_string(&elig).unwrap();
        let deserialized: CommissionEligibility = serde_json::from_str(&json).unwrap();
        assert_eq!(deserialized.minimum_pv, 150.0);
        assert!(deserialized.require_order_in_period);
        assert_eq!(deserialized.eligible_statuses, vec!["active"]);
        assert_eq!(deserialized.active_leg_tiers.len(), 2);
        assert_eq!(deserialized.active_leg_tiers[1].max_commission_depth, 0);
    }
}
