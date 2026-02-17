//! Payout configuration and commission cap types.
//!
//! Controls how commissions are disbursed and capped. PayoutConfig defines
//! minimum thresholds, available payment methods, and currency settings.
//! CapsConfig protects the company and distributors through per-distributor
//! and company-wide payout limits.

use serde::{Deserialize, Serialize};

/// Payout configuration.
///
/// Controls the mechanics of commission disbursement. Defines the currency,
/// minimum payout threshold, partial payout rules, and available payment
/// methods. The currency should match `VolumeConfig.base_currency`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PayoutConfig {
    /// ISO 4217 currency code for all payouts.
    ///
    /// Should match `VolumeConfig.base_currency`. All commission amounts
    /// are denominated in this currency.
    #[serde(rename = "base_currency")]
    pub currency: String,

    /// Minimum commission amount before payout is issued.
    ///
    /// Commissions below this threshold roll over to the next period.
    /// Prevents micro-payouts that cost more to process than they're worth.
    #[serde(rename = "minimum_amount")]
    pub minimum_payout: f64,

    /// When true, distributors can request partial payouts above the minimum.
    ///
    /// Allows distributors to withdraw a portion of their earned commissions
    /// while leaving the rest in their account.
    #[serde(rename = "split_payouts_enabled")]
    pub allow_partial_payout: bool,

    /// Available payment methods for commission disbursement.
    #[serde(rename = "methods")]
    pub payment_methods: Vec<PayoutMethod>,
}

/// A payout method available for commission disbursement.
///
/// Each method has an associated processing fee. The fee can be a flat
/// amount or a percentage, depending on company policy.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PayoutMethod {
    /// Payment method identifier.
    ///
    /// Common values: "bank_transfer", "check", "ewallet".
    /// Serialized as "type" in JSON because `type` is a Rust keyword.
    #[serde(rename = "type")]
    pub method_type: String,

    /// Processing fee as a flat amount or percentage.
    ///
    /// Interpretation (flat vs. percentage) is company-defined.
    /// Applied when this payment method is selected for disbursement.
    pub fee: f64,
}

/// Commission cap configuration.
///
/// The financial safety net. Caps prevent the company from paying out
/// more than it can sustain. Per-distributor caps limit individual earnings.
/// The company-wide cap limits total payout as a percentage of revenue.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CapsConfig {
    /// Maximum commission per distributor per period.
    ///
    /// None means uncapped. When set, no distributor can earn more than
    /// this amount in a single commission period.
    #[serde(rename = "per_distributor_per_period")]
    pub per_distributor_cap: Option<f64>,

    /// Maximum percentage of company volume paid as commissions.
    ///
    /// Industry standard is 40-45%. If total commissions exceed this
    /// percentage of total company volume, the enforcement strategy
    /// determines how to reduce payouts.
    pub company_payout_cap_percent: f64,

    /// How caps are enforced when exceeded.
    #[serde(rename = "cap_enforcement")]
    pub enforcement: CapEnforcement,

    /// When true, overpayments from prior periods can be deducted
    /// from future commissions.
    ///
    /// Provides a correction mechanism when commissions are paid
    /// before all data is finalized. Requires careful legal review.
    #[serde(rename = "clawback_on_refund")]
    pub enable_clawback: bool,
}

/// How commission caps are enforced when the company payout cap is exceeded.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CapEnforcement {
    /// Reduce everyone's commissions proportionally.
    ///
    /// Fair across the board but impacts top earners the most in absolute
    /// dollar terms. The simplest enforcement strategy.
    ProRata,

    /// Reduce lower-rank commissions first, protecting higher ranks.
    ///
    /// Preserves leadership income at the expense of newer distributors.
    /// Common in plans that heavily incentivize rank advancement.
    PriorityReduction,

    /// Stop paying once the cap is reached.
    ///
    /// First-processed, first-paid. Distributors processed later in the
    /// commission run may receive nothing. Simple but potentially unfair.
    HardStop,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_payout_config() {
        let json = r#"{
            "base_currency": "USD",
            "minimum_amount": 50.0,
            "split_payouts_enabled": true,
            "methods": [
                { "type": "bank_transfer", "fee": 2.50 },
                { "type": "check", "fee": 5.00 },
                { "type": "ewallet", "fee": 0.00 }
            ]
        }"#;
        let config: PayoutConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.currency, "USD");
        assert_eq!(config.minimum_payout, 50.0);
        assert!(config.allow_partial_payout);
        assert_eq!(config.payment_methods.len(), 3);
        assert_eq!(config.payment_methods[0].method_type, "bank_transfer");
        assert_eq!(config.payment_methods[0].fee, 2.50);
        assert_eq!(config.payment_methods[1].method_type, "check");
        assert_eq!(config.payment_methods[1].fee, 5.00);
        assert_eq!(config.payment_methods[2].method_type, "ewallet");
        assert_eq!(config.payment_methods[2].fee, 0.00);
    }

    #[test]
    fn deserialize_payout_method() {
        let json = r#"{
            "type": "bank_transfer",
            "fee": 2.50
        }"#;
        let method: PayoutMethod = serde_json::from_str(json).unwrap();
        assert_eq!(method.method_type, "bank_transfer");
        assert_eq!(method.fee, 2.50);
    }

    #[test]
    fn deserialize_caps_config() {
        let json = r#"{
            "per_distributor_per_period": 10000.0,
            "company_payout_cap_percent": 0.42,
            "cap_enforcement": "pro_rata",
            "clawback_on_refund": false
        }"#;
        let config: CapsConfig = serde_json::from_str(json).unwrap();
        assert_eq!(config.per_distributor_cap, Some(10000.0));
        assert_eq!(config.company_payout_cap_percent, 0.42);
        assert!(matches!(config.enforcement, CapEnforcement::ProRata));
        assert!(!config.enable_clawback);
    }

    #[test]
    fn deserialize_caps_config_uncapped() {
        let json = r#"{
            "per_distributor_per_period": null,
            "company_payout_cap_percent": 0.45,
            "cap_enforcement": "hard_stop",
            "clawback_on_refund": true
        }"#;
        let config: CapsConfig = serde_json::from_str(json).unwrap();
        assert!(config.per_distributor_cap.is_none());
        assert_eq!(config.company_payout_cap_percent, 0.45);
        assert!(matches!(config.enforcement, CapEnforcement::HardStop));
        assert!(config.enable_clawback);
    }

    #[test]
    fn deserialize_cap_enforcement_variants() {
        let json_pro_rata = r#""pro_rata""#;
        let enforcement: CapEnforcement = serde_json::from_str(json_pro_rata).unwrap();
        assert!(matches!(enforcement, CapEnforcement::ProRata));

        let json_priority = r#""priority_reduction""#;
        let enforcement: CapEnforcement = serde_json::from_str(json_priority).unwrap();
        assert!(matches!(enforcement, CapEnforcement::PriorityReduction));

        let json_hard_stop = r#""hard_stop""#;
        let enforcement: CapEnforcement = serde_json::from_str(json_hard_stop).unwrap();
        assert!(matches!(enforcement, CapEnforcement::HardStop));
    }

    #[test]
    fn round_trip_payout_config() {
        let config = PayoutConfig {
            currency: "EUR".to_string(),
            minimum_payout: 25.0,
            allow_partial_payout: false,
            payment_methods: vec![
                PayoutMethod {
                    method_type: "bank_transfer".to_string(),
                    fee: 1.50,
                },
                PayoutMethod {
                    method_type: "ewallet".to_string(),
                    fee: 0.0,
                },
            ],
        };
        let json = serde_json::to_string(&config).unwrap();
        let deserialized: PayoutConfig = serde_json::from_str(&json).unwrap();
        assert_eq!(deserialized.currency, "EUR");
        assert_eq!(deserialized.minimum_payout, 25.0);
        assert!(!deserialized.allow_partial_payout);
        assert_eq!(deserialized.payment_methods.len(), 2);
        assert_eq!(deserialized.payment_methods[0].method_type, "bank_transfer");
        assert_eq!(deserialized.payment_methods[1].fee, 0.0);
    }

    #[test]
    fn round_trip_caps_config() {
        let config = CapsConfig {
            per_distributor_cap: Some(5000.0),
            company_payout_cap_percent: 0.40,
            enforcement: CapEnforcement::PriorityReduction,
            enable_clawback: true,
        };
        let json = serde_json::to_string(&config).unwrap();
        let deserialized: CapsConfig = serde_json::from_str(&json).unwrap();
        assert_eq!(deserialized.per_distributor_cap, Some(5000.0));
        assert_eq!(deserialized.company_payout_cap_percent, 0.40);
        assert!(matches!(
            deserialized.enforcement,
            CapEnforcement::PriorityReduction
        ));
        assert!(deserialized.enable_clawback);
    }

    #[test]
    fn payout_method_serializes_type_as_json_key() {
        let method = PayoutMethod {
            method_type: "check".to_string(),
            fee: 3.00,
        };
        let json = serde_json::to_string(&method).unwrap();
        assert!(json.contains(r#""type":"check""#));
        assert!(!json.contains("method_type"));
    }
}
