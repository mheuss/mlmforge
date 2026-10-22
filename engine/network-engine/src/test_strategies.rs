//! Proptest strategies shared by the engine's unit tests.

use proptest::prelude::*;

use crate::test_support::FromLiteral;

/// Whole cents in `[lo, hi)`, as any decimal test type.
pub(crate) fn cents<T: FromLiteral + std::fmt::Debug>(
    lo: u64,
    hi: u64,
) -> impl Strategy<Value = T> {
    (lo..hi).prop_map(|c| T::from_literal(&format!("{}.{:02}", c / 100, c % 100)))
}
