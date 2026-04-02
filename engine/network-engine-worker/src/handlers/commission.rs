use std::collections::HashMap;

use network_engine::commission::{
    BinaryCommissionEarning, DistributorSnapshot, LegVolumes, VolumeSource,
    calculate_binary_pairing, calculate_generation, calculate_unilevel,
};
use network_engine::config::{
    BinaryStructureConfig, CompensationPlan, GenerationStructureConfig, StructureConfig,
    UnilevelStructureConfig,
};
use uuid::Uuid;

use super::common::{require_binary_tree, require_plan, require_unilevel_tree};
use crate::protocol::{Request, Response};
use crate::state::WorkerState;

// --- Structure config finders ---

fn find_unilevel_structure<'a>(
    plan: &'a CompensationPlan,
    name: &str,
) -> Option<&'a UnilevelStructureConfig> {
    plan.structures.iter().find_map(|s| match s {
        StructureConfig::Unilevel(u) if u.name == name => Some(u),
        _ => None,
    })
}

fn find_generation_structure<'a>(
    plan: &'a CompensationPlan,
    name: &str,
) -> Option<&'a GenerationStructureConfig> {
    plan.structures.iter().find_map(|s| match s {
        StructureConfig::Generation(g) if g.name == name => Some(g),
        _ => None,
    })
}

fn find_binary_structure<'a>(
    plan: &'a CompensationPlan,
    name: &str,
) -> Option<&'a BinaryStructureConfig> {
    plan.structures.iter().find_map(|s| match s {
        StructureConfig::Binary(b) if b.name == name => Some(b),
        _ => None,
    })
}

// --- Commission param types ---

/// Request parameters for calculating unilevel commissions.
#[derive(serde::Deserialize)]
struct CalculateUnilevelParams {
    #[serde(rename = "structure")]
    structure_name: String,
    snapshots: HashMap<Uuid, DistributorSnapshot>,
    volume: Vec<VolumeSource>,
}

/// Request parameters for calculating generation commissions.
#[derive(serde::Deserialize)]
struct CalculateGenerationParams {
    #[serde(rename = "structure")]
    structure_name: String,
    snapshots: HashMap<Uuid, DistributorSnapshot>,
    volume: Vec<VolumeSource>,
}

/// Request parameters for calculating binary pairing commissions.
#[derive(serde::Deserialize)]
struct CalculateBinaryPairingParams {
    #[serde(rename = "structure")]
    structure_name: String,
    snapshots: HashMap<Uuid, DistributorSnapshot>,
    volume: Vec<VolumeSource>,
    #[serde(default)]
    carry_forward: HashMap<Uuid, LegVolumes>,
    #[serde(default)]
    ownership: Option<HashMap<Uuid, Uuid>>,
}

/// Wire response for a binary pairing calculation result.
///
/// The library's `BinaryCalculationResult` is not Serialize (carry_forward
/// uses `HashMap<Uuid, LegVolumes>` which we convert to string-keyed JSON).
/// This struct owns the wire-safe shape.
#[derive(serde::Serialize)]
struct BinaryCalculationResponse {
    earnings: Vec<BinaryCommissionEarning>,
    carry_forward: HashMap<String, LegVolumes>,
}

// --- Commission handlers ---

pub(crate) fn handle_calculate_unilevel(state: &WorkerState, request: &Request) -> Response {
    let plan = match require_plan(state, &request.id) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let params: CalculateUnilevelParams = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string());
        }
    };
    let tree = match require_unilevel_tree(state, &params.structure_name, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };
    let structure = match find_unilevel_structure(plan, &params.structure_name) {
        Some(s) => s,
        None => {
            return Response::error(
                request.id.clone(),
                "STRUCTURE_NOT_FOUND",
                format!("no unilevel structure named '{}'", params.structure_name),
            );
        }
    };

    match calculate_unilevel(tree, plan, structure, &params.snapshots, &params.volume) {
        Ok(earnings) => Response::success(
            request.id.clone(),
            serde_json::to_value(&earnings)
                .expect("serialization of Vec<CommissionEarning> is infallible"),
        ),
        Err(e) => Response::error(request.id.clone(), "CALCULATION_ERROR", e.to_string()),
    }
}

pub(crate) fn handle_calculate_generation(state: &WorkerState, request: &Request) -> Response {
    let plan = match require_plan(state, &request.id) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let params: CalculateGenerationParams = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string());
        }
    };
    let tree = match require_unilevel_tree(state, &params.structure_name, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };
    let structure = match find_generation_structure(plan, &params.structure_name) {
        Some(s) => s,
        None => {
            return Response::error(
                request.id.clone(),
                "STRUCTURE_NOT_FOUND",
                format!("no generation structure named '{}'", params.structure_name),
            );
        }
    };

    match calculate_generation(tree, plan, structure, &params.snapshots, &params.volume) {
        Ok(earnings) => Response::success(
            request.id.clone(),
            serde_json::to_value(&earnings)
                .expect("serialization of Vec<CommissionEarning> is infallible"),
        ),
        Err(e) => Response::error(request.id.clone(), "CALCULATION_ERROR", e.to_string()),
    }
}

pub(crate) fn handle_calculate_binary_pairing(state: &WorkerState, request: &Request) -> Response {
    let plan = match require_plan(state, &request.id) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let params: CalculateBinaryPairingParams = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string());
        }
    };
    let tree = match require_binary_tree(state, &params.structure_name, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };
    let structure = match find_binary_structure(plan, &params.structure_name) {
        Some(s) => s,
        None => {
            return Response::error(
                request.id.clone(),
                "STRUCTURE_NOT_FOUND",
                format!("no binary structure named '{}'", params.structure_name),
            );
        }
    };

    match calculate_binary_pairing(
        tree,
        plan,
        structure,
        &params.snapshots,
        &params.volume,
        &params.carry_forward,
        params.ownership.as_ref(),
    ) {
        Ok(result) => {
            let response = BinaryCalculationResponse {
                earnings: result.earnings,
                carry_forward: result
                    .carry_forward
                    .into_iter()
                    .map(|(k, v)| (k.to_string(), v))
                    .collect(),
            };
            Response::success(
                request.id.clone(),
                serde_json::to_value(&response)
                    .expect("serialization of BinaryCalculationResponse is infallible"),
            )
        }
        Err(e) => Response::error(request.id.clone(), "CALCULATION_ERROR", e.to_string()),
    }
}
