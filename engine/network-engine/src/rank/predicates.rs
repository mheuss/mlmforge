//! Per-criterion predicate evaluators (PV, GV, retail, distributor count, products).

use std::collections::HashMap;

use uuid::Uuid;

use crate::config::rank::{
    DistributorCountRequirement, LegPredicate, LegQualityRequirement, RankDefinition,
    RankQualificationWindow, SearchMode, StructureQualification, TenureRequirement,
};
use crate::decimal::{AuthoredDec, Dec, NumericError};
use crate::rank::evaluator::{EvalCtx, VolumeIndex};
use crate::rank::types::{DistributorPrimitives, EvaluatedRank, EvaluationError};
use crate::tree::error::TreeError;
use crate::tree::navigator::TreeNavigator;

/// Reasons distributor_count evaluation can fail. Caller maps these to
/// `EvaluationError` variants with rank context attached.
#[derive(Debug, PartialEq)]
pub(crate) enum DistributorCountError {
    Tree(TreeError),
    UnknownMinRank(String),
    Numeric(NumericError),
}

impl From<TreeError> for DistributorCountError {
    fn from(e: TreeError) -> Self {
        DistributorCountError::Tree(e)
    }
}

impl From<NumericError> for DistributorCountError {
    fn from(e: NumericError) -> Self {
        DistributorCountError::Numeric(e)
    }
}

/// Reasons group volume evaluation can fail.
#[derive(Debug, PartialEq)]
pub(crate) enum GroupVolumeError {
    Tree(TreeError),
    Numeric(NumericError),
}

impl From<TreeError> for GroupVolumeError {
    fn from(e: TreeError) -> Self {
        GroupVolumeError::Tree(e)
    }
}

impl From<NumericError> for GroupVolumeError {
    fn from(e: NumericError) -> Self {
        GroupVolumeError::Numeric(e)
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
pub(crate) fn pv_meets(required: AuthoredDec, primitives: &DistributorPrimitives) -> bool {
    primitives.personal_volume >= required
}

/// Pass when the distributor's retail volume is at least `required`.
pub(crate) fn retail_meets(required: AuthoredDec, primitives: &DistributorPrimitives) -> bool {
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

/// Return the per-leg cap a structure sets, or None when it sets none.
pub(crate) fn effective_leg_cap(sq: &StructureQualification) -> Result<Option<Dec>, NumericError> {
    if sq.max_group_volume_per_leg > AuthoredDec::ZERO {
        return Ok(Some(sq.max_group_volume_per_leg.dec()));
    }
    let cap = sq
        .max_group_volume_per_leg_fraction
        .dec()
        .try_mul(sq.group_volume)?;
    Ok((cap > Dec::ZERO).then_some(cap))
}

/// Replace each fraction cap in `ranks` with the fixed cap it resolves to.
pub(crate) fn resolve_fraction_caps(ranks: &mut [RankDefinition]) -> Result<(), NumericError> {
    for sq in ranks
        .iter_mut()
        .flat_map(|rank| rank.qualification.structures.iter_mut())
    {
        if sq.max_group_volume_per_leg > AuthoredDec::ZERO {
            continue;
        }
        if let Some(cap) = effective_leg_cap(sq)? {
            sq.max_group_volume_per_leg = AuthoredDec::from_resolved(cap);
            sq.max_group_volume_per_leg_fraction = AuthoredDec::ZERO;
        }
    }
    Ok(())
}

/// Pass when personal volume plus each first-level leg's CV, capped at `cap`, meets `required`.
pub(crate) fn capped_gv_meets(
    required: AuthoredDec,
    cap: Option<Dec>,
    user_id: Uuid,
    tree: &dyn TreeNavigator,
    volume_index: &VolumeIndex,
    primitives: &DistributorPrimitives,
) -> Result<bool, GroupVolumeError> {
    let mut gv = primitives.personal_volume.dec();
    for child in tree.get_children(user_id)? {
        let mut leg = volume_index.cv_for(child.user_id);
        for d in tree.get_downline(child.user_id, 0)? {
            leg = leg.try_add(volume_index.cv_for(d.user_id))?;
        }
        gv = gv.try_add(cap.map_or(leg, |c| leg.min(c)))?;
    }
    Ok(gv >= required.dec())
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
            leg_gv = leg_gv.try_add(volume_index.cv_for(d.user_id))?;
        }
        if leg_gv >= req.min_leg_group_volume.dec() {
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
/// a node that is absent or `Unranked` does not match.
///
/// `ContainsPersonalVolume` reads the node's personal volume from
/// `distributors`. Its `None` arm is a fallback. It does not mean a missing
/// input has no volume.
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
    already: &HashMap<Uuid, EvaluatedRank>,
    ctx: &EvalCtx<'_, '_>,
) -> Result<bool, EvaluationError> {
    // evaluate_ranks only evaluates users present in `ctx.distributors`, so
    // this lookup always succeeds. A subject with no primitives cannot meet
    // any volume or product criterion, so treat the absence as not-qualifying.
    let Some(primitives) = ctx.distributors.get(&user_id) else {
        return Ok(false);
    };

    if !required_products_met(&rank.qualification.required_products, primitives) {
        return Ok(false);
    }

    for sq in &rank.qualification.structures {
        let tree =
            ctx.trees
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
        match capped_gv_meets(
            sq.group_volume,
            effective_leg_cap(sq)?,
            user_id,
            *tree,
            ctx.volume_index,
            primitives,
        ) {
            Ok(true) => {}
            Ok(false) => return Ok(false),
            Err(GroupVolumeError::Tree(_)) => {
                return Err(EvaluationError::DistributorNotInTree(
                    user_id,
                    sq.structure.clone(),
                ));
            }
            Err(GroupVolumeError::Numeric(e)) => return Err(e.into()),
        }
        if let Some(req) = &sq.distributor_count {
            match distributor_count_meets(
                req,
                user_id,
                *tree,
                ctx.volume_index,
                already,
                ctx.rank_ordinals,
            ) {
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
                Err(DistributorCountError::Numeric(e)) => return Err(e.into()),
            }
        }
        match leg_quality_meets(
            &sq.leg_quality,
            user_id,
            *tree,
            already,
            ctx.distributors,
            ctx.rank_ordinals,
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
    // Windowed gate (BR3/BR4): AND-combined with the structure criteria. When
    // the rank declares a window, the distributor's per-period history must
    // satisfy it. Absent window = no-op (BR4). The empty-axis case (BR9) is
    // caught loudly upstream in `evaluate_ranks`, so a non-empty axis here is
    // the only reachable path that can fail the gate.
    if let Some(w) = &rank.qualification.window {
        if !windowed_meets(
            w,
            ctx.history_window,
            ctx.history.get(&user_id),
            ctx.rank_ordinals,
            &rank.name,
        )? {
            return Ok(false);
        }
    }
    // Tenure gate (BR3/BR4): AND-combined with the window and structure
    // criteria. When the rank declares a tenure requirement, the distributor's
    // consecutive per-period history must satisfy it. Absent tenure = no-op
    // (BR4). The empty-axis case (BR9) is caught loudly upstream in
    // `evaluate_ranks`, mirroring the window gate above.
    if let Some(t) = &rank.qualification.tenure {
        if !tenure_meets(
            t,
            ctx.history_window,
            ctx.history.get(&user_id),
            ctx.rank_ordinals,
            &rank.name,
        )? {
            return Ok(false);
        }
    }
    // Reaching here means every gate passed: window AND tenure AND base criteria (BR3).
    Ok(true)
}

/// Look up a threshold rank's ordinal, erroring with the qualifying rank's
/// name (`rank_name`) for context.
fn lookup_threshold(
    threshold: &str,
    rank_ordinals: &HashMap<String, u16>,
    rank_name: &str,
) -> Result<u16, EvaluationError> {
    rank_ordinals
        .get(threshold)
        .copied()
        .ok_or_else(|| EvaluationError::UnknownThresholdRank {
            rank: rank_name.to_string(),
            referenced: threshold.to_string(),
        })
}

/// BR1: achieved >= threshold ordinal in >= N of the M most-recent prior
/// periods. `axis` is the history window (DESC); `hist` is this distributor's
/// per-period achieved ordinals; `rank_name` is the rank being qualified
/// (error context only).
pub(crate) fn windowed_meets(
    w: &RankQualificationWindow,
    axis: &[String],
    hist: Option<&HashMap<String, Option<u16>>>,
    rank_ordinals: &HashMap<String, u16>,
    rank_name: &str,
) -> Result<bool, EvaluationError> {
    let threshold = lookup_threshold(&w.threshold_rank, rank_ordinals, rank_name)?; // BR7/E
    let m = w.window_periods as usize;
    if axis.len() < m {
        return Ok(false); // BR5
    }
    let count = axis[..m]
        .iter()
        .filter(|pid| {
            // missing key OR Unranked (None) both map to false (BR6)
            hist.and_then(|h| h.get(pid.as_str()))
                .copied()
                .flatten()
                .is_some_and(|o| o >= threshold)
        })
        .count();
    Ok(count >= w.qualifying_periods as usize)
}

/// BR2: achieved >= threshold ordinal for X consecutive most-recent prior
/// periods (no gap). `axis` is the history window (DESC); `hist` is this
/// distributor's per-period achieved ordinals; `rank_name` is the rank being
/// qualified (error context only). A gap (missing period row) or an Unranked
/// period (`None`) breaks the streak. An axis shorter than X fails (BR5).
pub(crate) fn tenure_meets(
    t: &TenureRequirement,
    axis: &[String],
    hist: Option<&HashMap<String, Option<u16>>>,
    rank_ordinals: &HashMap<String, u16>,
    rank_name: &str,
) -> Result<bool, EvaluationError> {
    let threshold = lookup_threshold(&t.threshold_rank, rank_ordinals, rank_name)?;
    let x = t.periods as usize;
    if axis.len() < x {
        return Ok(false); // BR5
    }
    let all = axis[..x].iter().all(|pid| {
        // missing key OR Unranked (None) both break the streak
        hist.and_then(|h| h.get(pid.as_str()))
            .copied()
            .flatten()
            .is_some_and(|o| o >= threshold)
    });
    Ok(all)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::types::VolumeSource;
    use crate::d;
    use crate::decimal::{AuthoredDec, Dec};
    use crate::rank::evaluator::VolumeIndex;
    use crate::rank::types::DistributorPrimitives;
    use crate::tree::error::TreeError;
    use crate::tree::unilevel::UnilevelTree;
    use proptest::prelude::*;
    use uuid::Uuid;

    fn uid(i: u128) -> Uuid {
        Uuid::from_u128(i)
    }

    fn primitives_with_pv(pv: AuthoredDec) -> DistributorPrimitives {
        DistributorPrimitives {
            personal_volume: pv,
            retail_volume: d!(0.0),
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        }
    }

    #[test]
    fn pv_meets_returns_true_when_pv_at_or_above_threshold() {
        assert!(pv_meets(d!(100.0), &primitives_with_pv(d!(100.0))));
        assert!(pv_meets(d!(100.0), &primitives_with_pv(d!(150.0))));
    }

    #[test]
    fn pv_meets_returns_false_when_pv_below_threshold() {
        assert!(!pv_meets(d!(100.0), &primitives_with_pv(d!(99.99))));
        assert!(!pv_meets(d!(100.0), &primitives_with_pv(d!(0.0))));
    }

    fn primitives_with_retail(retail: AuthoredDec) -> DistributorPrimitives {
        DistributorPrimitives {
            personal_volume: d!(0.0),
            retail_volume: retail,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        }
    }

    #[test]
    fn retail_meets_returns_true_when_at_or_above_threshold() {
        assert!(retail_meets(d!(50.0), &primitives_with_retail(d!(50.0))));
        assert!(retail_meets(d!(50.0), &primitives_with_retail(d!(75.0))));
    }

    #[test]
    fn retail_meets_returns_false_when_below_threshold() {
        assert!(!retail_meets(d!(50.0), &primitives_with_retail(d!(49.99))));
    }

    fn primitives_with_products(products: Vec<&str>) -> DistributorPrimitives {
        DistributorPrimitives {
            personal_volume: d!(0.0),
            retail_volume: d!(0.0),
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

    /// A root `uid(1)` with one first-level child per entry in `legs`, each carrying that CV.
    fn root_with_legs(legs: &[AuthoredDec]) -> (UnilevelTree, VolumeIndex) {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let mut sources = vec![];
        for (i, cv) in legs.iter().enumerate() {
            let child = uid(10 + i as u128);
            tree.add_node(child, uid(1), uid(1), 0).unwrap();
            sources.push(VolumeSource {
                source_id: child,
                cv_amount: *cv,
            });
        }
        (tree, VolumeIndex::build(&sources).unwrap())
    }

    fn sq_with_caps(
        group_volume: AuthoredDec,
        cap: AuthoredDec,
        fraction: AuthoredDec,
    ) -> StructureQualification {
        StructureQualification {
            structure: "Test".to_string(),
            personal_volume: d!(0.0),
            group_volume,
            max_group_volume_per_leg: cap,
            max_group_volume_per_leg_fraction: fraction,
            min_retail_volume: d!(0.0),
            distributor_count: None,
            leg_quality: vec![],
        }
    }

    #[test]
    fn effective_leg_cap_of_twenty_nine_hundredths_of_one_hundred_thousand_is_exact() {
        let sq = sq_with_caps(d!(100000), d!(0), d!(0.29));
        assert_eq!(effective_leg_cap(&sq), Ok(Some(d!(29000))));
    }

    #[test]
    fn effective_leg_cap_returns_the_fixed_cap_when_set() {
        assert_eq!(
            effective_leg_cap(&sq_with_caps(d!(10000.0), d!(6000.0), d!(0.0))),
            Ok(Some(d!(6000.0)))
        );
    }

    #[test]
    fn effective_leg_cap_returns_the_fraction_of_group_volume() {
        assert_eq!(
            effective_leg_cap(&sq_with_caps(d!(10000.0), d!(0.0), d!(0.6))),
            Ok(Some(d!(6000.0)))
        );
    }

    #[test]
    fn effective_leg_cap_returns_none_for_a_fraction_with_zero_group_volume() {
        assert_eq!(
            effective_leg_cap(&sq_with_caps(d!(0.0), d!(0.0), d!(0.5))),
            Ok(None)
        );
    }

    #[test]
    fn effective_leg_cap_returns_none_when_neither_cap_is_set() {
        assert_eq!(
            effective_leg_cap(&sq_with_caps(d!(10000.0), d!(0.0), d!(0.0))),
            Ok(None)
        );
    }

    #[test]
    fn effective_leg_cap_gives_the_exact_cap_for_a_fraction_that_rounds_high() {
        assert_eq!(
            effective_leg_cap(&sq_with_caps(d!(10000.0), d!(0.0), d!(0.07))),
            Ok(Some(d!(700.0)))
        );
    }

    #[test]
    fn effective_leg_cap_gives_the_exact_cap_for_a_cent_valued_group_volume() {
        assert_eq!(
            effective_leg_cap(&sq_with_caps(d!(1234.56), d!(0.0), d!(0.09))),
            Ok(Some(d!(111.1104)))
        );
    }

    #[test]
    fn effective_leg_cap_gives_the_exact_cap_for_a_basis_point_fraction() {
        assert_eq!(
            effective_leg_cap(&sq_with_caps(d!(100.0), d!(0.0), d!(0.0078))),
            Ok(Some(d!(0.78)))
        );
    }

    #[test]
    fn effective_leg_cap_gives_the_exact_cap_for_a_tiny_fraction() {
        assert_eq!(
            effective_leg_cap(&sq_with_caps(d!(100.0), d!(0.0), d!(1e-9))),
            Ok(Some(d!(1e-7)))
        );
    }

    #[test]
    fn effective_leg_cap_gives_the_exact_cap_for_a_fraction_with_more_than_six_decimals() {
        let cap = effective_leg_cap(&sq_with_caps(d!(100.0), d!(0.0), d!(0.333333334))).unwrap();
        assert_eq!(cap, Some(d!(33.3333334)));

        let (tree, idx) = root_with_legs(&[d!(33.34), d!(33.34), d!(33.34)]);
        assert!(
            capped_gv_meets(
                d!(100.0),
                cap,
                uid(1),
                &tree,
                &idx,
                &primitives_with_pv(d!(0.0))
            )
            .unwrap()
        );
    }

    #[test]
    fn effective_leg_cap_gives_the_exact_cap_for_a_seventh_decimal_on_a_large_cap() {
        let cap = effective_leg_cap(&sq_with_caps(d!(60000000.0000001), d!(0.0), d!(1.0))).unwrap();
        assert_eq!(cap, Some(d!(60000000.0000001)));

        let (tree, idx) = root_with_legs(&[d!(60000000.0000001)]);
        assert!(
            capped_gv_meets(
                d!(60000000.0000001),
                cap,
                uid(1),
                &tree,
                &idx,
                &primitives_with_pv(d!(0.0))
            )
            .unwrap()
        );
    }

    #[test]
    fn effective_leg_cap_gives_the_exact_cap_for_an_eighth_decimal_on_a_mid_sized_cap() {
        let cap =
            effective_leg_cap(&sq_with_caps(d!(29000000.00000004), d!(0.0), d!(1.0))).unwrap();
        assert_eq!(cap, Some(d!(29000000.00000004)));

        let (tree, idx) = root_with_legs(&[d!(29000000.00000004)]);
        assert!(
            capped_gv_meets(
                d!(29000000.00000004),
                cap,
                uid(1),
                &tree,
                &idx,
                &primitives_with_pv(d!(0.0))
            )
            .unwrap()
        );
    }

    #[test]
    fn effective_leg_cap_gives_the_exact_cap_for_a_ninth_decimal_fraction() {
        let cap =
            effective_leg_cap(&sq_with_caps(d!(2000001.0), d!(0.0), d!(0.500000001))).unwrap();
        assert_eq!(cap, Some(d!(1000000.502000001)));

        let (tree, idx) = root_with_legs(&[d!(1000000.5020000009)]);
        let primitives = primitives_with_pv(d!(1000000.4979999991));
        assert!(capped_gv_meets(d!(2000001.0), cap, uid(1), &tree, &idx, &primitives).unwrap());
    }

    #[test]
    fn effective_leg_cap_gives_the_exact_cap_for_a_fraction_that_rounds_high_on_a_large_cap() {
        assert_eq!(
            effective_leg_cap(&sq_with_caps(d!(1.4e9), d!(0.0), d!(0.07))),
            Ok(Some(d!(98000000.0)))
        );
    }

    #[test]
    fn capped_gv_meets_sums_downline_cv_plus_own_pv_with_no_cap() {
        // Build chain: 1 -> 2 -> 3
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(2), uid(2), 0).unwrap();
        let idx = VolumeIndex::build(&[
            VolumeSource {
                source_id: uid(2),
                cv_amount: d!(100.0),
            },
            VolumeSource {
                source_id: uid(3),
                cv_amount: d!(200.0),
            },
        ])
        .unwrap();
        let primitives = primitives_with_pv(d!(50.0));

        assert!(capped_gv_meets(d!(350.0), None, uid(1), &tree, &idx, &primitives).unwrap());
        assert!(!capped_gv_meets(d!(350.01), None, uid(1), &tree, &idx, &primitives).unwrap());
    }

    #[test]
    fn capped_gv_meets_counts_a_strong_leg_up_to_the_cap() {
        let (tree, idx) = root_with_legs(&[d!(7000.0), d!(4000.0)]);
        let primitives = primitives_with_pv(d!(0.0));

        assert!(
            capped_gv_meets(
                d!(10000.0),
                Some(d!(6000.0)),
                uid(1),
                &tree,
                &idx,
                &primitives
            )
            .unwrap()
        );
        assert!(
            !capped_gv_meets(
                d!(10000.0),
                Some(d!(5000.0)),
                uid(1),
                &tree,
                &idx,
                &primitives
            )
            .unwrap()
        );
    }

    #[test]
    fn capped_gv_meets_caps_every_leg() {
        let (tree, idx) = root_with_legs(&[d!(300.0), d!(400.0), d!(500.0)]);
        let primitives = primitives_with_pv(d!(0.0));

        assert!(
            capped_gv_meets(d!(600.0), Some(d!(200.0)), uid(1), &tree, &idx, &primitives).unwrap()
        );
        assert!(
            !capped_gv_meets(
                d!(600.01),
                Some(d!(200.0)),
                uid(1),
                &tree,
                &idx,
                &primitives
            )
            .unwrap()
        );
    }

    #[test]
    fn capped_gv_meets_caps_a_leg_subtree_as_a_whole() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap(); // leg A
        tree.add_node(uid(3), uid(1), uid(1), 0).unwrap(); // leg B
        tree.add_node(uid(4), uid(2), uid(2), 0).unwrap(); // child of leg A
        let idx = VolumeIndex::build(&[
            VolumeSource {
                source_id: uid(2),
                cv_amount: d!(50.0),
            },
            VolumeSource {
                source_id: uid(4),
                cv_amount: d!(150.0),
            }, // leg A: 200, capped to 120
            VolumeSource {
                source_id: uid(3),
                cv_amount: d!(100.0),
            }, // leg B: 100
        ])
        .unwrap();
        let primitives = primitives_with_pv(d!(0.0));

        assert!(
            capped_gv_meets(d!(220.0), Some(d!(120.0)), uid(1), &tree, &idx, &primitives).unwrap()
        );
        assert!(
            !capped_gv_meets(
                d!(220.01),
                Some(d!(120.0)),
                uid(1),
                &tree,
                &idx,
                &primitives
            )
            .unwrap()
        );
    }

    #[test]
    fn capped_gv_meets_does_not_cap_personal_volume() {
        let (tree, idx) = root_with_legs(&[d!(500.0)]);
        let primitives = primitives_with_pv(d!(900.0));

        assert!(
            capped_gv_meets(
                d!(1000.0),
                Some(d!(100.0)),
                uid(1),
                &tree,
                &idx,
                &primitives
            )
            .unwrap()
        );
        assert!(
            !capped_gv_meets(
                d!(1000.01),
                Some(d!(100.0)),
                uid(1),
                &tree,
                &idx,
                &primitives
            )
            .unwrap()
        );
    }

    #[test]
    fn capped_gv_meets_passes_on_personal_volume_with_no_children() {
        let (tree, idx) = root_with_legs(&[]);
        let primitives = primitives_with_pv(d!(100.0));

        assert!(
            capped_gv_meets(d!(100.0), Some(d!(50.0)), uid(1), &tree, &idx, &primitives).unwrap()
        );
    }

    #[test]
    fn capped_gv_meets_errors_when_distributor_missing_from_tree() {
        let tree = UnilevelTree::new();
        let idx = VolumeIndex::build(&[]).unwrap();
        let primitives = primitives_with_pv(d!(0.0));

        let err = capped_gv_meets(d!(0.0), None, uid(99), &tree, &idx, &primitives).unwrap_err();
        assert_eq!(
            err,
            GroupVolumeError::Tree(TreeError::UserNotFound(uid(99)))
        );
    }

    #[test]
    fn capped_gv_meets_ignores_upline_volume_and_own_cv() {
        // Build chain: 1 -> 2 -> 3, evaluating 2.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(2), uid(2), 0).unwrap();
        let idx = VolumeIndex::build(&[
            VolumeSource {
                source_id: uid(1),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: uid(2),
                cv_amount: d!(300.0),
            },
            VolumeSource {
                source_id: uid(3),
                cv_amount: d!(100.0),
            },
        ])
        .unwrap();
        let primitives = primitives_with_pv(d!(50.0));

        for cap in [None, Some(d!(1000.0))] {
            assert!(capped_gv_meets(d!(150.0), cap, uid(2), &tree, &idx, &primitives).unwrap());
            assert!(!capped_gv_meets(d!(150.01), cap, uid(2), &tree, &idx, &primitives).unwrap());
        }
    }

    #[test]
    fn capped_gv_meets_treats_a_fraction_cap_like_its_fixed_cap() {
        let (tree, idx) = root_with_legs(&[d!(29000.0), d!(29000.0), d!(29000.0), d!(13000.0)]);
        let primitives = primitives_with_pv(d!(0.0));
        let fixed_cap =
            effective_leg_cap(&sq_with_caps(d!(100000.0), d!(29000.0), d!(0.0))).unwrap();
        let fraction_cap =
            effective_leg_cap(&sq_with_caps(d!(100000.0), d!(0.0), d!(0.29))).unwrap();

        assert!(
            capped_gv_meets(d!(100000.0), fixed_cap, uid(1), &tree, &idx, &primitives).unwrap()
        );
        assert!(
            capped_gv_meets(d!(100000.0), fraction_cap, uid(1), &tree, &idx, &primitives).unwrap()
        );
    }

    fn whole<T: crate::test_support::FromLiteral>(v: u32) -> T {
        T::from_literal(&v.to_string())
    }

    proptest! {
        /// Capped group volume equals personal volume plus each leg's CV capped, summed as integers.
        #[test]
        fn prop_capped_gv_matches_an_integer_oracle(
            pv in 0u32..1_000,
            legs in proptest::collection::vec(0u32..10_000, 0..6),
            cap in proptest::option::of(1u32..10_000),
            required in 0u32..40_000,
        ) {
            let leg_cvs: Vec<AuthoredDec> = legs.iter().map(|v| whole(*v)).collect();
            let (tree, idx) = root_with_legs(&leg_cvs);
            let oracle = u64::from(pv)
                + legs
                    .iter()
                    .map(|&v| u64::from(cap.map_or(v, |c| v.min(c))))
                    .sum::<u64>();
            let got = capped_gv_meets(
                whole(required),
                cap.map(whole),
                uid(1),
                &tree,
                &idx,
                &primitives_with_pv(whole(pv)),
            )
            .unwrap();
            prop_assert_eq!(got, oracle >= u64::from(required));
        }

        /// Raising the cap never turns a pass into a fail.
        #[test]
        fn prop_capped_gv_is_monotone_in_the_cap(
            legs in proptest::collection::vec(0u32..10_000, 0..6),
            low in 1u32..10_000,
            raise in 0u32..10_000,
            required in 0u32..40_000,
        ) {
            let leg_cvs: Vec<AuthoredDec> = legs.iter().map(|v| whole(*v)).collect();
            let (tree, idx) = root_with_legs(&leg_cvs);
            let primitives = primitives_with_pv(d!(0.0));
            let required = whole(required);
            let low_pass =
                capped_gv_meets(required, Some(whole(low)), uid(1), &tree, &idx, &primitives).unwrap();
            let high_pass = capped_gv_meets(
                required,
                Some(whole(low + raise)),
                uid(1),
                &tree,
                &idx,
                &primitives,
            )
            .unwrap();
            prop_assert!(!low_pass || high_pass);
        }
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

        let mut cv_per: HashMap<Uuid, AuthoredDec> = HashMap::new();
        cv_per.insert(uid(2), d!(600.0));
        cv_per.insert(uid(3), d!(600.0));
        cv_per.insert(uid(4), d!(600.0));
        let sources: Vec<VolumeSource> = cv_per
            .into_iter()
            .map(|(id, cv)| VolumeSource {
                source_id: id,
                cv_amount: cv,
            })
            .collect();
        let idx = VolumeIndex::build(&sources).unwrap();

        let req = DistributorCountRequirement {
            count: 2,
            min_rank: "bronze".to_string(),
            search_mode: SearchMode::FirstLevels,
            search_depth: Some(1),
            total_count: 3,
            min_leg_group_volume: d!(500.0),
        };

        assert!(distributor_count_meets(&req, uid(1), &tree, &idx, &already, &ordinals).unwrap());
    }

    #[test]
    fn distributor_count_meets_unknown_min_rank_errors() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        let idx = VolumeIndex::build(&[]).unwrap();

        let req = DistributorCountRequirement {
            count: 1,
            min_rank: "unknown".to_string(),
            search_mode: SearchMode::AnyLevel,
            search_depth: None,
            total_count: 0,
            min_leg_group_volume: d!(0.0),
        };

        let err =
            distributor_count_meets(&req, uid(1), &tree, &idx, &already, &ordinals).unwrap_err();
        assert_eq!(
            err,
            DistributorCountError::UnknownMinRank("unknown".to_string())
        );
    }

    #[test]
    fn distributor_count_meets_refuses_a_leg_volume_it_cannot_hold() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        let mut sources = Vec::new();
        for i in 3..8_003 {
            tree.add_node(uid(i), uid(2), uid(2), 0).unwrap();
            sources.push(VolumeSource {
                source_id: uid(i),
                cv_amount: d!(999_999_999_999_999.999_999_999_9),
            });
        }
        let idx = VolumeIndex::build(&sources).unwrap();
        let already = HashMap::from([(
            uid(2),
            EvaluatedRank::Qualified {
                rank: "bronze".to_string(),
                ordinal: 1,
            },
        )]);
        let ordinals = HashMap::from([("bronze".to_string(), 1u16)]);
        let req = DistributorCountRequirement {
            count: 1,
            min_rank: "bronze".to_string(),
            search_mode: SearchMode::AnyLevel,
            search_depth: None,
            total_count: 0,
            min_leg_group_volume: d!(0.0),
        };

        let err =
            distributor_count_meets(&req, uid(1), &tree, &idx, &already, &ordinals).unwrap_err();
        assert!(
            matches!(err, DistributorCountError::Numeric(ref e) if e.op == crate::decimal::NumericOp::Add),
            "got {err:?}"
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
        distributors.insert(uid(2), primitives_with_pv(d!(200.0)));
        distributors.insert(uid(3), primitives_with_pv(d!(50.0)));
        let ordinals: HashMap<String, u16> = HashMap::new();

        let reqs = vec![LegQualityRequirement {
            count: 1,
            predicate: LegPredicate::ContainsPersonalVolume {
                min_personal_volume: d!(200.0),
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
        distributors.insert(uid(2), primitives_with_pv(d!(500.0)));
        distributors.insert(uid(3), primitives_with_pv(d!(500.0)));
        let ordinals: HashMap<String, u16> = HashMap::new();

        let reqs = vec![LegQualityRequirement {
            count: 3, // requires 3 legs but only 2 exist
            predicate: LegPredicate::ContainsPersonalVolume {
                min_personal_volume: d!(100.0),
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
                min_personal_volume: d!(9999.0), // impossible threshold — vacuously satisfied
            },
        }];
        assert!(
            leg_quality_meets(&reqs, uid(1), &tree, &already, &distributors, &ordinals).unwrap()
        );
    }

    #[test]
    fn leg_quality_meets_unranked_node_does_not_match() {
        // An EvaluatedRank::Unranked entry must NOT satisfy a ContainsRank
        // predicate.
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
        pv: AuthoredDec,
        gv: AuthoredDec,
    ) -> RankDefinition {
        let ten: Dec = d!(10);
        RankDefinition {
            name: name.to_string(),
            ordinal: ord,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: structure.to_string(),
                    personal_volume: pv,
                    group_volume: gv,
                    max_group_volume_per_leg: AuthoredDec::from_resolved(
                        gv.dec().try_mul(ten).unwrap(),
                    ), // permissive
                    max_group_volume_per_leg_fraction: d!(0.0),
                    min_retail_volume: d!(0.0),
                    distributor_count: None,
                    leg_quality: vec![],
                }],
                required_products: vec![],
                window: None,
                tenure: None,
            },
            qualified_structures: vec![structure.to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        }
    }

    fn rank_with_fraction_cap(gv: AuthoredDec, fraction: AuthoredDec) -> RankDefinition {
        let mut rank = rank_with_pv_and_gv("silver", 1, "Test", d!(0.0), gv);
        let sq = &mut rank.qualification.structures[0];
        sq.max_group_volume_per_leg = d!(0.0);
        sq.max_group_volume_per_leg_fraction = fraction;
        rank
    }

    fn caps(rank: &RankDefinition) -> (AuthoredDec, AuthoredDec) {
        let sq = &rank.qualification.structures[0];
        (
            sq.max_group_volume_per_leg,
            sq.max_group_volume_per_leg_fraction,
        )
    }

    #[test]
    fn resolve_fraction_caps_turns_a_fraction_cap_into_its_fixed_cap() {
        let mut ranks = vec![rank_with_fraction_cap(d!(100000.0), d!(0.29))];
        resolve_fraction_caps(&mut ranks).unwrap();
        assert_eq!(caps(&ranks[0]), (d!(29000.0), d!(0.0)));
    }

    #[test]
    fn resolve_fraction_caps_leaves_a_fixed_cap_alone() {
        let mut ranks = vec![rank_with_pv_and_gv("gold", 2, "Test", d!(0.0), d!(5000.0))];
        resolve_fraction_caps(&mut ranks).unwrap();
        assert_eq!(caps(&ranks[0]), (d!(50000.0), d!(0.0)));
    }

    #[test]
    fn resolve_fraction_caps_leaves_a_fraction_that_gives_no_cap_alone() {
        let mut ranks = vec![rank_with_fraction_cap(d!(0.0), d!(0.5))];
        resolve_fraction_caps(&mut ranks).unwrap();
        assert_eq!(caps(&ranks[0]), (d!(0.0), d!(0.5)));
        assert_eq!(
            effective_leg_cap(&ranks[0].qualification.structures[0]),
            Ok(None)
        );
    }

    #[test]
    fn satisfies_passes_when_every_structure_passes_and_products_pass() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();

        let sources = vec![VolumeSource {
            source_id: uid(2),
            cv_amount: d!(1000.0),
        }];
        let idx = VolumeIndex::build(&sources).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let primitives = DistributorPrimitives {
            personal_volume: d!(100.0),
            retail_volume: d!(0.0),
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        };

        let rank = rank_with_pv_and_gv("silver", 2, "Test", d!(100.0), d!(500.0));
        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives);

        let ranks: Vec<RankDefinition> = vec![];
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };

        assert!(satisfies(&rank, uid(1), &already, &ctx).unwrap());
    }

    #[test]
    fn satisfies_fails_when_any_structure_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let primitives = primitives_with_pv(d!(50.0)); // PV too low

        let rank = rank_with_pv_and_gv("silver", 2, "Test", d!(100.0), d!(0.0));
        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives);

        let ranks: Vec<RankDefinition> = vec![];
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };

        assert!(!satisfies(&rank, uid(1), &already, &ctx).unwrap());
    }

    #[test]
    fn satisfies_counts_a_strong_leg_up_to_the_per_leg_cap() {
        let (tree, idx) = root_with_legs(&[d!(7000.0), d!(4000.0)]);
        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(d!(0.0)));
        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        let ranks: Vec<RankDefinition> = vec![];
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };

        for (cap, fraction, qualifies) in [
            (d!(6000.0), d!(0.0), true),
            (d!(5000.0), d!(0.0), false),
            (d!(0.0), d!(0.6), true),
            (d!(0.0), d!(0.5), false),
        ] {
            let mut rank = rank_with_pv_and_gv("silver", 2, "Test", d!(0.0), d!(10000.0));
            let sq = &mut rank.qualification.structures[0];
            sq.max_group_volume_per_leg = cap;
            sq.max_group_volume_per_leg_fraction = fraction;
            assert_eq!(
                satisfies(&rank, uid(1), &already, &ctx).unwrap(),
                qualifies,
                "cap {cap}, fraction {fraction}"
            );
        }
    }

    #[test]
    fn satisfies_errors_on_unknown_structure() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]).unwrap();

        let nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new(); // missing "Test"

        let primitives = primitives_with_pv(d!(0.0));
        let rank = rank_with_pv_and_gv("silver", 2, "Test", d!(0.0), d!(0.0));
        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives);

        let ranks: Vec<RankDefinition> = vec![];
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };

        let err = satisfies(&rank, uid(1), &already, &ctx).unwrap_err();
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
        let idx = VolumeIndex::build(&[]).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let primitives = primitives_with_pv(d!(0.0));

        let mut rank = rank_with_pv_and_gv("silver", 2, "Test", d!(0.0), d!(0.0));
        rank.qualification.structures[0].distributor_count = Some(DistributorCountRequirement {
            count: 1,
            min_rank: "phantom_rank".to_string(),
            search_mode: SearchMode::AnyLevel,
            search_depth: None,
            total_count: 0,
            min_leg_group_volume: d!(0.0),
        });

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new(); // intentionally empty
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives);

        let ranks: Vec<RankDefinition> = vec![];
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };

        let err = satisfies(&rank, uid(1), &already, &ctx).unwrap_err();
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
        let idx = VolumeIndex::build(&[]).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(d!(100.0)));

        let mut rank = rank_with_pv_and_gv("silver", 2, "Test", d!(100.0), d!(0.0));
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

        let ranks: Vec<RankDefinition> = vec![];
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };

        assert!(satisfies(&rank, uid(1), &already, &ctx).unwrap());
    }

    #[test]
    fn satisfies_fails_with_unsatisfied_leg_quality() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(d!(100.0)));

        let mut rank = rank_with_pv_and_gv("silver", 2, "Test", d!(100.0), d!(0.0));
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

        let ranks: Vec<RankDefinition> = vec![];
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };

        assert!(!satisfies(&rank, uid(1), &already, &ctx).unwrap());
    }

    #[test]
    fn satisfies_maps_leg_quality_unknown_min_rank_error() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(d!(100.0)));

        let mut rank = rank_with_pv_and_gv("silver", 2, "Test", d!(100.0), d!(0.0));
        rank.qualification.structures[0].leg_quality = vec![LegQualityRequirement {
            count: 1,
            predicate: LegPredicate::ContainsRank {
                min_rank: "phantom_rank".to_string(),
            },
        }];

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals: HashMap<String, u16> = HashMap::new();

        let ranks: Vec<RankDefinition> = vec![];
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };

        let err = satisfies(&rank, uid(1), &already, &ctx).unwrap_err();
        assert_eq!(
            err,
            EvaluationError::UnknownMinRank {
                rank: "silver".to_string(),
                referenced: "phantom_rank".to_string(),
            }
        );
    }

    fn ords() -> HashMap<String, u16> {
        HashMap::from([
            ("Associate".into(), 1),
            ("Director".into(), 5),
            ("QD".into(), 6),
        ])
    }
    fn win(n: u8, m: u8) -> RankQualificationWindow {
        RankQualificationWindow {
            threshold_rank: "Director".into(),
            qualifying_periods: n,
            window_periods: m,
        }
    }

    #[test]
    fn windowed_meets_counts_at_or_above_threshold() {
        let axis: Vec<String> = (0..12).rev().map(|i| format!("2026-{i:02}")).collect();
        let mut h = HashMap::new();
        for (i, p) in axis.iter().enumerate() {
            h.insert(p.clone(), if i < 6 { Some(5) } else { Some(1) });
        }
        assert!(windowed_meets(&win(6, 12), &axis, Some(&h), &ords(), "QD").unwrap());
        assert!(!windowed_meets(&win(7, 12), &axis, Some(&h), &ords(), "QD").unwrap());
    }

    #[test]
    fn windowed_meets_missing_and_unranked_count_as_below() {
        let axis = vec!["2026-05".to_string(), "2026-04".to_string()];
        let h = HashMap::from([("2026-05".to_string(), None)]); // Unranked; 2026-04 missing
        assert!(!windowed_meets(&win(1, 2), &axis, Some(&h), &ords(), "QD").unwrap());
    }

    #[test]
    fn windowed_meets_insufficient_axis_fails() {
        let axis = vec!["2026-05".to_string()];
        assert!(!windowed_meets(&win(1, 12), &axis, None, &ords(), "QD").unwrap());
    }

    #[test]
    fn windowed_meets_unknown_threshold_errors_with_qualifying_rank() {
        let axis: Vec<String> = (0..12).map(|i| format!("2026-{i:02}")).collect();
        let bad = RankQualificationWindow {
            threshold_rank: "Ghost".into(),
            qualifying_periods: 1,
            window_periods: 12,
        };
        let err = windowed_meets(&bad, &axis, None, &ords(), "QD").unwrap_err();
        assert_eq!(
            err,
            EvaluationError::UnknownThresholdRank {
                rank: "QD".into(),
                referenced: "Ghost".into()
            }
        );
    }

    fn ten(x: u8) -> TenureRequirement {
        TenureRequirement {
            threshold_rank: "Director".into(),
            periods: x,
        }
    }

    #[test]
    fn tenure_meets_requires_consecutive_at_or_above() {
        let axis = vec!["2026-05".into(), "2026-04".into(), "2026-03".into()];
        let pass = HashMap::from([
            ("2026-05".into(), Some(5)),
            ("2026-04".into(), Some(6)),
            ("2026-03".into(), Some(5)),
        ]);
        assert!(tenure_meets(&ten(3), &axis, Some(&pass), &ords(), "QD").unwrap());
        let gap = HashMap::from([("2026-05".into(), Some(5)), ("2026-03".into(), Some(5))]); // 2026-04 missing
        assert!(!tenure_meets(&ten(3), &axis, Some(&gap), &ords(), "QD").unwrap());
        let unr = HashMap::from([
            ("2026-05".into(), Some(5)),
            ("2026-04".into(), None),
            ("2026-03".into(), Some(5)),
        ]);
        assert!(!tenure_meets(&ten(3), &axis, Some(&unr), &ords(), "QD").unwrap());
    }

    #[test]
    fn tenure_meets_insufficient_axis_fails() {
        let axis = vec!["2026-05".into()];
        assert!(!tenure_meets(&ten(4), &axis, None, &ords(), "QD").unwrap());
    }

    #[test]
    fn satisfies_fails_when_window_gate_unmet() {
        // Base criteria pass (PV 100 >= 100, trivial GV) but the windowed gate
        // requires Director in 2 of the last 3 periods and the distributor has
        // no qualifying history, so the rank is not satisfied.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(d!(100.0)));

        let mut rank = rank_with_pv_and_gv("QD", 6, "Test", d!(100.0), d!(0.0));
        rank.qualification.window = Some(win(2, 3));

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals = ords();

        let ranks: Vec<RankDefinition> = vec![];
        let axis = vec![
            "2026-05".to_string(),
            "2026-04".to_string(),
            "2026-03".to_string(),
        ];
        // No history rows for this distributor: every axis period gates as below.
        let history: HashMap<Uuid, HashMap<String, Option<u16>>> = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &axis,
            history: &history,
        };

        assert!(!satisfies(&rank, uid(1), &already, &ctx).unwrap());
    }

    #[test]
    fn satisfies_passes_when_window_gate_met() {
        // Same base criteria, but the distributor achieved Director (ordinal 5)
        // in 2 of the last 3 periods, satisfying the windowed gate.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(d!(100.0)));

        let mut rank = rank_with_pv_and_gv("QD", 6, "Test", d!(100.0), d!(0.0));
        rank.qualification.window = Some(win(2, 3));

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals = ords();

        let ranks: Vec<RankDefinition> = vec![];
        let axis = vec![
            "2026-05".to_string(),
            "2026-04".to_string(),
            "2026-03".to_string(),
        ];
        let mut hist: HashMap<String, Option<u16>> = HashMap::new();
        hist.insert("2026-05".to_string(), Some(5)); // Director
        hist.insert("2026-04".to_string(), Some(5)); // Director
        hist.insert("2026-03".to_string(), Some(1)); // Associate (below)
        let mut history: HashMap<Uuid, HashMap<String, Option<u16>>> = HashMap::new();
        history.insert(uid(1), hist);
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &axis,
            history: &history,
        };

        assert!(satisfies(&rank, uid(1), &already, &ctx).unwrap());
    }

    #[test]
    fn satisfies_fails_when_tenure_gate_unmet() {
        // Base criteria pass (PV 100 >= 100, trivial GV) but the tenure gate
        // requires Director for 3 consecutive periods and the distributor has
        // no qualifying history, so the rank is not satisfied.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(d!(100.0)));

        let mut rank = rank_with_pv_and_gv("QD", 6, "Test", d!(100.0), d!(0.0));
        rank.qualification.tenure = Some(ten(3));

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals = ords();

        let ranks: Vec<RankDefinition> = vec![];
        let axis = vec![
            "2026-05".to_string(),
            "2026-04".to_string(),
            "2026-03".to_string(),
        ];
        // No history rows for this distributor: every axis period breaks the streak.
        let history: HashMap<Uuid, HashMap<String, Option<u16>>> = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &axis,
            history: &history,
        };

        assert!(!satisfies(&rank, uid(1), &already, &ctx).unwrap());
    }

    #[test]
    fn satisfies_passes_when_tenure_gate_met() {
        // Same base criteria, but the distributor achieved Director (ordinal 5)
        // or above for 3 consecutive periods, satisfying the tenure gate.
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        let idx = VolumeIndex::build(&[]).unwrap();

        let mut nav_map: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav_map.insert("Test".to_string(), &tree);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(uid(1), primitives_with_pv(d!(100.0)));

        let mut rank = rank_with_pv_and_gv("QD", 6, "Test", d!(100.0), d!(0.0));
        rank.qualification.tenure = Some(ten(3));

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let ordinals = ords();

        let ranks: Vec<RankDefinition> = vec![];
        let axis = vec![
            "2026-05".to_string(),
            "2026-04".to_string(),
            "2026-03".to_string(),
        ];
        let mut hist: HashMap<String, Option<u16>> = HashMap::new();
        hist.insert("2026-05".to_string(), Some(5)); // Director
        hist.insert("2026-04".to_string(), Some(6)); // QD (above)
        hist.insert("2026-03".to_string(), Some(5)); // Director
        let mut history: HashMap<Uuid, HashMap<String, Option<u16>>> = HashMap::new();
        history.insert(uid(1), hist);
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav_map,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &axis,
            history: &history,
        };

        assert!(satisfies(&rank, uid(1), &already, &ctx).unwrap());
    }

    #[test]
    fn capped_gv_meets_counts_ten_ten_plus_twenty_twenty_as_thirty_thirty() {
        let (tree, idx) = root_with_legs(&[d!(10.10), d!(20.20)]);
        let primitives = primitives_with_pv(d!(0));
        assert!(capped_gv_meets(d!(30.30), None, uid(1), &tree, &idx, &primitives).unwrap());
    }

    #[test]
    fn distributor_count_meets_counts_a_leg_of_ten_ten_plus_twenty_twenty_as_thirty_thirty() {
        let mut tree = UnilevelTree::new();
        tree.add_root(uid(1), 0).unwrap();
        tree.add_node(uid(2), uid(1), uid(1), 0).unwrap();
        tree.add_node(uid(3), uid(2), uid(2), 0).unwrap();
        let already = HashMap::from([(
            uid(2),
            EvaluatedRank::Qualified {
                rank: "bronze".to_string(),
                ordinal: 1,
            },
        )]);
        let ordinals = HashMap::from([("bronze".to_string(), 1u16)]);
        let idx = VolumeIndex::build(&[
            VolumeSource {
                source_id: uid(2),
                cv_amount: d!(10.10),
            },
            VolumeSource {
                source_id: uid(3),
                cv_amount: d!(20.20),
            },
        ])
        .unwrap();
        let req = DistributorCountRequirement {
            count: 1,
            min_rank: "bronze".to_string(),
            search_mode: SearchMode::AnyLevel,
            search_depth: None,
            total_count: 1,
            min_leg_group_volume: d!(30.30),
        };
        assert!(distributor_count_meets(&req, uid(1), &tree, &idx, &already, &ordinals).unwrap());
    }
}
