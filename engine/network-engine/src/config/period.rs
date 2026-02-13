//! Commission period configuration types.

use chrono::NaiveDate;
use serde::{Deserialize, Serialize};

/// Timing configuration for commission pay periods.
///
/// Defines when commission cycles start, how long they last, and how
/// many days after a period closes before commissions become payable.
/// Once the first commission run executes, `length` and `start_date`
/// are immutable. Only `payout_lag_days` can change after that point.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PeriodConfig {
    /// How often commissions are calculated and paid.
    pub length: PeriodLength,

    /// Anchor date for all period calculations.
    ///
    /// Every future period is derived from this date. Must be in the
    /// future at creation time. Immutable after the first commission run.
    pub start_date: NaiveDate,

    /// Days after a period closes before commissions are payable.
    ///
    /// Provides a buffer for review, disputes, and payment processing.
    /// Industry standard is 7-14 days. Values above 30 may draw
    /// regulatory attention. Maximum 60.
    pub payout_lag_days: u8,
}

/// The length of a commission pay period.
///
/// Determines how frequently commission calculations run and
/// distributors receive payouts.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PeriodLength {
    /// Every 7 days from the start date. Fast payouts, more admin overhead.
    /// Common in binary plans and newer companies.
    Week,

    /// 1st through 15th, then 16th through end of month. Two predictable
    /// cycles per month. Balances speed with manageable processing.
    SemiMonth,

    /// Calendar month. The industry standard. Simplest to explain.
    Month,

    /// Every 3 calendar months. Long cycles, rare for primary commissions.
    /// Sometimes used for pool bonuses or leadership bonuses alongside
    /// a shorter primary period.
    Quarter,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deserialize_period_config() {
        let json = r#"{
            "length": "month",
            "start_date": "2026-03-01",
            "payout_lag_days": 14
        }"#;
        let config: PeriodConfig = serde_json::from_str(json).unwrap();
        assert!(matches!(config.length, PeriodLength::Month));
        assert_eq!(
            config.start_date,
            NaiveDate::from_ymd_opt(2026, 3, 1).unwrap()
        );
        assert_eq!(config.payout_lag_days, 14);
    }

    #[test]
    fn deserialize_semi_month_period() {
        let json = r#""semi_month""#;
        let length: PeriodLength = serde_json::from_str(json).unwrap();
        assert!(matches!(length, PeriodLength::SemiMonth));
    }

    #[test]
    fn deserialize_all_period_lengths() {
        for (json, expected) in [
            (r#""week""#, PeriodLength::Week),
            (r#""semi_month""#, PeriodLength::SemiMonth),
            (r#""month""#, PeriodLength::Month),
            (r#""quarter""#, PeriodLength::Quarter),
        ] {
            let length: PeriodLength = serde_json::from_str(json).unwrap();
            assert_eq!(length, expected);
        }
    }

    #[test]
    fn round_trip_period_config() {
        let config = PeriodConfig {
            length: PeriodLength::SemiMonth,
            start_date: NaiveDate::from_ymd_opt(2026, 6, 1).unwrap(),
            payout_lag_days: 7,
        };
        let json = serde_json::to_string(&config).unwrap();
        let deserialized: PeriodConfig = serde_json::from_str(&json).unwrap();
        assert!(matches!(deserialized.length, PeriodLength::SemiMonth));
        assert_eq!(deserialized.payout_lag_days, 7);
    }
}
