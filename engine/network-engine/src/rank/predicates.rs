//! Per-criterion predicate evaluators (PV, GV, max-leg-GV, retail, distributor count, products).

use crate::rank::types::DistributorPrimitives;

/// Pass when the distributor's personal volume is at least `required`.
#[allow(dead_code)] // Wired up by `satisfies()` in a later task.
pub(crate) fn pv_meets(required: f64, primitives: &DistributorPrimitives) -> bool {
    primitives.personal_volume >= required
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rank::types::DistributorPrimitives;

    fn primitives_with_pv(pv: f64) -> DistributorPrimitives {
        DistributorPrimitives {
            personal_volume: pv,
            retail_volume: 0.0,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        }
    }

    #[test]
    fn pv_meets_returns_true_when_pv_at_or_above_threshold() {
        assert!(pv_meets(100.0, &primitives_with_pv(100.0)));
        assert!(pv_meets(100.0, &primitives_with_pv(150.0)));
    }

    #[test]
    fn pv_meets_returns_false_when_pv_below_threshold() {
        assert!(!pv_meets(100.0, &primitives_with_pv(99.99)));
        assert!(!pv_meets(100.0, &primitives_with_pv(0.0)));
    }
}
