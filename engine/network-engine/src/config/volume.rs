//! Volume configuration types.

use serde::{Deserialize, Serialize};

/// Plan-level volume configuration.
///
/// Controls how purchase volume enters the commission system.
/// Volume is measured in Commission Volume (CV) points, not currency.
/// A $100 product might generate 80 CV. The CV value is set per product
/// in the product catalog, not here.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct VolumeConfig {
    /// When true, enrollment purchases generate no commission volume.
    ///
    /// The distributor still enrolls and gets placed on the tree, but
    /// no commissions are triggered from the signup purchase. Used when
    /// the enrollment fee is administrative, not product-based.
    pub inhibit_signup_volume: bool,

    /// ISO 4217 currency code for all monetary calculations.
    pub base_currency: String,

    /// Converts CV points to the base currency amount.
    ///
    /// Applied in the commission formula: `CV * multiplier * rate`.
    /// Most plans set this to 1.0. Must be greater than 0.
    /// Can be overridden per structure.
    pub volume_to_dollar_multiplier: f64,

    /// When true, volume used for rank qualification is subtracted
    /// from commissionable volume. Prevents double-counting.
    pub deduct_qualifying_volume: bool,
}

/// What triggered the volume event.
///
/// Not yet referenced by production code. Used in tests to validate
/// deserialization. Will be referenced by volume event types when the
/// commission engine processes real orders.
#[cfg(test)]
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum VolumeType {
    /// One-time volume from a new distributor's enrollment purchase.
    Signup,

    /// Recurring volume from an autoship (subscription) charge.
    Recurring,

    /// Ad-hoc volume from a one-time product purchase.
    Store,
}

/// Who made the purchase.
///
/// Determined by whether a customer ID is present on the order.
/// Affects rank qualification (plans can require minimum retail volume)
/// and regulatory compliance (retail-to-personal ratio).
///
/// Not yet referenced by production code. Used in tests to validate
/// deserialization. Will be referenced by volume event types when the
/// commission engine processes real orders.
#[cfg(test)]
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PurchaserType {
    /// The distributor bought the product themselves.
    Personal,

    /// A retail customer bought through the distributor's store.
    Retail,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_volume_config() {
        let json = r#"{
            "inhibit_signup_volume": false,
            "base_currency": "USD",
            "volume_to_dollar_multiplier": 1.0,
            "deduct_qualifying_volume": false
        }"#;
        let config: VolumeConfig = serde_json::from_str(json).unwrap();
        assert!(!config.inhibit_signup_volume);
        assert_eq!(config.base_currency, "USD");
    }

    #[test]
    fn deserialize_volume_type() {
        let json = r#""recurring""#;
        let vt: VolumeType = serde_json::from_str(json).unwrap();
        assert!(matches!(vt, VolumeType::Recurring));
    }

    #[test]
    fn deserialize_purchaser_type() {
        let json = r#""retail""#;
        let pt: PurchaserType = serde_json::from_str(json).unwrap();
        assert!(matches!(pt, PurchaserType::Retail));
    }
}
