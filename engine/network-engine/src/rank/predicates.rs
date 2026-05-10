//! Per-criterion predicate evaluators (PV, GV, max-leg-GV, retail, distributor count, products).

use uuid::Uuid;

use crate::rank::evaluator::VolumeIndex;
use crate::rank::types::DistributorPrimitives;
use crate::tree::error::TreeError;
use crate::tree::navigator::TreeNavigator;

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

/// Pass when group volume (downline CV + own PV) meets `required`.
///
/// "Group volume" follows the design: sum of `cv_amount` across the
/// distributor's downline (any depth) plus the distributor's own
/// `personal_volume` from primitives. The distributor's own CV from
/// `volume_sources` is not added on top — `personal_volume` is the
/// authoritative own contribution.
#[allow(dead_code)] // Wired up by `satisfies()` in a later task.
pub(crate) fn gv_meets(
    required: f64,
    user_id: Uuid,
    tree: &dyn TreeNavigator,
    volume_index: &VolumeIndex,
    primitives: &DistributorPrimitives,
) -> Result<bool, TreeError> {
    // depth=0 is unbounded in TreeNavigator semantics.
    let downline = tree.get_downline(user_id, 0)?;
    let downline_cv: f64 = downline
        .iter()
        .map(|n| volume_index.cv_for(n.user_id))
        .sum();
    Ok(downline_cv + primitives.personal_volume >= required)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::types::VolumeSource;
    use crate::rank::evaluator::VolumeIndex;
    use crate::rank::types::DistributorPrimitives;
    use crate::tree::unilevel::UnilevelTree;
    use uuid::Uuid;

    fn uid(i: u128) -> Uuid {
        Uuid::from_u128(i)
    }

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

    #[test]
    fn gv_meets_sums_downline_cv_plus_own_pv() {
        // Build chain: 1 -> 2 -> 3
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(2), uid(2), 0).unwrap();

        let sources = vec![
            VolumeSource {
                source_id: uid(2),
                cv_amount: 100.0,
            },
            VolumeSource {
                source_id: uid(3),
                cv_amount: 200.0,
            },
        ];
        let idx = VolumeIndex::build(&sources);

        // Distributor 1's GV = downline CV (100 + 200) + own PV (50) = 350.
        let primitives = DistributorPrimitives {
            personal_volume: 50.0,
            retail_volume: 0.0,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        };

        assert!(gv_meets(350.0, uid(1), &tree, &idx, &primitives).unwrap());
        assert!(!gv_meets(350.01, uid(1), &tree, &idx, &primitives).unwrap());
    }

    #[test]
    fn gv_meets_errors_when_distributor_missing_from_tree() {
        let tree = UnilevelTree::new();
        let idx = VolumeIndex::build(&[]);
        let primitives = primitives_with_pv(0.0);

        let err = gv_meets(0.0, uid(99), &tree, &idx, &primitives).unwrap_err();
        // We don't pin the structure name here; the caller does. The error
        // surfaces from get_downline as a TreeError that the predicate
        // converts to None / Err. Predicates return Result<bool, TreeError>.
        assert!(format!("{:?}", err).contains("UserNotFound"));
    }
}
