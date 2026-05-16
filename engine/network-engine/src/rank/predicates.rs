//! Per-criterion predicate evaluators (PV, GV, max-leg-GV, retail, distributor count, products).

use std::collections::HashMap;

use uuid::Uuid;

use crate::config::rank::{
    DistributorCountRequirement, LegPredicate, LegQualityRequirement, RankDefinition, SearchMode,
};
use crate::rank::evaluator::VolumeIndex;
use crate::rank::types::{DistributorPrimitives, EvaluatedRank, EvaluationError};
use crate::tree::error::TreeError;
use crate::tree::navigator::TreeNavigator;

/// Reasons distributor_count evaluation can fail. Caller maps these to
/// `EvaluationError` variants with rank context attached.
#[derive(Debug, PartialEq)]
pub(crate) enum DistributorCountError {
    Tree(TreeError),
    UnknownMinRank(String),
}

impl From<TreeError> for DistributorCountError {
    fn from(e: TreeError) -> Self {
        DistributorCountError::Tree(e)
    }
}

/// Reasons leg_quality evaluation can fail. Caller maps these to
/// `EvaluationError` variants with rank context attached.
#[derive(Debug, PartialEq)]
pub(crate) enum LegQualityError {
    Tree(TreeError),
    UnknownMinRank(String),
}

impl From<TreeError> for LegQualityError {
    fn from(e: TreeError) -> Self {
        LegQualityError::Tree(e)
    }
}

/// Pass when the distributor's personal volume is at least `required`.
pub(crate) fn pv_meets(required: f64, primitives: &DistributorPrimitives) -> bool {
    primitives.personal_volume >= required
}

/// Pass when the distributor's retail volume is at least `required`.
pub(crate) fn retail_meets(required: f64, primitives: &DistributorPrimitives) -> bool {
    primitives.retail_volume >= required
}

/// Pass when at least one element of `required` appears in `primitives.active_products`.
/// An empty `required` list always passes.
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

/// Pass when no first-level child's subtree CV exceeds `cap`.
///
/// For each direct child of `user_id`, sum CV across the child plus their
/// downline. The maximum of those sums must be at most `cap`.
pub(crate) fn max_leg_gv_meets(
    cap: f64,
    user_id: Uuid,
    tree: &dyn TreeNavigator,
    volume_index: &VolumeIndex,
) -> Result<bool, TreeError> {
    let children = tree.get_children(user_id)?;
    let mut max_leg = 0.0_f64;
    for child in children {
        let mut leg_cv = volume_index.cv_for(child.user_id);
        let descendants = tree.get_downline(child.user_id, 0)?;
        for d in descendants {
            leg_cv += volume_index.cv_for(d.user_id);
        }
        if leg_cv > max_leg {
            max_leg = leg_cv;
        }
    }
    Ok(max_leg <= cap)
}

/// Pass when the downline contains enough qualifying distributors per the
/// `DistributorCountRequirement`.
///
/// Counts nodes within `search_depth` (or the entire downline for `AnyLevel`)
/// whose evaluated rank ordinal >= `min_rank` ordinal AND whose group volume
/// (subtree CV including the node) >= `min_leg_group_volume`. The total
/// downline node count within scope must also be >= `total_count`.
pub(crate) fn distributor_count_meets(
    req: &DistributorCountRequirement,
    user_id: Uuid,
    tree: &dyn TreeNavigator,
    volume_index: &VolumeIndex,
    already: &HashMap<Uuid, EvaluatedRank>,
    rank_ordinals: &HashMap<String, u16>,
) -> Result<bool, DistributorCountError> {
    let min_ordinal = rank_ordinals
        .get(&req.min_rank)
        .copied()
        .ok_or_else(|| DistributorCountError::UnknownMinRank(req.min_rank.clone()))?;

    let depth: u32 = match req.search_mode {
        SearchMode::FirstLevels => req.search_depth.unwrap_or(0) as u32,
        SearchMode::AnyLevel => 0, // unbounded
    };

    let nodes = tree.get_downline(user_id, depth)?;

    if nodes.len() < req.total_count as usize {
        return Ok(false);
    }

    let mut qualifying: usize = 0;
    for node in nodes {
        let Some(rank) = already.get(&node.user_id) else {
            continue;
        };
        let ord = match rank {
            EvaluatedRank::Qualified { ordinal, .. } => *ordinal,
            EvaluatedRank::Unranked => continue,
        };
        if ord < min_ordinal {
            continue;
        }
        // Group volume for this node = node CV + descendants' CV.
        let mut leg_gv = volume_index.cv_for(node.user_id);
        let desc = tree.get_downline(node.user_id, 0)?;
        for d in desc {
            leg_gv += volume_index.cv_for(d.user_id);
        }
        if leg_gv >= req.min_leg_group_volume {
            qualifying += 1;
        }
    }

    Ok(qualifying >= req.count as usize)
}

/// Pass when, for every requirement, at least `count` of `user_id`'s
/// frontline legs contain a node matching the requirement's predicate.
///
/// A "leg" is a direct child of `user_id` plus that child's entire
/// subtree (the child included). Requirements are AND-combined.
pub(crate) fn leg_quality_meets(
    reqs: &[LegQualityRequirement],
    user_id: Uuid,
    tree: &dyn TreeNavigator,
    already: &HashMap<Uuid, EvaluatedRank>,
    distributors: &HashMap<Uuid, DistributorPrimitives>,
    rank_ordinals: &HashMap<String, u16>,
) -> Result<bool, LegQualityError> {
    if reqs.is_empty() {
        return Ok(true); // BR5: empty is an exact no-op, no tree calls.
    }
    let children = tree.get_children(user_id)?;
    for req in reqs {
        if req.count == 0 {
            // A count-0 requirement is vacuously satisfied. Skip the leg scan.
            continue;
        }
        if req.count as usize > children.len() {
            // At most children.len() legs can match, so a count larger than
            // the frontline leg count cannot be met. Mirrors the population-size
            // early-return in distributor_count_meets (the nodes.len() < req.total_count check).
            return Ok(false);
        }
        let mut matching_legs: usize = 0;
        for child in &children {
            // A leg is the frontline child plus its whole subtree.
            if leg_contains_match(
                &req.predicate,
                child.user_id,
                tree,
                already,
                distributors,
                rank_ordinals,
            )? {
                matching_legs += 1;
            }
        }
        if matching_legs < req.count as usize {
            return Ok(false);
        }
    }
    Ok(true)
}

/// Pass when the leg rooted at `leg_root` (the node itself plus its entire
/// subtree) contains at least one node matching `predicate`. Short-circuits
/// on the first match.
fn leg_contains_match(
    predicate: &LegPredicate,
    leg_root: Uuid,
    tree: &dyn TreeNavigator,
    already: &HashMap<Uuid, EvaluatedRank>,
    distributors: &HashMap<Uuid, DistributorPrimitives>,
    rank_ordinals: &HashMap<String, u16>,
) -> Result<bool, LegQualityError> {
    if node_matches(predicate, leg_root, already, distributors, rank_ordinals)? {
        return Ok(true);
    }
    // depth=0 is unbounded in TreeNavigator semantics. get_downline is
    // deterministic BFS — reused here to preserve the NFR2 ordering guarantee.
    for node in tree.get_downline(leg_root, 0)? {
        if node_matches(
            predicate,
            node.user_id,
            already,
            distributors,
            rank_ordinals,
        )? {
            return Ok(true);
        }
    }
    Ok(false)
}

/// Test a single node against `predicate`.
///
/// `ContainsRank` resolves `min_rank` against `rank_ordinals` (unknown ranks
/// raise `UnknownMinRank`) and reads the node's evaluated rank from `already`;
/// a node that is absent or `Unranked` does not match. `ContainsPersonalVolume`
/// reads the node's personal volume from `distributors`; a node absent from
/// the map does not match. Plan validation (the Go layer) is the primary gate
/// for `min_rank`; this error is defense in depth.
fn node_matches(
    predicate: &LegPredicate,
    node_id: Uuid,
    already: &HashMap<Uuid, EvaluatedRank>,
    distributors: &HashMap<Uuid, DistributorPrimitives>,
    rank_ordinals: &HashMap<String, u16>,
) -> Result<bool, LegQualityError> {
    match predicate {
        LegPredicate::ContainsRank { min_rank } => {
            let min_ordinal = rank_ordinals
                .get(min_rank)
                .copied()
                .ok_or_else(|| LegQualityError::UnknownMinRank(min_rank.clone()))?;
            match already.get(&node_id) {
                Some(EvaluatedRank::Qualified { ordinal, .. }) => Ok(*ordinal >= min_ordinal),
                Some(EvaluatedRank::Unranked) | None => Ok(false),
            }
        }
        LegPredicate::ContainsPersonalVolume {
            min_personal_volume,
        } => match distributors.get(&node_id) {
            Some(primitives) => Ok(primitives.personal_volume >= *min_personal_volume),
            None => Ok(false),
        },
    }
}

/// Pass when every structure qualification on `rank` passes for `user_id`
/// and the `required_products` clause passes.
///
/// Errors:
/// - `UnknownStructure` if the rank references a structure not in `trees`.
/// - `UnknownMinRank` if a `DistributorCountRequirement.min_rank` is not in `rank_ordinals`.
/// - `DistributorNotInTree` if the user is not present in a tree the rank references.
pub(crate) fn satisfies(
    rank: &RankDefinition,
    user_id: Uuid,
    distributors: &HashMap<Uuid, DistributorPrimitives>,
    trees: &HashMap<String, &dyn TreeNavigator>,
    volume_index: &VolumeIndex,
    already: &HashMap<Uuid, EvaluatedRank>,
    rank_ordinals: &HashMap<String, u16>,
) -> Result<bool, EvaluationError> {
    // evaluate_ranks only evaluates users present in `distributors`, so this
    // lookup always succeeds. A subject with no primitives cannot meet any
    // volume or product criterion, so treat the absence as not-qualifying.
    let Some(primitives) = distributors.get(&user_id) else {
        return Ok(false);
    };

    if !required_products_met(&rank.qualification.required_products, primitives) {
        return Ok(false);
    }

    for sq in &rank.qualification.structures {
        let tree = trees
            .get(&sq.structure)
            .ok_or_else(|| EvaluationError::UnknownStructure {
                rank: rank.name.clone(),
                structure: sq.structure.clone(),
            })?;

        if !tree.contains(user_id) {
            return Err(EvaluationError::DistributorNotInTree(
                user_id,
                sq.structure.clone(),
            ));
        }

        if !pv_meets(sq.personal_volume, primitives) {
            return Ok(false);
        }
        if !retail_meets(sq.min_retail_volume, primitives) {
            return Ok(false);
        }
        match gv_meets(sq.group_volume, user_id, *tree, volume_index, primitives) {
            Ok(true) => {}
            Ok(false) => return Ok(false),
            Err(_) => {
                return Err(EvaluationError::DistributorNotInTree(
                    user_id,
                    sq.structure.clone(),
                ));
            }
        }
        match max_leg_gv_meets(sq.max_group_volume_per_leg, user_id, *tree, volume_index) {
            Ok(true) => {}
            Ok(false) => return Ok(false),
            Err(_) => {
                return Err(EvaluationError::DistributorNotInTree(
                    user_id,
                    sq.structure.clone(),
                ));
            }
        }
        if let Some(req) = &sq.distributor_count {
            match distributor_count_meets(req, user_id, *tree, volume_index, already, rank_ordinals)
            {
                Ok(true) => {}
                Ok(false) => return Ok(false),
                Err(DistributorCountError::Tree(_)) => {
                    return Err(EvaluationError::DistributorNotInTree(
                        user_id,
                        sq.structure.clone(),
                    ));
                }
                Err(DistributorCountError::UnknownMinRank(referenced)) => {
                    return Err(EvaluationError::UnknownMinRank {
                        rank: rank.name.clone(),
                        referenced,
                    });
                }
            }
        }
        match leg_quality_meets(
            &sq.leg_quality,
            user_id,
            *tree,
            already,
            distributors,
            rank_ordinals,
        ) {
            Ok(true) => {}
            Ok(false) => return Ok(false),
            Err(LegQualityError::Tree(_)) => {
                return Err(EvaluationError::DistributorNotInTree(
                    user_id,
                    sq.structure.clone(),
                ));
            }
            Err(LegQualityError::UnknownMinRank(referenced)) => {
                return Err(EvaluationError::UnknownMinRank {
                    rank: rank.name.clone(),
                    referenced,
                });
            }
        }
    }
    Ok(true)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::types::VolumeSource;
    use crate::rank::evaluator::VolumeIndex;
    use crate::rank::types::DistributorPrimitives;
    use crate::tree::error::TreeError;
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
        // surfaces from get_downline as a TreeError. Predicates return
        // Result<bool, TreeError>.
        assert_eq!(err, TreeError::UserNotFound(uid(99)));
    }

    #[test]
    fn max_leg_gv_meets_passes_when_no_leg_exceeds_cap() {
        // Root with two first-level children, each subtree CV totaling 100, cap 150.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap(); // leg A
        tree.add_node(uid(3), uid(1), uid(1), 0).unwrap(); // leg B
        tree.add_node(uid(4), uid(2), uid(2), 0).unwrap(); // child of leg A

        let sources = vec![
            VolumeSource {
                source_id: uid(2),
                cv_amount: 50.0,
            },
            VolumeSource {
                source_id: uid(4),
                cv_amount: 50.0,
            }, // leg A subtotal: 100
            VolumeSource {
                source_id: uid(3),
                cv_amount: 100.0,
            }, // leg B subtotal: 100
        ];
        let idx = VolumeIndex::build(&sources);

        assert!(max_leg_gv_meets(150.0, uid(1), &tree, &idx).unwrap());
    }

    #[test]
    fn max_leg_gv_meets_fails_when_a_leg_exceeds_cap() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(1), uid(1), 0).unwrap();

        let sources = vec![
            VolumeSource {
                source_id: uid(2),
                cv_amount: 200.0,
            }, // leg A: 200
            VolumeSource {
                source_id: uid(3),
                cv_amount: 50.0,
            }, // leg B: 50
        ];
        let idx = VolumeIndex::build(&sources);

        assert!(!max_leg_gv_meets(150.0, uid(1), &tree, &idx).unwrap());
    }

    #[test]
    fn max_leg_gv_meets_passes_for_distributor_with_no_children() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]);
        // No legs means "max leg GV" = 0 ≤ any cap.
        assert!(max_leg_gv_meets(100.0, uid(1), &tree, &idx).unwrap());
    }

    use crate::config::rank::{DistributorCountRequirement, SearchMode};
    use crate::rank::types::EvaluatedRank;
    use std::collections::HashMap;

    #[test]
    fn distributor_count_meets_counts_qualifying_descendants() {
        // Tree: 1 -> 2, 1 -> 3, 1 -> 4. Each direct child has rank "bronze".
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(4), uid(1), uid(1), 0).unwrap();

        let mut already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        already.insert(
            uid(2),
            EvaluatedRank::Qualified {
                rank: "bronze".to_string(),
                ordinal: 1,
            },
        );
        already.insert(
            uid(3),
            EvaluatedRank::Qualified {
                rank: "bronze".to_string(),
                ordinal: 1,
            },
        );
        already.insert(
            uid(4),
            EvaluatedRank::Qualified {
                rank: "associate".to_string(),
                ordinal: 0,
            },
        );

        let mut ordinals: HashMap<String, u16> = HashMap::new();
        ordinals.insert("associate".to_string(), 0);
        ordinals.insert("bronze".to_string(), 1);

        let mut cv_per: HashMap<Uuid, f64> = HashMap::new();
        cv_per.insert(uid(2), 600.0);
        cv_per.insert(uid(3), 600.0);
        cv_per.insert(uid(4), 600.0);
        let sources: Vec<VolumeSource> = cv_per
            .into_iter()
            .map(|(id, cv)| VolumeSource {
                source_id: id,
                cv_amount: cv,
            })
            .collect();
        let idx = VolumeIndex::build(&sources);

        let req = DistributorCountRequirement {
            count: 2,
            min_rank: "bronze".to_string(),
            search_mode: SearchMode::FirstLevels,
            search_depth: Some(1),
            total_count: 3,
            min_leg_group_volume: 500.0,
        };

        assert!(distributor_count_meets(&req, uid(1), &tree, &idx, &already, &ordinals).unwrap());
    }

    #[test]
    fn distributor_count_meets_unknown_min_rank_errors() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        let idx = VolumeIndex::build(&[]);

        let req = DistributorCountRequirement {
            count: 1,
            min_rank: "unknown".to_string(),
            search_mode: SearchMode::AnyLevel,
            search_depth: None,
            total_count: 0,
            min_leg_group_volume: 0.0,
        };

        let err =
            distributor_count_meets(&req, uid(1), &tree, &idx, &already, &ordinals).unwrap_err();
        assert_eq!(
            err,
            DistributorCountError::UnknownMinRank("unknown".to_string())
        );
    }

    use crate::config::rank::{LegPredicate, LegQualityRequirement};

    fn ranked(name: &str, ordinal: u16) -> EvaluatedRank {
        EvaluatedRank::Qualified {
            rank: name.to_string(),
            ordinal,
        }
    }

    #[test]
    fn leg_quality_meets_empty_requirements_passes() {
        // BR5: empty requirements is an exact no-op and makes no tree calls.
        let tree = UnilevelTree::new();
        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        assert!(leg_quality_meets(&[], uid(1), &tree, &already, &distributors, &ordinals).unwrap());
    }

    #[test]
    fn leg_quality_meets_counts_legs_containing_rank() {
        // Root 1 has two legs: 2 (with child 4) and 3. Node 4 is Gold.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(4), uid(2), uid(2), 0).unwrap();

        let mut already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        already.insert(uid(4), ranked("gold", 3));

        let distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        let mut ordinals: HashMap<String, u16> = HashMap::new();
        ordinals.insert("gold".to_string(), 3);

        let reqs = vec![LegQualityRequirement {
            count: 1,
            predicate: LegPredicate::ContainsRank {
                min_rank: "gold".to_string(),
            },
        }];
        // Leg 2 contains a Gold node (4); leg 3 does not. One matching leg.
        assert!(
            leg_quality_meets(&reqs, uid(1), &tree, &already, &distributors, &ordinals).unwrap()
        );

        let reqs_two = vec![LegQualityRequirement {
            count: 2,
            predicate: LegPredicate::ContainsRank {
                min_rank: "gold".to_string(),
            },
        }];
        assert!(
            !leg_quality_meets(&reqs_two, uid(1), &tree, &already, &distributors, &ordinals)
                .unwrap()
        );
    }

    #[test]
    fn leg_quality_meets_counts_legs_containing_personal_volume() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(1), uid(1), 0).unwrap();

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(2), primitives_with_pv(200.0));
        distributors.insert(uid(3), primitives_with_pv(50.0));
        let ordinals: HashMap<String, u16> = HashMap::new();

        let reqs = vec![LegQualityRequirement {
            count: 1,
            predicate: LegPredicate::ContainsPersonalVolume {
                min_personal_volume: 200.0,
            },
        }];
        // Only leg 2 has a node with PV >= 200.
        assert!(
            leg_quality_meets(&reqs, uid(1), &tree, &already, &distributors, &ordinals).unwrap()
        );
    }

    #[test]
    fn leg_quality_meets_and_combines_multiple_requirements() {
        // Tiered, Oriflame-like: legs containing Gold AND a leg containing
        // Diamond. Root 1 has legs 2, 3, 4. Nodes 2 and 3 are Gold; node 4
        // is Diamond.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(4), uid(1), uid(1), 0).unwrap();

        let mut already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        already.insert(uid(2), ranked("gold", 3));
        already.insert(uid(3), ranked("gold", 3));
        already.insert(uid(4), ranked("diamond", 4));

        let distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        let mut ordinals: HashMap<String, u16> = HashMap::new();
        ordinals.insert("gold".to_string(), 3);
        ordinals.insert("diamond".to_string(), 4);

        // A Diamond node satisfies a `ContainsRank { min_rank: gold }` test
        // too (ordinal 4 >= 3), so all three legs count as Gold legs.
        let reqs = vec![
            LegQualityRequirement {
                count: 2,
                predicate: LegPredicate::ContainsRank {
                    min_rank: "gold".to_string(),
                },
            },
            LegQualityRequirement {
                count: 1,
                predicate: LegPredicate::ContainsRank {
                    min_rank: "diamond".to_string(),
                },
            },
        ];
        assert!(
            leg_quality_meets(&reqs, uid(1), &tree, &already, &distributors, &ordinals).unwrap()
        );

        // Raise the Diamond requirement to 2 — only one Diamond leg exists.
        let reqs_fail = vec![
            LegQualityRequirement {
                count: 2,
                predicate: LegPredicate::ContainsRank {
                    min_rank: "gold".to_string(),
                },
            },
            LegQualityRequirement {
                count: 2,
                predicate: LegPredicate::ContainsRank {
                    min_rank: "diamond".to_string(),
                },
            },
        ];
        assert!(
            !leg_quality_meets(
                &reqs_fail,
                uid(1),
                &tree,
                &already,
                &distributors,
                &ordinals
            )
            .unwrap()
        );
    }

    #[test]
    fn leg_quality_meets_unknown_min_rank_errors() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();

        let reqs = vec![LegQualityRequirement {
            count: 1,
            predicate: LegPredicate::ContainsRank {
                min_rank: "phantom".to_string(),
            },
        }];
        let err = leg_quality_meets(&reqs, uid(1), &tree, &already, &distributors, &ordinals)
            .unwrap_err();
        assert_eq!(err, LegQualityError::UnknownMinRank("phantom".to_string()));
    }

    #[test]
    fn leg_quality_meets_count_exceeds_frontline_returns_false() {
        // Root 1 has 2 frontline children. A requirement with count 3 cannot
        // be met — there are only 2 legs. Expect false without any subtree BFS.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(1), uid(1), 0).unwrap();

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(2), primitives_with_pv(500.0));
        distributors.insert(uid(3), primitives_with_pv(500.0));
        let ordinals: HashMap<String, u16> = HashMap::new();

        let reqs = vec![LegQualityRequirement {
            count: 3, // requires 3 legs but only 2 exist
            predicate: LegPredicate::ContainsPersonalVolume {
                min_personal_volume: 100.0,
            },
        }];
        assert!(
            !leg_quality_meets(&reqs, uid(1), &tree, &already, &distributors, &ordinals).unwrap()
        );
    }

    #[test]
    fn leg_quality_meets_count_zero_passes() {
        // A count-0 requirement is vacuously satisfied regardless of the tree.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();

        let reqs = vec![LegQualityRequirement {
            count: 0,
            predicate: LegPredicate::ContainsPersonalVolume {
                min_personal_volume: 9999.0, // impossible threshold — vacuously satisfied
            },
        }];
        assert!(
            leg_quality_meets(&reqs, uid(1), &tree, &already, &distributors, &ordinals).unwrap()
        );
    }

    #[test]
    fn leg_quality_meets_unranked_node_does_not_match() {
        // An EvaluatedRank::Unranked entry means the node was evaluated but
        // did not qualify for any rank. It must NOT satisfy a ContainsRank
        // predicate — distinct from an absent node, but treated identically.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();

        let mut already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        already.insert(uid(2), EvaluatedRank::Unranked);

        let distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        let mut ordinals: HashMap<String, u16> = HashMap::new();
        ordinals.insert("bronze".to_string(), 1);

        let reqs = vec![LegQualityRequirement {
            count: 1,
            predicate: LegPredicate::ContainsRank {
                min_rank: "bronze".to_string(),
            },
        }];
        // The only leg's only node is Unranked — no matching leg exists.
        assert!(
            !leg_quality_meets(&reqs, uid(1), &tree, &already, &distributors, &ordinals).unwrap()
        );
    }

    use crate::config::rank::{
        DemotionPolicy, RankDefinition, RankQualification, StructureQualification,
    };

    fn rank_with_pv_and_gv(
        name: &str,
        ord: u16,
        structure: &str,
        pv: f64,
        gv: f64,
    ) -> RankDefinition {
        RankDefinition {
            name: name.to_string(),
            ordinal: ord,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: structure.to_string(),
                    personal_volume: pv,
                    group_volume: gv,
                    max_group_volume_per_leg: gv * 10.0, // permissive
                    min_retail_volume: 0.0,
                    distributor_count: None,
                    leg_quality: vec![],
                }],
                required_products: vec![],
            },
            qualified_structures: vec![structure.to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        }
    }

    #[test]
    fn satisfies_passes_when_every_structure_passes_and_products_pass() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();

        let sources = vec![VolumeSource {
            source_id: uid(2),
            cv_amount: 1000.0,
        }];
        let idx = VolumeIndex::build(&sources);

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let primitives = DistributorPrimitives {
            personal_volume: 100.0,
            retail_volume: 0.0,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        };

        let rank = rank_with_pv_and_gv("silver", 2, "Test", 100.0, 500.0);
        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives);

        assert!(
            satisfies(
                &rank,
                uid(1),
                &distributors,
                &nav_map,
                &idx,
                &already,
                &ordinals
            )
            .unwrap()
        );
    }

    #[test]
    fn satisfies_fails_when_any_structure_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]);

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let primitives = primitives_with_pv(50.0); // PV too low

        let rank = rank_with_pv_and_gv("silver", 2, "Test", 100.0, 0.0);
        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives);

        assert!(
            !satisfies(
                &rank,
                uid(1),
                &distributors,
                &nav_map,
                &idx,
                &already,
                &ordinals
            )
            .unwrap()
        );
    }

    #[test]
    fn satisfies_errors_on_unknown_structure() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]);

        let nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new(); // missing "Test"

        let primitives = primitives_with_pv(0.0);
        let rank = rank_with_pv_and_gv("silver", 2, "Test", 0.0, 0.0);
        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives);

        let err = satisfies(
            &rank,
            uid(1),
            &distributors,
            &nav_map,
            &idx,
            &already,
            &ordinals,
        )
        .unwrap_err();
        assert_eq!(
            err,
            crate::rank::types::EvaluationError::UnknownStructure {
                rank: "silver".to_string(),
                structure: "Test".to_string(),
            }
        );
    }

    #[test]
    fn satisfies_errors_on_unknown_min_rank() {
        // Construct a rank whose StructureQualification carries a
        // DistributorCountRequirement referencing a min_rank that is NOT
        // present in `rank_ordinals`. satisfies() must surface
        // EvaluationError::UnknownMinRank with the qualifying rank's name
        // and the referenced rank string.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]);

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let primitives = primitives_with_pv(0.0);

        let mut rank = rank_with_pv_and_gv("silver", 2, "Test", 0.0, 0.0);
        rank.qualification.structures[0].distributor_count = Some(DistributorCountRequirement {
            count: 1,
            min_rank: "phantom_rank".to_string(),
            search_mode: SearchMode::AnyLevel,
            search_depth: None,
            total_count: 0,
            min_leg_group_volume: 0.0,
        });

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new(); // intentionally empty
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives);

        let err = satisfies(
            &rank,
            uid(1),
            &distributors,
            &nav_map,
            &idx,
            &already,
            &ordinals,
        )
        .unwrap_err();
        assert_eq!(
            err,
            EvaluationError::UnknownMinRank {
                rank: "silver".to_string(),
                referenced: "phantom_rank".to_string(),
            }
        );
    }

    #[test]
    fn satisfies_passes_with_satisfied_leg_quality() {
        // Subject 1 has two legs, each a node ranked "bronze".
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(1), uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]);

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(100.0));

        let mut rank = rank_with_pv_and_gv("silver", 2, "Test", 100.0, 0.0);
        rank.qualification.structures[0].leg_quality = vec![LegQualityRequirement {
            count: 2,
            predicate: LegPredicate::ContainsRank {
                min_rank: "bronze".to_string(),
            },
        }];

        let mut already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        already.insert(uid(2), ranked("bronze", 1));
        already.insert(uid(3), ranked("bronze", 1));
        let mut ordinals: HashMap<String, u16> = HashMap::new();
        ordinals.insert("bronze".to_string(), 1);

        assert!(
            satisfies(
                &rank,
                uid(1),
                &distributors,
                &nav_map,
                &idx,
                &already,
                &ordinals
            )
            .unwrap()
        );
    }

    #[test]
    fn satisfies_fails_with_unsatisfied_leg_quality() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]);

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(100.0));

        let mut rank = rank_with_pv_and_gv("silver", 2, "Test", 100.0, 0.0);
        rank.qualification.structures[0].leg_quality = vec![LegQualityRequirement {
            count: 2, // requires 2 bronze legs; only one leg exists
            predicate: LegPredicate::ContainsRank {
                min_rank: "bronze".to_string(),
            },
        }];

        let mut already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        already.insert(uid(2), ranked("bronze", 1));
        let mut ordinals: HashMap<String, u16> = HashMap::new();
        ordinals.insert("bronze".to_string(), 1);

        assert!(
            !satisfies(
                &rank,
                uid(1),
                &distributors,
                &nav_map,
                &idx,
                &already,
                &ordinals
            )
            .unwrap()
        );
    }

    #[test]
    fn satisfies_maps_leg_quality_unknown_min_rank_error() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]);

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(100.0));

        let mut rank = rank_with_pv_and_gv("silver", 2, "Test", 100.0, 0.0);
        rank.qualification.structures[0].leg_quality = vec![LegQualityRequirement {
            count: 1,
            predicate: LegPredicate::ContainsRank {
                min_rank: "phantom_rank".to_string(),
            },
        }];

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();

        let err = satisfies(
            &rank,
            uid(1),
            &distributors,
            &nav_map,
            &idx,
            &already,
            &ordinals,
        )
        .unwrap_err();
        assert_eq!(
            err,
            EvaluationError::UnknownMinRank {
                rank: "silver".to_string(),
                referenced: "phantom_rank".to_string(),
            }
        );
    }
}
