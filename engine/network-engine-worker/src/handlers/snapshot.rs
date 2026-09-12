use network_engine::board_plan::BoardPlanEngine;
use network_engine::streamline::StreamlineEngine;
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::matrix::MatrixTree;
use network_engine::tree::unilevel::UnilevelTree;

use super::common::parse_params;
use crate::protocol::{Request, Response};
use crate::state::{TreeInstance, WorkerState};

/// A snapshot payload: the tree's own JSON under a tag naming its type.
///
/// `data` is already serialized, so the tree does not become a
/// `serde_json::Value` on the way out. That also rules out `json!`, which can
/// only nest a `Value`.
#[derive(serde::Serialize)]
struct Snapshot<'a> {
    tree_type: &'a str,
    data: Box<serde_json::value::RawValue>,
}

/// Serializes a tree or board plan engine for snapshot persistence.
///
/// Params: structure.
///
/// Serialization failure returns `SERIALIZATION_ERROR` rather than panicking.
/// Changing that to `expect` would let one bad snapshot take the whole process
/// down.
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
        TreeInstance::Unilevel(t) => serde_json::value::to_raw_value(t),
        TreeInstance::Binary(t) => serde_json::value::to_raw_value(t),
        TreeInstance::Matrix(t) => serde_json::value::to_raw_value(t),
        TreeInstance::BoardPlan(e) => serde_json::value::to_raw_value(e),
        TreeInstance::Streamline(e) => serde_json::value::to_raw_value(e),
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
            Response::success(request.id.clone(), Snapshot { tree_type, data })
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
            Ok(t) => match t.validate_restored() {
                Ok(()) => TreeInstance::Unilevel(t),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize unilevel snapshot: {}", e),
                );
            }
        },
        "binary" => match serde_json::from_value::<BinaryTree>(params.data) {
            Ok(t) => match t.validate_restored() {
                Ok(()) => TreeInstance::Binary(t),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize binary snapshot: {}", e),
                );
            }
        },
        "matrix" => match serde_json::from_value::<MatrixTree>(params.data) {
            Ok(t) => match t.validate_restored() {
                Ok(()) => TreeInstance::Matrix(t),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize matrix snapshot: {}", e),
                );
            }
        },
        "board_plan" => match serde_json::from_value::<BoardPlanEngine>(params.data) {
            Ok(e) => match e.validate_restored() {
                Ok(()) => TreeInstance::BoardPlan(e),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize board plan snapshot: {}", e),
                );
            }
        },
        "streamline" => match serde_json::from_value::<StreamlineEngine>(params.data) {
            Ok(e) => match e.validate_restored() {
                Ok(()) => TreeInstance::Streamline(e),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
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
