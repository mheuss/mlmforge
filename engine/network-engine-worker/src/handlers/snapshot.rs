use network_engine::board_plan::BoardPlanEngine;
use network_engine::streamline::StreamlineEngine;
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::matrix::MatrixTree;
use network_engine::tree::unilevel::UnilevelTree;

use super::common::parse_params;
use crate::protocol::{Request, Response};
use crate::state::{TreeInstance, WorkerState};

/// Serializes a tree or board plan engine for snapshot persistence.
///
/// Params: structure.
pub(crate) fn handle_take_snapshot(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match params.get("structure").and_then(|v| v.as_str()) {
        Some(s) => s,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing structure name",
            );
        }
    };

    let tree = match state.trees.get(structure) {
        Some(t) => t,
        None => {
            return Response::error(
                request.id.clone(),
                "STRUCTURE_NOT_FOUND",
                format!("structure '{}' not found", structure),
            );
        }
    };

    let snapshot = match tree {
        TreeInstance::Unilevel(t) => serde_json::to_value(t),
        TreeInstance::Binary(t) => serde_json::to_value(t),
        TreeInstance::Matrix(t) => serde_json::to_value(t),
        TreeInstance::BoardPlan(e) => serde_json::to_value(e),
        TreeInstance::Streamline(e) => serde_json::to_value(e),
    };

    match snapshot {
        Ok(data) => {
            let tree_type = match tree {
                TreeInstance::Unilevel(_) => "unilevel",
                TreeInstance::Binary(_) => "binary",
                TreeInstance::Matrix(_) => "matrix",
                TreeInstance::BoardPlan(_) => "board_plan",
                TreeInstance::Streamline(_) => "streamline",
            };
            Response::success(
                request.id.clone(),
                serde_json::json!({
                    "tree_type": tree_type,
                    "data": data,
                }),
            )
        }
        Err(e) => Response::error(
            request.id.clone(),
            "SERIALIZATION_ERROR",
            format!("failed to serialize snapshot: {}", e),
        ),
    }
}

/// Deserializes and replaces a tree or board plan engine from a snapshot.
///
/// Params: structure, tree_type, data.
pub(crate) fn handle_restore_snapshot(state: &mut WorkerState, request: &Request) -> Response {
    #[derive(serde::Deserialize)]
    struct Params {
        structure: String,
        tree_type: String,
        data: serde_json::Value,
    }

    let params: Params = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string());
        }
    };

    if state.trees.contains_key(&params.structure) {
        return Response::error(
            request.id.clone(),
            "TREE_EXISTS",
            format!(
                "structure '{}' already exists; remove it first to restore a snapshot",
                params.structure
            ),
        );
    }

    let instance = match params.tree_type.as_str() {
        "unilevel" => match serde_json::from_value::<UnilevelTree>(params.data) {
            Ok(t) => TreeInstance::Unilevel(t),
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize unilevel snapshot: {}", e),
                );
            }
        },
        "binary" => match serde_json::from_value::<BinaryTree>(params.data) {
            Ok(t) => TreeInstance::Binary(t),
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize binary snapshot: {}", e),
                );
            }
        },
        "matrix" => match serde_json::from_value::<MatrixTree>(params.data) {
            Ok(t) => TreeInstance::Matrix(t),
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize matrix snapshot: {}", e),
                );
            }
        },
        "board_plan" => match serde_json::from_value::<BoardPlanEngine>(params.data) {
            Ok(e) => TreeInstance::BoardPlan(e),
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize board plan snapshot: {}", e),
                );
            }
        },
        "streamline" => match serde_json::from_value::<StreamlineEngine>(params.data) {
            Ok(e) => TreeInstance::Streamline(e),
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize streamline snapshot: {}", e),
                );
            }
        },
        other => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!("unknown tree_type: {}", other),
            );
        }
    };

    state.trees.insert(params.structure, instance);
    Response::success(request.id.clone(), serde_json::json!({"restored": true}))
}
