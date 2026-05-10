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

use self::evaluator::{VolumeIndex, evaluate_distributor, evaluation_order_for_users};

/// Evaluate the qualified rank for every distributor in `inputs` that also
/// appears in at least one tree.
///
/// Walks each tree bottom-up so descendants are evaluated first. Each
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

    let rank_ordinals: HashMap<String, u16> = ranks_owned
        .iter()
        .map(|r| (r.name.clone(), r.ordinal))
        .collect();

    let volume_index = VolumeIndex::build(&inputs.volume_sources);

    let user_ids: Vec<Uuid> = inputs.distributors.keys().copied().collect();
    let order = evaluation_order_for_users(trees, &user_ids);

    let mut already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
    for user_id in order {
        let Some(primitives) = inputs.distributors.get(&user_id) else {
            continue;
        };
        let evaluated = evaluate_distributor(
            user_id,
            primitives,
            &ranks_owned,
            trees,
            &volume_index,
            &rank_ordinals,
            &already,
        )?;
        already.insert(user_id, evaluated);
    }

    // Move into a BTreeMap so the result serializes with sorted keys.
    let ranks: std::collections::BTreeMap<Uuid, EvaluatedRank> = already.into_iter().collect();
    Ok(EvaluationResult { ranks })
}
