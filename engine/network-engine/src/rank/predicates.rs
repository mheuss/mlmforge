//! Per-criterion predicate evaluators (PV, GV, max-leg-GV, retail, distributor count, products).

use crate::rank::types::DistributorPrimitives;

/// Pass when the distributor's personal volume is at least `required`.
#[allow(dead_code)] // Wired up by `satisfies()` in a later task.
pub(crate) fn pv_meets(required: f64, primitives: &DistributorPrimitives) -> bool {
    primitives.personal_volume >= required
}

/// Pass when the distributor's retail volume is at least `required`.
#[allow(dead_code)] // Wired up by `satisfies()` in a later task.
pub(crate) fn retail_meets(required: f64, primitives: &DistributorPrimitives) -> bool {
    primitives.retail_volume >= required
}

/// Pass when at least one element of `required` appears in `primitives.active_products`.
/// An empty `required` list always passes.
#[allow(dead_code)] // Wired up by `satisfies()` in a later task.
pub(crate) fn required_products_met(
    required: &[String],
    primitives: &DistributorPrimitives,
) -> bool {
    if required.is_empty() {
        return true;
    }
    required
        .iter()
        .any(|r| primitives.active_products.iter().any(|p| p == r))
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

    fn primitives_with_retail(retail: f64) -> DistributorPrimitives {
        DistributorPrimitives {
            personal_volume: 0.0,
            retail_volume: retail,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        }
    }

    #[test]
    fn retail_meets_returns_true_when_at_or_above_threshold() {
        assert!(retail_meets(50.0, &primitives_with_retail(50.0)));
        assert!(retail_meets(50.0, &primitives_with_retail(75.0)));
    }

    #[test]
    fn retail_meets_returns_false_when_below_threshold() {
        assert!(!retail_meets(50.0, &primitives_with_retail(49.99)));
    }

    fn primitives_with_products(products: Vec<&str>) -> DistributorPrimitives {
        DistributorPrimitives {
            personal_volume: 0.0,
            retail_volume: 0.0,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: products.into_iter().map(String::from).collect(),
        }
    }

    #[test]
    fn required_products_met_passes_when_required_is_empty() {
        assert!(required_products_met(
            &[],
            &primitives_with_products(vec![])
        ));
    }

    #[test]
    fn required_products_met_passes_when_distributor_holds_any_required_product() {
        let req = ["premium-kit".to_string(), "exec-kit".to_string()];
        assert!(required_products_met(
            &req,
            &primitives_with_products(vec!["premium-kit"])
        ));
        assert!(required_products_met(
            &req,
            &primitives_with_products(vec!["exec-kit", "extra"])
        ));
    }

    #[test]
    fn required_products_met_fails_when_no_required_product_held() {
        let req = ["premium-kit".to_string()];
        assert!(!required_products_met(
            &req,
            &primitives_with_products(vec!["other"])
        ));
        assert!(!required_products_met(
            &req,
            &primitives_with_products(vec![])
        ));
    }
}
