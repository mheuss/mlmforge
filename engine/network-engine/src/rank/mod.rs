//! Per-period rank evaluation against the plan's rank ladder.

pub mod evaluator;
pub mod predicates;
pub mod types;

pub use types::{
    DistributorPrimitives, EvaluatedRank, EvaluationError, EvaluationInputs, EvaluationResult,
};

use std::collections::HashMap;

use uuid::Uuid;

use crate::config::CompensationPlan;
use crate::tree::navigator::TreeNavigator;

use self::evaluator::{EvalCtx, VolumeIndex, evaluation_order_for_users, iterate_to_fixpoint};

/// Evaluate the qualified rank for every distributor in `inputs` that also
/// appears in at least one tree.
///
/// Iterates evaluation to a fixpoint so descendants are resolved before the
/// ancestors that count them, across any number of structure trees. Each
/// distributor ascends the rank ladder lowest-ordinal-first; the highest
/// passing rank wins. Distributors omitted from `inputs.distributors`, and
/// distributors not present in any tree, are not in the result and do not
/// contribute to ancestors' counts.
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
    let ranks_owned: Vec<crate::config::rank::RankDefinition> =
        ranks_sorted.iter().map(|r| (*r).clone()).collect();

    // BR9 empty-axis guard: a time gate cannot be evaluated without an axis.
    // If any rank declares a window or tenure requirement but the caller
    // supplied no history_window, fail loud rather than silently treating every
    // gate as unmet. The guard lives engine-side because the Go client does not
    // have the plan.
    if inputs.history_window.is_empty() {
        if let Some(r) = ranks_owned
            .iter()
            .find(|r| r.qualification.window.is_some() || r.qualification.tenure.is_some())
        {
            return Err(EvaluationError::TimeGateWithoutHistory {
                rank: r.name.clone(),
            });
        }
    }

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
    let already = iterate_to_fixpoint(&order, &ctx)?;

    // Move into a BTreeMap so the result serializes with sorted keys.
    let ranks: std::collections::BTreeMap<Uuid, EvaluatedRank> = already.into_iter().collect();
    Ok(EvaluationResult { ranks })
}
