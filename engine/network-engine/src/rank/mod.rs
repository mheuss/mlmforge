//! Per-period rank evaluation against the plan's rank ladder.

pub mod evaluator;
pub mod predicates;
pub mod types;

pub use types::{
    DistributorPrimitives, EvaluatedRank, EvaluationError, EvaluationInputs, EvaluationResult,
};

use std::collections::{BTreeSet, HashMap};

use uuid::Uuid;

use crate::config::CompensationPlan;
use crate::config::rank::RankDefinition;
use crate::tree::navigator::TreeNavigator;

use self::evaluator::{EvalCtx, VolumeIndex, evaluation_order_for_users, iterate_to_fixpoint};

/// Evaluate the rank of every distributor in `inputs` against the plan's
/// rank ladder. Refuses inputs that leave out a member of a tree in `trees`
/// whose structure a rank above the lowest names.
pub fn evaluate_ranks(
    plan: &CompensationPlan,
    trees: &HashMap<String, &dyn TreeNavigator>,
    inputs: &EvaluationInputs,
) -> Result<EvaluationResult, EvaluationError> {
    // Sort ranks by ordinal ascending once.
    let mut ranks_sorted: Vec<&crate::config::rank::RankDefinition> = plan.ranks.iter().collect();
    ranks_sorted.sort_by_key(|r| r.ordinal);
    // Re-bind to owned RankDefinition slice for predicate lifetimes.
    // (RankDefinition is Clone via the derive on its fields.)
    let mut ranks_owned: Vec<crate::config::rank::RankDefinition> =
        ranks_sorted.iter().map(|r| (*r).clone()).collect();
    // Resolve fraction caps once per evaluation. The exact product is too slow to compute
    // per distributor.
    predicates::resolve_fraction_caps(&mut ranks_owned);

    // BR9 empty-axis guard: a time gate cannot be evaluated without an axis.
    // If any rank above the lowest declares a window or tenure requirement but the caller
    // supplied no history_window, fail loud rather than silently treating every
    // gate as unmet. The guard lives engine-side because the Go client does not
    // have the plan.
    if inputs.history_window.is_empty() {
        if let Some(r) = ranks_owned
            .iter()
            .skip(1)
            .find(|r| r.qualification.window.is_some() || r.qualification.tenure.is_some())
        {
            return Err(EvaluationError::TimeGateWithoutHistory {
                rank: r.name.clone(),
            });
        }
    }

    require_every_tree_member(plan, trees, &inputs.distributors)?;

    let rank_ordinals: HashMap<String, u16> = ranks_owned
        .iter()
        .map(|r| (r.name.clone(), r.ordinal))
        .collect();

    let volume_index = VolumeIndex::build(&inputs.volume_sources);

    let user_ids: Vec<Uuid> = inputs.distributors.keys().copied().collect();
    let order = evaluation_order_for_users(trees, &user_ids);

    // Iterate evaluation passes until the rank map stops changing. A single
    // ordered pass is correct only when one order places every descendant
    // before its ancestor in every tree at once; multi-structure plans break
    // that. The fixpoint is order-independent. See design-rationale 026.
    let ctx = EvalCtx {
        distributors: &inputs.distributors,
        ranks: &ranks_owned,
        trees,
        volume_index: &volume_index,
        rank_ordinals: &rank_ordinals,
        history_window: &inputs.history_window,
        history: &inputs.history,
    };
    let mut already = iterate_to_fixpoint(&order, &ctx)?;

    // Every input distributor the fixpoint did not rank holds the lowest rank.
    if let Some(base) = ranks_owned.first() {
        for user_id in &user_ids {
            already
                .entry(*user_id)
                .or_insert_with(|| EvaluatedRank::Qualified {
                    rank: base.name.clone(),
                    ordinal: base.ordinal,
                });
        }
    }

    // Move into a BTreeMap so the result serializes with sorted keys.
    let ranks: std::collections::BTreeMap<Uuid, EvaluatedRank> = already.into_iter().collect();
    Ok(EvaluationResult { ranks })
}

/// Refuse inputs that leave out a member of a tree in `trees` whose structure
/// a rank above the lowest names. A named structure missing from `trees` is
/// skipped.
fn require_every_tree_member(
    plan: &CompensationPlan,
    trees: &HashMap<String, &dyn TreeNavigator>,
    distributors: &HashMap<Uuid, DistributorPrimitives>,
) -> Result<(), EvaluationError> {
    let missing = structures_above_lowest_rank(plan)
        .into_iter()
        .filter_map(|name| trees.get(name))
        .flat_map(|tree| tree.user_ids())
        .filter(|id| !distributors.contains_key(id))
        .min();
    match missing {
        Some(id) => Err(EvaluationError::DistributorNotInInputs(id)),
        None => Ok(()),
    }
}

/// Return the structure names that a rank above the lowest names in its
/// qualification. The set iterates in name order.
pub fn structures_above_lowest_rank(plan: &CompensationPlan) -> BTreeSet<&str> {
    let mut ladder: Vec<&RankDefinition> = plan.ranks.iter().collect();
    // Stable, so of two ranks tied at the lowest ordinal the first is skipped.
    ladder.sort_by_key(|r| r.ordinal);
    ladder
        .into_iter()
        .skip(1)
        .flat_map(|r| r.qualification.structures.iter())
        .map(|sq| sq.structure.as_str())
        .collect()
}
