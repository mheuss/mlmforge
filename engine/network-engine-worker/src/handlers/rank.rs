use std::collections::{HashMap, HashSet};

use network_engine::rank::{EvaluationInputs, evaluate_ranks};
use network_engine::tree::navigator::TreeNavigator;

use super::common::require_plan;
use crate::protocol::{Request, Response};
use crate::state::WorkerState;

/// Handle `evaluate_ranks` op: compute per-distributor rank for the period.
pub(crate) fn handle_evaluate_ranks(state: &WorkerState, request: &Request) -> Response {
    let plan = match require_plan(state, &request.id) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let inputs: EvaluationInputs = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string()),
    };

    // Collect every structure name referenced by any rank's qualification.
    let mut needed: HashSet<&str> = HashSet::new();
    for rank in &plan.ranks {
        for sq in &rank.qualification.structures {
            needed.insert(sq.structure.as_str());
        }
    }

    // Build navigator map. Structures that don't expose TreeNavigator
    // (board_plan, streamline) are not supported by HEU-443. If any rank
    // references one, surface STRUCTURE_NOT_FOUND.
    let mut navigators: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    for structure_name in needed {
        match state
            .trees
            .get(structure_name)
            .and_then(|t| t.as_navigator())
        {
            Some(nav) => {
                navigators.insert(structure_name.to_string(), nav);
            }
            None => {
                return Response::error(
                    request.id.clone(),
                    "STRUCTURE_NOT_FOUND",
                    format!(
                        "rank ladder references structure '{}' which is not loaded or does not support navigation",
                        structure_name
                    ),
                );
            }
        }
    }

    match evaluate_ranks(plan, &navigators, &inputs) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result).expect("EvaluationResult serialization is infallible"),
        ),
        Err(e) => Response::error(request.id.clone(), "EVALUATION_ERROR", e.to_string()),
    }
}
