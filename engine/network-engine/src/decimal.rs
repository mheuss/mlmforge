//! Exact decimal values for volume, money and rates.

use std::fmt;
use std::str::FromStr;

use bnum::types::{I512, U512};
use rust_decimal::Decimal;
use rust_decimal_macros::dec;
use thiserror::Error;

const MAX_SCALE: u32 = 28;
const ENVELOPE_MAX_SCALE: u32 = 10;
const ENVELOPE_LIMIT: Decimal = dec!(1_000_000_000_000_000);

/// An engine-derived decimal: sums, products, outputs and carried state.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Dec(Decimal);

/// A decimal authored by a plan or a caller, held to the authored envelope.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct AuthoredDec(Decimal);

/// Why a number's text was refused.
#[derive(Clone, Debug, PartialEq, Eq, Error)]
#[error("{text:?} is not a decimal the engine can hold exactly: {reason}")]
pub struct ParseDecError {
    text: String,
    reason: &'static str,
}

/// The arithmetic operation that failed.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NumericOp {
    Add,
    Sub,
    Mul,
    DivRatio,
    MulDivTrunc { divisor: Dec, scale: u32 },
    FloorDiv,
}

/// What went wrong in an arithmetic operation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NumericErrorKind {
    Overflow,
    Precision,
    DivisionByZero,
}

/// An arithmetic operation that has no exact result the engine can hold.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Error)]
#[error("cannot {op} {lhs} and {rhs}: {kind}")]
pub struct NumericError {
    pub op: NumericOp,
    pub lhs: Dec,
    pub rhs: Dec,
    pub kind: NumericErrorKind,
}

impl fmt::Display for NumericOp {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            NumericOp::Add => f.write_str("add"),
            NumericOp::Sub => f.write_str("subtract"),
            NumericOp::Mul => f.write_str("multiply"),
            NumericOp::DivRatio => f.write_str("divide"),
            NumericOp::MulDivTrunc { divisor, scale } => {
                write!(f, "divide by {divisor} at scale {scale} the product of")
            }
            NumericOp::FloorDiv => f.write_str("floor-divide"),
        }
    }
}

impl fmt::Display for NumericErrorKind {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            NumericErrorKind::Overflow => "the result is beyond the decimal range",
            NumericErrorKind::Precision => {
                "the exact result needs more than 28 decimal places or 96 bits of digits"
            }
            NumericErrorKind::DivisionByZero => "the divisor is zero",
        })
    }
}

fn pow10(n: u32) -> I512 {
    I512::TEN.pow(n)
}

fn fits_96_bits(m: I512) -> bool {
    m.unsigned_abs() < (U512::ONE << 96u32)
}

/// Hold the exact integer `m` at scale `s` as a `Decimal`, or say why it cannot be held.
fn fit(mut m: I512, mut s: u32) -> Result<Decimal, NumericErrorKind> {
    while s > 0 && (s > MAX_SCALE || !fits_96_bits(m)) && (m % I512::TEN).is_zero() {
        m /= I512::TEN;
        s -= 1;
    }
    if s <= MAX_SCALE && fits_96_bits(m) {
        let m = i128::try_from(m).expect("a 96-bit mantissa fits i128");
        return Ok(Decimal::from_i128_with_scale(m, s));
    }
    // When 10^s is beyond I512, every I512 value is below it, so the integer part is zero.
    if I512::TEN.checked_pow(s).is_none_or(|p| fits_96_bits(m / p)) {
        Err(NumericErrorKind::Precision)
    } else {
        Err(NumericErrorKind::Overflow)
    }
}

const RANGE: &str = "the value is beyond the decimal range";
const PRECISION: &str = "the value needs more than 28 decimal places or 96 bits of digits";

/// Parse JSON number text into the exact `Decimal` it names, and the scale it was written with.
fn parse_exact(text: &str) -> Result<(Decimal, i64), ParseDecError> {
    let refuse = |reason| ParseDecError {
        text: text.to_string(),
        reason,
    };
    let (body, negative) = match text.strip_prefix('-') {
        Some(rest) => (rest, true),
        None => (text, false),
    };
    let (mantissa, exponent) = match body.find(['e', 'E']) {
        Some(at) => (&body[..at], Some(&body[at + 1..])),
        None => (body, None),
    };
    let (whole, fraction) = match mantissa.split_once('.') {
        Some((whole, fraction)) => (whole, Some(fraction)),
        None => (mantissa, None),
    };
    let all_digits = |s: &str| !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit());
    let exponent_digits = exponent.map(|e| e.strip_prefix(['+', '-']).unwrap_or(e));
    if !all_digits(whole)
        || (whole.len() > 1 && whole.starts_with('0'))
        || fraction.is_some_and(|f| !all_digits(f))
        || exponent_digits.is_some_and(|e| !all_digits(e))
    {
        return Err(refuse("not a JSON number"));
    }
    let fraction = fraction.unwrap_or("");
    let exponent_is_negative = exponent.is_some_and(|e| e.starts_with('-'));
    let parsed_exponent: Option<i64> = match exponent {
        Some(e) => e.parse().ok(),
        None => Some(0),
    };
    let written_scale = match parsed_exponent {
        Some(e) => (fraction.len() as i64).saturating_sub(e).max(0),
        None if exponent_is_negative => i64::MAX,
        None => 0,
    };
    let mut digits = format!("{whole}{fraction}")
        .trim_start_matches('0')
        .to_string();
    if digits.is_empty() {
        return Ok((Decimal::ZERO, written_scale));
    }
    let exponent = parsed_exponent.ok_or_else(|| {
        refuse(if exponent_is_negative {
            PRECISION
        } else {
            RANGE
        })
    })?;
    let mut scale = (fraction.len() as i64)
        .checked_sub(exponent)
        .ok_or_else(|| refuse(PRECISION))?;
    if scale < -60 {
        return Err(refuse(RANGE));
    }
    if scale < 0 {
        if digits.len() as i64 - scale > 60 {
            return Err(refuse(RANGE));
        }
        digits.push_str(&"0".repeat(scale.unsigned_abs() as usize));
        scale = 0;
    }
    while scale > i64::from(MAX_SCALE) && digits.ends_with('0') {
        digits.pop();
        scale -= 1;
    }
    if scale > i64::from(MAX_SCALE) {
        return Err(refuse(PRECISION));
    }
    if digits.len() > 150 {
        return Err(refuse(RANGE));
    }
    let mut m = I512::parse_str_radix(&digits, 10);
    if negative {
        m = -m;
    }
    let value = fit(m, scale as u32).map_err(|kind| {
        refuse(if kind == NumericErrorKind::Overflow {
            RANGE
        } else {
            PRECISION
        })
    })?;
    Ok((value, written_scale))
}

fn wide(d: Decimal) -> (I512, u32) {
    (I512::from(d.mantissa()), d.scale())
}

fn fast_add(a: Decimal, b: Decimal, negate_b: bool) -> Option<Decimal> {
    let s = a.scale().max(b.scale());
    let ma = a
        .mantissa()
        .checked_mul(10i128.checked_pow(s - a.scale())?)?;
    let mb = b
        .mantissa()
        .checked_mul(10i128.checked_pow(s - b.scale())?)?;
    let mb = if negate_b { mb.checked_neg()? } else { mb };
    Decimal::try_from_i128_with_scale(ma.checked_add(mb)?, s).ok()
}

fn fast_mul(a: Decimal, b: Decimal) -> Option<Decimal> {
    let m = a.mantissa().checked_mul(b.mantissa())?;
    Decimal::try_from_i128_with_scale(m, a.scale() + b.scale()).ok()
}

impl Dec {
    pub const ZERO: Dec = Dec(Decimal::ZERO);
    pub const ONE: Dec = Dec(Decimal::ONE);

    /// Wrap a `Decimal`, for constants built with `rust_decimal_macros::dec!`.
    pub const fn new(value: Decimal) -> Self {
        Dec(value)
    }

    pub fn is_zero(self) -> bool {
        self.0.is_zero()
    }

    pub fn is_negative(self) -> bool {
        self.0 < Decimal::ZERO
    }
}

impl Dec {
    fn error(self, op: NumericOp, rhs: Dec, kind: NumericErrorKind) -> NumericError {
        NumericError {
            op,
            lhs: self,
            rhs,
            kind,
        }
    }

    fn wide_add(self, rhs: Dec, op: NumericOp) -> Result<Dec, NumericError> {
        debug_assert!(matches!(op, NumericOp::Add | NumericOp::Sub));
        let ((ma, sa), (mb, sb)) = (wide(self.0), wide(rhs.0));
        let s = sa.max(sb);
        let (a, b) = (ma * pow10(s - sa), mb * pow10(s - sb));
        let m = if op == NumericOp::Sub { a - b } else { a + b };
        fit(m, s).map(Dec).map_err(|kind| self.error(op, rhs, kind))
    }

    /// The exact sum, or an error when it cannot be held.
    pub fn try_add(self, rhs: impl Into<Dec>) -> Result<Dec, NumericError> {
        let rhs = rhs.into();
        match fast_add(self.0, rhs.0, false) {
            Some(sum) => Ok(Dec(sum)),
            None => self.wide_add(rhs, NumericOp::Add),
        }
    }

    /// The exact difference, or an error when it cannot be held.
    pub fn try_sub(self, rhs: impl Into<Dec>) -> Result<Dec, NumericError> {
        let rhs = rhs.into();
        match fast_add(self.0, rhs.0, true) {
            Some(diff) => Ok(Dec(diff)),
            None => self.wide_add(rhs, NumericOp::Sub),
        }
    }

    /// The exact product, or an error when it cannot be held.
    pub fn try_mul(self, rhs: impl Into<Dec>) -> Result<Dec, NumericError> {
        let rhs = rhs.into();
        if let Some(product) = fast_mul(self.0, rhs.0) {
            return Ok(Dec(product));
        }
        let ((ma, sa), (mb, sb)) = (wide(self.0), wide(rhs.0));
        fit(ma * mb, sa + sb)
            .map(Dec)
            .map_err(|kind| self.error(NumericOp::Mul, rhs, kind))
    }

    /// The exact sum of `values`, or the first error.
    pub fn sum<I: IntoIterator<Item = Dec>>(values: I) -> Result<Dec, NumericError> {
        values.into_iter().try_fold(Dec::ZERO, Dec::try_add)
    }

    /// E1 and E2: rust_decimal 1.43's `checked_div`, the nearest-even value a
    /// 96-bit coefficient holds at scale 28 or below.
    pub fn div_ratio(self, rhs: impl Into<Dec>) -> Result<Dec, NumericError> {
        let rhs = rhs.into();
        if rhs.is_zero() {
            return Err(self.error(NumericOp::DivRatio, rhs, NumericErrorKind::DivisionByZero));
        }
        self.0
            .checked_div(rhs.0)
            .map(Dec)
            .ok_or_else(|| self.error(NumericOp::DivRatio, rhs, NumericErrorKind::Overflow))
    }

    /// E3: `self × b ÷ c`, truncated toward zero at `scale` decimal places.
    pub fn mul_div_trunc(
        self,
        b: impl Into<Dec>,
        c: impl Into<Dec>,
        scale: u32,
    ) -> Result<Dec, NumericError> {
        let (b, c) = (b.into(), c.into());
        let op = NumericOp::MulDivTrunc { divisor: c, scale };
        if c.is_zero() {
            return Err(self.error(op, b, NumericErrorKind::DivisionByZero));
        }
        if scale > MAX_SCALE {
            return Err(self.error(op, b, NumericErrorKind::Precision));
        }
        let ((ma, sa), (mb, sb), (mc, sc)) = (wide(self.0), wide(b.0), wide(c.0));
        let shift = i64::from(sc + scale) - i64::from(sa + sb);
        let (num, den) = if shift >= 0 {
            (ma * mb * pow10(shift as u32), mc)
        } else {
            (ma * mb, mc * pow10(shift.unsigned_abs() as u32))
        };
        fit(num / den, scale)
            .map(Dec)
            .map_err(|kind| self.error(op, b, kind))
    }

    /// E4: `self ÷ rhs`, rounded down to an integer.
    pub fn floor_div(self, rhs: impl Into<Dec>) -> Result<Dec, NumericError> {
        let rhs = rhs.into();
        if rhs.is_zero() {
            return Err(self.error(NumericOp::FloorDiv, rhs, NumericErrorKind::DivisionByZero));
        }
        let ((ma, sa), (mb, sb)) = (wide(self.0), wide(rhs.0));
        let (num, den) = if sb >= sa {
            (ma * pow10(sb - sa), mb)
        } else {
            (ma, mb * pow10(sa - sb))
        };
        let mut q = num / den;
        if !(num % den).is_zero() && (num.is_negative() != den.is_negative()) {
            q -= I512::ONE;
        }
        fit(q, 0)
            .map(Dec)
            .map_err(|kind| self.error(NumericOp::FloorDiv, rhs, kind))
    }
}

impl AuthoredDec {
    pub const ZERO: AuthoredDec = AuthoredDec(Decimal::ZERO);

    /// The value as an engine decimal, for arithmetic.
    pub fn dec(self) -> Dec {
        Dec(self.0)
    }

    pub fn is_zero(self) -> bool {
        self.0.is_zero()
    }

    pub fn is_negative(self) -> bool {
        self.0 < Decimal::ZERO
    }

    fn within_envelope(
        value: Decimal,
        written_scale: i64,
        text: &str,
    ) -> Result<AuthoredDec, ParseDecError> {
        let refuse = |reason| ParseDecError {
            text: text.to_string(),
            reason,
        };
        if written_scale > i64::from(ENVELOPE_MAX_SCALE) {
            return Err(refuse(
                "an authored value may have at most 10 decimal places",
            ));
        }
        if value.abs() >= ENVELOPE_LIMIT {
            return Err(refuse(
                "an authored value must have an absolute value below 10^15",
            ));
        }
        Ok(AuthoredDec(value))
    }
}

impl From<AuthoredDec> for Dec {
    fn from(value: AuthoredDec) -> Dec {
        value.dec()
    }
}

impl fmt::Display for Dec {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Display::fmt(&self.0.normalize(), f)
    }
}

impl fmt::Display for AuthoredDec {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Display::fmt(&self.0.normalize(), f)
    }
}

impl FromStr for Dec {
    type Err = ParseDecError;

    fn from_str(text: &str) -> Result<Dec, ParseDecError> {
        parse_exact(text).map(|(value, _)| Dec(value))
    }
}

impl FromStr for AuthoredDec {
    type Err = ParseDecError;

    fn from_str(text: &str) -> Result<AuthoredDec, ParseDecError> {
        let (value, written_scale) = parse_exact(text)?;
        AuthoredDec::within_envelope(value, written_scale, text)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use bigdecimal::{BigDecimal, Signed};
    use proptest::prelude::*;

    fn d(text: &str) -> Dec {
        text.parse().unwrap()
    }

    fn big(x: Dec) -> BigDecimal {
        x.to_string().parse().unwrap()
    }

    fn exceeds_the_decimal_range(exact: &BigDecimal) -> bool {
        let (digits, scale) = exact.normalized().as_bigint_and_exponent();
        let digits = if scale < 0 {
            digits * bigdecimal::num_bigint::BigInt::from(10u8).pow(scale.unsigned_abs() as u32)
        } else {
            digits
        };
        digits.abs() >= (bigdecimal::num_bigint::BigInt::from(1u8) << 96) || scale > 28
    }

    #[test]
    fn parse_keeps_the_exact_value_of_plain_and_exponent_forms() {
        assert_eq!(d("1e3"), d("1000"));
        assert_eq!(d("1.5E-3"), d("0.0015"));
        assert_eq!(d("2.50e+1"), d("25"));
        assert_eq!(d("0.10000000000000000000000000000000"), d("0.1"));
    }

    #[test]
    fn parse_refuses_text_outside_the_json_number_grammar() {
        for text in [
            "", "-", "1.", ".5", "01", "+1", "1e", "0x10", "NaN", "inf", "1_000", "\"1\"",
        ] {
            assert!(text.parse::<Dec>().is_err(), "{text}");
        }
    }

    #[test]
    fn parse_refuses_a_value_it_cannot_hold_exactly() {
        let too_precise = "0.00000000000000000000000000001"
            .parse::<Dec>()
            .unwrap_err();
        assert!(
            too_precise
                .to_string()
                .contains("more than 28 decimal places")
        );
        let too_large = "123456789012345678901234567890".parse::<Dec>().unwrap_err();
        assert!(too_large.to_string().contains("beyond the decimal range"));
        assert!("1e400".parse::<Dec>().is_err());
        assert!("1e-400".parse::<Dec>().is_err());
    }

    #[test]
    fn display_writes_the_normalized_value() {
        assert_eq!(d("2.000000").to_string(), "2");
        assert_eq!(d("-0.0").to_string(), "0");
        assert_eq!(d("30.30").to_string(), "30.3");
        assert_eq!(d("1e-7").to_string(), "0.0000001");
    }

    #[test]
    fn authored_values_inside_the_envelope_parse() {
        assert!("999999999999999.9999999999".parse::<AuthoredDec>().is_ok());
        assert!("-999999999999999.99".parse::<AuthoredDec>().is_ok());
        assert!("0.0000000001".parse::<AuthoredDec>().is_ok());
    }

    #[test]
    fn authored_values_outside_the_envelope_are_refused() {
        let at_limit = "1000000000000000".parse::<AuthoredDec>().unwrap_err();
        assert!(at_limit.to_string().contains("below 10^15"));
        assert!("-1e15".parse::<AuthoredDec>().is_err());
        let eleven = "0.00000000001".parse::<AuthoredDec>().unwrap_err();
        assert!(eleven.to_string().contains("at most 10 decimal places"));
    }

    #[test]
    fn negative_zero_and_zero_exponents_parse_as_zero() {
        for text in ["-0", "-0.0", "0e5", "-0.000e-3", "0.0"] {
            assert_eq!(d(text), Dec::ZERO, "{text}");
        }
    }

    #[test]
    fn parse_refuses_grammar_breaks_and_names_them() {
        for text in [
            "1e+-5", "1e--5", "1.e5", "1..2", " 1", "1 ", "--1", "1e5.0", "0x1", "1f64",
        ] {
            let err = text.parse::<Dec>().unwrap_err();
            assert!(
                err.to_string().contains("not a JSON number"),
                "{text}: {err}"
            );
        }
    }

    #[test]
    fn a_huge_exponent_is_refused_as_out_of_range_or_too_precise() {
        let large = "1e99999999999999999999".parse::<Dec>().unwrap_err();
        assert!(
            large.to_string().contains("beyond the decimal range"),
            "{large}"
        );
        let small = "1e-99999999999999999999".parse::<Dec>().unwrap_err();
        assert!(
            small.to_string().contains("more than 28 decimal places"),
            "{small}"
        );
    }

    #[test]
    fn extreme_exponents_are_refused_without_a_panic() {
        for exponent in [i64::MAX, i64::MAX - 1, i64::MIN, i64::MIN + 1] {
            let text = format!("1e{exponent}");
            assert!(text.parse::<Dec>().is_err(), "{text}");
            assert!(text.parse::<AuthoredDec>().is_err(), "{text}");
            let zero = format!("0e{exponent}");
            assert_eq!(zero.parse::<Dec>().unwrap(), Dec::ZERO, "{zero}");
        }
    }

    #[test]
    fn an_authored_zero_is_held_to_its_written_scale() {
        assert!("0.0000000000".parse::<AuthoredDec>().is_ok());
        assert!("0e-10".parse::<AuthoredDec>().is_ok());
        for text in [
            "0.00000000000",
            "0e-11",
            "0e-99999999999999999999",
            "-0.0e-10",
        ] {
            assert!(text.parse::<AuthoredDec>().is_err(), "{text}");
            assert_eq!(text.parse::<Dec>().unwrap(), Dec::ZERO, "{text}");
        }
    }

    #[test]
    fn trailing_zeros_past_the_digit_limit_still_parse() {
        assert_eq!(
            d("1234567890123.0000000000000000000000000000"),
            d("1234567890123")
        );
    }

    fn oracle(text: &str) -> BigDecimal {
        text.parse().unwrap()
    }

    fn written_scale(text: &str) -> i64 {
        let body = text.trim_start_matches('-');
        let (mantissa, exponent) = match body.find(['e', 'E']) {
            Some(at) => (&body[..at], body[at + 1..].parse::<i64>().unwrap()),
            None => (body, 0),
        };
        let fraction = mantissa.split_once('.').map_or(0, |(_, f)| f.len() as i64);
        (fraction - exponent).max(0)
    }

    proptest! {
        #[test]
        fn parse_agrees_with_an_arbitrary_precision_oracle(
            text in "-?(0|[1-9][0-9]{0,35})(\\.[0-9]{1,40})?([eE][+-]?[0-9]{1,2})?",
        ) {
            let exact = oracle(&text);
            match text.parse::<Dec>() {
                Ok(value) => {
                    prop_assert!(!exceeds_the_decimal_range(&exact), "{} parsed but cannot be held", text);
                    prop_assert_eq!(big(value), exact);
                }
                Err(e) => prop_assert!(exceeds_the_decimal_range(&exact), "{} refused ({}) but fits", text, e),
            }
        }

        #[test]
        fn authored_parse_applies_the_envelope_to_the_written_value(
            text in "-?(0|[1-9][0-9]{0,17})(\\.[0-9]{1,14})?([eE][+-]?[0-9])?",
        ) {
            let exact = oracle(&text);
            let inside = exact.abs() < 1_000_000_000_000_000i64
                && written_scale(&text) <= 10;
            prop_assert_eq!(text.parse::<AuthoredDec>().is_ok(), inside, "{}", text);
        }

        #[test]
        fn a_leading_zero_is_refused(text in "-?0[0-9]{1,5}(\\.[0-9]{1,3})?") {
            prop_assert!(text.parse::<Dec>().is_err(), "{}", text);
        }
    }

    #[test]
    fn threshold_sums_and_products_are_exact() {
        assert_eq!(d("10.10").try_add(d("20.20")).unwrap(), d("30.30"));
        assert_eq!(d("100").try_mul(d("0.07")).unwrap(), d("7"));
        assert_eq!(d("0.29").try_mul(d("100000")).unwrap(), d("29000"));
    }

    #[test]
    fn zero_results_are_exact() {
        assert_eq!(d("0").try_mul(d("0.05")).unwrap(), Dec::ZERO);
        assert_eq!(d("1.5").try_sub(d("1.5")).unwrap(), Dec::ZERO);
    }

    #[test]
    fn a_lossless_scale_reduction_is_exact() {
        let (a, b) = (d("0.000000000000000100"), d("0.0000000000100"));
        assert!(fast_mul(a.0, b.0).is_none());
        assert_eq!(a.try_mul(b).unwrap(), d("1e-27"));
        let negative = d("-0.000000000000000100");
        assert!(fast_mul(negative.0, b.0).is_none());
        assert_eq!(negative.try_mul(b).unwrap(), d("-1e-27"));
    }

    #[test]
    fn a_wide_sum_reduces_to_a_value_it_can_hold() {
        let a = Dec(Decimal::from_i128_with_scale((1i128 << 96) - 1, 1));
        let b = d("0.5");
        assert!(fast_add(a.0, b.0, false).is_none());
        assert_eq!(a.try_add(b).unwrap(), d("7922816251426433759354395034"));
    }

    #[test]
    fn wide_differences_keep_their_sign() {
        let a = d("39614081257132168796771975168");
        let b = d("1.0000000000000000");
        let neg_a = d("-39614081257132168796771975168");
        assert!(fast_add(a.0, b.0, true).is_none());
        assert_eq!(a.try_sub(b).unwrap(), d("39614081257132168796771975167"));
        assert_eq!(b.try_sub(a).unwrap(), d("-39614081257132168796771975167"));
        assert_eq!(
            neg_a.try_sub(b).unwrap(),
            d("-39614081257132168796771975169")
        );
        assert_eq!(
            b.try_sub(neg_a).unwrap(),
            d("39614081257132168796771975169")
        );
        assert_eq!(
            neg_a.try_add(b).unwrap(),
            d("-39614081257132168796771975167")
        );
    }

    #[test]
    fn results_just_past_the_range_are_classified_by_their_integer_part() {
        let max = d("79228162514264337593543950335");
        let kind = |r: Result<Dec, NumericError>| r.unwrap_err().kind;
        assert_eq!(kind(max.try_add(d("0.5"))), NumericErrorKind::Precision);
        assert_eq!(kind(max.try_add(d("1.5"))), NumericErrorKind::Overflow);
        let below = d("-79228162514264337593543950335")
            .try_sub(d("1"))
            .unwrap_err();
        assert_eq!(
            (below.kind, below.op),
            (NumericErrorKind::Overflow, NumericOp::Sub)
        );
        assert_eq!(
            kind(max.try_mul(d("-1.0000000000000000000000000001"))),
            NumericErrorKind::Overflow
        );
        assert_eq!(
            kind(max.try_mul(d("0.9999999999999999999999999999"))),
            NumericErrorKind::Precision
        );
    }

    #[test]
    fn fit_classifies_a_scale_beyond_i512_as_a_precision_error() {
        assert_eq!(fit(I512::from(7i32), 200), Err(NumericErrorKind::Precision));
        assert_eq!(fit(I512::from(7i32), 154), Err(NumericErrorKind::Precision));
    }

    #[test]
    fn a_sum_beyond_the_range_is_an_overflow() {
        let max = d("79228162514264337593543950335");
        let err = max.try_add(d("1")).unwrap_err();
        assert_eq!(err.kind, NumericErrorKind::Overflow);
        assert_eq!(err.op, NumericOp::Add);
        assert_eq!(
            err.to_string(),
            "cannot add 79228162514264337593543950335 and 1: the result is beyond the decimal range"
        );
    }

    #[test]
    fn a_product_needing_more_than_28_places_is_a_precision_error() {
        let tiny = d("0.0000000000000001");
        let err = tiny.try_mul(tiny).unwrap_err();
        assert_eq!(err.kind, NumericErrorKind::Precision);
    }

    #[test]
    fn a_four_factor_payout_is_exact() {
        let payout = d("1234.56")
            .try_mul(d("0.40"))
            .and_then(|v| v.try_mul(d("1.25")))
            .and_then(|v| v.try_mul(d("0.07")))
            .unwrap();
        assert_eq!(payout, d("43.2096"));
    }

    #[test]
    fn a_hundred_thousand_cent_values_sum_exactly() {
        let values = (0..100_000).map(|i| d(&format!("{}.{:02}", i % 997, i % 100)));
        let expected: i64 = (0..100_000i64).map(|i| (i % 997) * 100 + i % 100).sum();
        assert_eq!(
            Dec::sum(values).unwrap(),
            d(&format!("{}.{:02}", expected / 100, expected % 100))
        );
    }

    fn arb_dec() -> impl Strategy<Value = Dec> {
        prop_oneof![
            Just(Dec::ZERO),
            Just(d("79228162514264337593543950335")),
            Just(d("-79228162514264337593543950335")),
            Just(d("0.0000000000000000000000000001")),
            (any::<i128>(), 0u32..=28)
                .prop_map(|(m, s)| Dec(Decimal::from_i128_with_scale(m % (1i128 << 96), s))),
        ]
    }

    proptest! {
        #[test]
        fn add_sub_mul_agree_with_an_arbitrary_precision_oracle(a in arb_dec(), b in arb_dec()) {
            for (result, exact) in [
                (a.try_add(b), big(a) + big(b)),
                (a.try_sub(b), big(a) - big(b)),
                (a.try_mul(b), big(a) * big(b)),
            ] {
                match result {
                    Ok(value) => prop_assert_eq!(big(value), exact),
                    Err(e) => prop_assert!(exceeds_the_decimal_range(&exact), "refused {:?} but {} fits", e, exact),
                }
            }
        }
    }

    fn small_dec() -> impl Strategy<Value = Dec> {
        (-(1i128 << 47)..(1i128 << 47), 0u32..=10)
            .prop_map(|(m, s)| Dec(Decimal::from_i128_with_scale(m, s)))
    }

    fn wide_mantissa() -> impl Strategy<Value = i128> {
        ((1i128 << 94)..=(1i128 << 96) - 1, any::<bool>())
            .prop_map(|(m, negative)| if negative { -m } else { m })
    }

    proptest! {
        #[test]
        fn small_operands_take_the_i128_path_and_agree_with_the_oracle(a in small_dec(), b in small_dec()) {
            prop_assert!(fast_add(a.0, b.0, false).is_some());
            prop_assert!(fast_add(a.0, b.0, true).is_some());
            prop_assert!(fast_mul(a.0, b.0).is_some());
            prop_assert_eq!(big(a.try_add(b).unwrap()), big(a) + big(b));
            prop_assert_eq!(big(a.try_sub(b).unwrap()), big(a) - big(b));
            prop_assert_eq!(big(a.try_mul(b).unwrap()), big(a) * big(b));
        }

        #[test]
        fn wide_operands_take_the_bnum_path_and_agree_with_the_oracle(
            ma in wide_mantissa(),
            mb in wide_mantissa(),
            scale in 0u32..=14,
        ) {
            // A 2^94 mantissa rescaled by 10^14 needs more than 127 bits.
            let a = Dec(Decimal::from_i128_with_scale(ma, scale));
            let b = Dec(Decimal::from_i128_with_scale(mb, scale + 14));
            prop_assert!(fast_add(a.0, b.0, false).is_none());
            prop_assert!(fast_add(a.0, b.0, true).is_none());
            prop_assert!(fast_mul(a.0, b.0).is_none());
            for (result, exact) in [
                (a.try_add(b), big(a) + big(b)),
                (a.try_sub(b), big(a) - big(b)),
                (a.try_mul(b), big(a) * big(b)),
            ] {
                match result {
                    Ok(value) => prop_assert_eq!(big(value), exact),
                    Err(e) => prop_assert!(exceeds_the_decimal_range(&exact), "refused {:?} but {} fits", e, exact),
                }
            }
        }
    }

    #[test]
    fn div_ratio_follows_checked_div() {
        assert_eq!(
            d("1").div_ratio(d("3")).unwrap().to_string(),
            "0.3333333333333333333333333333"
        );
        assert_eq!(
            d("1").div_ratio(d("30")).unwrap().to_string(),
            "0.0333333333333333333333333333"
        );
        assert_eq!(
            d("10000").div_ratio(d("3")).unwrap().to_string(),
            "3333.3333333333333333333333333"
        );
        assert_eq!(
            d("1")
                .div_ratio(d("3000000000000000000000000000"))
                .unwrap()
                .to_string(),
            "0.0000000000000000000000000003"
        );
    }

    #[test]
    fn each_division_refuses_a_zero_divisor() {
        for err in [
            d("1").div_ratio(Dec::ZERO).unwrap_err(),
            d("1").mul_div_trunc(d("2"), Dec::ZERO, 13).unwrap_err(),
            d("1").floor_div(Dec::ZERO).unwrap_err(),
        ] {
            assert_eq!(err.kind, NumericErrorKind::DivisionByZero);
        }
    }

    #[test]
    fn mul_div_trunc_truncates_at_the_requested_scale() {
        assert_eq!(
            d("1").mul_div_trunc(d("2"), d("3"), 13).unwrap(),
            d("0.6666666666666")
        );
        assert_eq!(
            d("2").mul_div_trunc(d("2"), d("3"), 13).unwrap(),
            d("1.3333333333333")
        );
    }

    #[test]
    fn a_share_at_the_envelope_edge_fits_at_scale_13() {
        let cap = d("999999999999999.99");
        let share = d("1").mul_div_trunc(cap, d("3"), 13).unwrap();
        assert_eq!(share, d("333333333333333.33"));
    }

    #[test]
    fn mul_div_trunc_refuses_a_scale_past_28_without_a_panic() {
        for scale in [29, 512, u32::MAX] {
            let err = d("1").mul_div_trunc(d("2"), d("3"), scale).unwrap_err();
            assert_eq!(err.kind, NumericErrorKind::Precision);
        }
    }

    #[test]
    fn floor_div_rounds_down() {
        assert_eq!(d("7").floor_div(d("2")).unwrap(), d("3"));
        assert_eq!(d("30.30").floor_div(d("10.10")).unwrap(), d("3"));
        assert_eq!(d("0.3").floor_div(d("0.1")).unwrap(), d("3"));
        assert_eq!(d("-7").floor_div(d("2")).unwrap(), d("-4"));
    }

    proptest! {
        #[test]
        fn mul_div_trunc_back_multiplies_within_one_unit(
            earning in 0i64..100_000_000_000_000_000,
            rest in 0i64..100_000_000_000_000_000,
            cap in 0i64..99_999_999_999_999_999,
        ) {
            let a = Dec(Decimal::new(earning, 2));
            let c = Dec(Decimal::new(earning + rest + 1, 2));
            let b = Dec(Decimal::new(cap, 2));
            let q = a.mul_div_trunc(b, c, 13).unwrap();
            let unit = BigDecimal::new(1.into(), 13);
            let product = big(a) * big(b);
            prop_assert!(big(q) * big(c) <= product);
            prop_assert!((big(q) + unit) * big(c) > product);
        }

        #[test]
        fn floor_div_back_multiplies_within_one(
            matched in 0i64..100_000_000_000,
            highest in 1i64..10_000_000,
        ) {
            let (a, b) = (Dec(Decimal::new(matched, 2)), Dec(Decimal::new(highest, 2)));
            let q = a.floor_div(b).unwrap();
            prop_assert!(big(q) * big(b) <= big(a));
            prop_assert!((big(q) + BigDecimal::from(1)) * big(b) > big(a));
        }
    }
}
