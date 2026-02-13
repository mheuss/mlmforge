//! Placement configuration types.
//!
//! Controls how new distributors are placed into tree structures.
//! Includes donated placement restrictions, holding tank behavior,
//! and binary-specific placement preferences.

use serde::{Deserialize, Serialize};

/// Placement configuration.
///
/// Controls how new enrollees are positioned in tree structures. Covers
/// donated placement (sponsor placing someone other than directly under
/// themselves), holding tank (temporary staging before final placement),
/// and binary-specific placement preferences.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PlacementConfig {
    /// Whether sponsors can place new enrollees somewhere other than
    /// directly under themselves.
    ///
    /// None means donated placement is not allowed. The sponsor's
    /// new enrollee always goes directly under the sponsor.
    pub donated_placement: Option<DonatedPlacementRestriction>,

    /// Holding tank configuration.
    ///
    /// None means no holding tank. New enrollees are placed immediately.
    pub holding_tank: Option<HoldingTankConfig>,

    /// Binary-specific placement options.
    ///
    /// None when the plan does not include a binary structure.
    pub binary_placement: Option<BinaryPlacementConfig>,
}

/// Restrictions on where a sponsor can donate-place a new enrollee.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum DonatedPlacementRestriction {
    /// Sponsor can place anywhere in their own downline subtree.
    ///
    /// The most flexible option. Allows strategic placement deep in the
    /// tree to strengthen weak legs or fill gaps.
    OwnDownline,

    /// Sponsor can only place under their direct children.
    ///
    /// Limits placement depth to one level below the sponsor's direct
    /// children. Simpler to administer but less strategic flexibility.
    DirectChildrenOnly,
}

/// Holding tank configuration.
///
/// A holding tank temporarily stages new enrollees before final tree
/// placement. Gives the sponsor time to decide optimal placement.
/// Auto-placement kicks in when the expiration period lapses.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HoldingTankConfig {
    /// Master switch for holding tank.
    ///
    /// When false, the remaining fields are ignored and new enrollees
    /// are placed immediately.
    pub enabled: bool,

    /// Days before auto-placement kicks in. Maximum 7.
    ///
    /// After this many days, the system automatically places the enrollee
    /// using default placement rules. Keeps the tree from having
    /// permanently unplaced distributors.
    pub expiration_days: u8,

    /// Whether the sponsor can change during the holding tank period.
    ///
    /// When true, the enrollee's sponsor can be reassigned before
    /// final placement. Useful for team-building coordination.
    pub allow_sponsor_change: bool,

    /// Which tree structures use this holding tank.
    ///
    /// A plan can have multiple structures (e.g., binary and unilevel).
    /// This field specifies which structures route through the holding
    /// tank. Values reference structure names defined in the plan.
    pub applicable_structures: Vec<String>,
}

/// Binary-specific placement configuration.
///
/// Controls how new enrollees are placed into the left or right leg
/// of a binary tree. Includes system defaults and user override options.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BinaryPlacementConfig {
    /// System default when no user preference is set.
    ///
    /// Applied when a distributor has not specified their own placement
    /// preference and the system needs to auto-place a new enrollee.
    pub default_preference: BinaryPlacementPreference,

    /// Whether individual distributors can override the default.
    ///
    /// When true, distributors can set their own placement preference
    /// (balanced, left, or right) to control where new enrollees land.
    pub allow_user_preference: bool,

    /// Whether upline placements can spill into this distributor's tree.
    ///
    /// Should almost always be true. Spillover is a key feature of binary
    /// plans. When an upline's preferred leg is full, the new enrollee
    /// spills down into the next available position.
    pub spillover_enabled: bool,
}

/// Binary tree placement preference.
///
/// Determines which leg receives new enrollees when auto-placement
/// or user preference is applied.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum BinaryPlacementPreference {
    /// Place on the weaker leg. Maintains balance.
    ///
    /// The system compares leg volumes and places the new enrollee on
    /// the side with less volume. Best for distributors who want even
    /// growth on both sides.
    Balanced,

    /// Always place on the left leg.
    Left,

    /// Always place on the right leg.
    Right,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_placement_config() {
        let json = r#"{
            "donated_placement": "own_downline",
            "holding_tank": {
                "enabled": true,
                "expiration_days": 5,
                "allow_sponsor_change": false,
                "applicable_structures": ["binary", "unilevel"]
            },
            "binary_placement": {
                "default_preference": "balanced",
                "allow_user_preference": true,
                "spillover_enabled": true
            }
        }"#;
        let config: PlacementConfig = serde_json::from_str(json).unwrap();
        assert!(matches!(
            config.donated_placement,
            Some(DonatedPlacementRestriction::OwnDownline)
        ));
        let tank = config.holding_tank.unwrap();
        assert!(tank.enabled);
        assert_eq!(tank.expiration_days, 5);
        assert!(!tank.allow_sponsor_change);
        assert_eq!(tank.applicable_structures.len(), 2);
        let binary = config.binary_placement.unwrap();
        assert!(matches!(
            binary.default_preference,
            BinaryPlacementPreference::Balanced
        ));
        assert!(binary.allow_user_preference);
        assert!(binary.spillover_enabled);
    }

    #[test]
    fn deserialize_placement_config_all_none() {
        let json = r#"{
            "donated_placement": null,
            "holding_tank": null,
            "binary_placement": null
        }"#;
        let config: PlacementConfig = serde_json::from_str(json).unwrap();
        assert!(config.donated_placement.is_none());
        assert!(config.holding_tank.is_none());
        assert!(config.binary_placement.is_none());
    }

    #[test]
    fn deserialize_holding_tank_config() {
        let json = r#"{
            "enabled": true,
            "expiration_days": 7,
            "allow_sponsor_change": true,
            "applicable_structures": ["binary"]
        }"#;
        let config: HoldingTankConfig = serde_json::from_str(json).unwrap();
        assert!(config.enabled);
        assert_eq!(config.expiration_days, 7);
        assert!(config.allow_sponsor_change);
        assert_eq!(config.applicable_structures, vec!["binary".to_string()]);
    }

    #[test]
    fn deserialize_binary_placement_config() {
        let json = r#"{
            "default_preference": "left",
            "allow_user_preference": false,
            "spillover_enabled": true
        }"#;
        let config: BinaryPlacementConfig = serde_json::from_str(json).unwrap();
        assert!(matches!(
            config.default_preference,
            BinaryPlacementPreference::Left
        ));
        assert!(!config.allow_user_preference);
        assert!(config.spillover_enabled);
    }

    #[test]
    fn deserialize_placement_preferences() {
        let json_balanced = r#""balanced""#;
        let pref: BinaryPlacementPreference = serde_json::from_str(json_balanced).unwrap();
        assert!(matches!(pref, BinaryPlacementPreference::Balanced));

        let json_left = r#""left""#;
        let pref: BinaryPlacementPreference = serde_json::from_str(json_left).unwrap();
        assert!(matches!(pref, BinaryPlacementPreference::Left));

        let json_right = r#""right""#;
        let pref: BinaryPlacementPreference = serde_json::from_str(json_right).unwrap();
        assert!(matches!(pref, BinaryPlacementPreference::Right));
    }

    #[test]
    fn deserialize_donated_placement_restrictions() {
        let json_own_downline = r#""own_downline""#;
        let restriction: DonatedPlacementRestriction =
            serde_json::from_str(json_own_downline).unwrap();
        assert!(matches!(
            restriction,
            DonatedPlacementRestriction::OwnDownline
        ));

        let json_direct_children = r#""direct_children_only""#;
        let restriction: DonatedPlacementRestriction =
            serde_json::from_str(json_direct_children).unwrap();
        assert!(matches!(
            restriction,
            DonatedPlacementRestriction::DirectChildrenOnly
        ));
    }

    #[test]
    fn round_trip_placement_config() {
        let config = PlacementConfig {
            donated_placement: Some(DonatedPlacementRestriction::DirectChildrenOnly),
            holding_tank: Some(HoldingTankConfig {
                enabled: true,
                expiration_days: 3,
                allow_sponsor_change: false,
                applicable_structures: vec!["binary".to_string()],
            }),
            binary_placement: Some(BinaryPlacementConfig {
                default_preference: BinaryPlacementPreference::Right,
                allow_user_preference: true,
                spillover_enabled: true,
            }),
        };
        let json = serde_json::to_string(&config).unwrap();
        let deserialized: PlacementConfig = serde_json::from_str(&json).unwrap();
        assert!(matches!(
            deserialized.donated_placement,
            Some(DonatedPlacementRestriction::DirectChildrenOnly)
        ));
        let tank = deserialized.holding_tank.unwrap();
        assert_eq!(tank.expiration_days, 3);
        let binary = deserialized.binary_placement.unwrap();
        assert!(matches!(
            binary.default_preference,
            BinaryPlacementPreference::Right
        ));
        assert!(binary.allow_user_preference);
    }
}
