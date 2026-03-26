use std::collections::HashMap;

use network_engine::board_plan::BoardPlanEngine;
use network_engine::commission::{
    DistributorSnapshot, LegVolumes, VolumeSource, calculate_binary_pairing,
    calculate_board_commissions, calculate_streamline, calculate_unilevel,
};
use network_engine::config::board_plan::BoardPlanConfig;
use network_engine::config::matrix::SpilloverDirection;
use network_engine::config::streamline::StreamAssignmentMode;
use network_engine::config::{
    BinaryStructureConfig, CompensationPlan, StreamlineStructureConfig, StructureConfig,
};
use network_engine::streamline::StreamlineEngine;
use network_engine::streamline::engine::StreamlineConfig;
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::error::TreeError;
use network_engine::tree::matrix::{MatrixTree, PruningMode};
use network_engine::tree::node::Node;
use network_engine::tree::unilevel::UnilevelTree;
use uuid::Uuid;

use crate::protocol::{Request, Response};
use crate::state::{TreeInstance, WorkerState};

/// Serializable representation of a tree node for JSON responses.
///
/// Mirrors the public fields of `Node` but is owned and serializable.
/// Arena indices (parent, children) are intentionally excluded because
/// they are meaningless outside the tree.
///
/// `user_id` is stored as a `String` rather than `Uuid` because this is a
/// lightweight serialization struct that converts from the library's `Node`
/// type. A plain string is simpler than depending on uuid's serde feature
/// transitively, and the wire format is the same either way: a hyphenated
/// UUID string.
#[derive(serde::Serialize)]
struct NodeResponse {
    user_id: String,
    depth: u32,
    /// Unix timestamp in seconds when the user was enrolled.
    enrolled_at: i64,
}

impl NodeResponse {
    fn from_node(node: &Node) -> Self {
        Self {
            user_id: node.user_id.to_string(),
            depth: node.depth,
            enrolled_at: node.enrolled_at,
        }
    }
}

// --- Tree lookup helpers ---

/// Reads the "structure" param and looks up the named tree (immutable).
fn get_tree<'a>(
    state: &'a WorkerState,
    params: &serde_json::Value,
    request_id: &str,
) -> Result<&'a TreeInstance, Response> {
    let structure = params
        .get("structure")
        .and_then(|v| v.as_str())
        .ok_or_else(|| {
            Response::error(
                request_id.to_string(),
                "MISSING_PARAM",
                "missing structure name",
            )
        })?;
    state.trees.get(structure).ok_or_else(|| {
        Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("no tree named '{}'", structure),
        )
    })
}

/// Reads the "structure" param and looks up the named tree (mutable).
fn get_tree_mut<'a>(
    state: &'a mut WorkerState,
    structure: &str,
    request_id: &str,
) -> Result<&'a mut TreeInstance, Response> {
    state.trees.get_mut(structure).ok_or_else(|| {
        Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("no tree named '{}'", structure),
        )
    })
}

/// Maps a `TreeError` to a `Response` with an appropriate error code.
fn tree_error_to_response(request_id: &str, e: TreeError) -> Response {
    let code = match &e {
        TreeError::PositionOccupied { .. } => "POSITION_OCCUPIED",
        TreeError::PositionOutOfRange { .. } => "INVALID_POSITION",
        TreeError::UserNotFound(_) => "USER_NOT_FOUND",
        TreeError::UserAlreadyExists(_) => "USER_ALREADY_EXISTS",
        TreeError::RootAlreadyExists => "ROOT_ALREADY_EXISTS",
        TreeError::HasChildren(_, _) => "HAS_CHILDREN",
        TreeError::InvalidWidth(_) => "INVALID_WIDTH",
        TreeError::TreeEmpty => "TREE_EMPTY",
        TreeError::CannotRemoveRoot => "CANNOT_REMOVE_ROOT",
        TreeError::SponsorNotFound(_) => "SPONSOR_NOT_FOUND",
        TreeError::UserNotInHoldingTank(_) => "USER_NOT_IN_HOLDING_TANK",
        TreeError::UnsupportedSpillover => "UNSUPPORTED_SPILLOVER",
        TreeError::SubtreeFull(_) => "SUBTREE_FULL",
    };
    Response::error(request_id.to_string(), code, e.to_string())
}

// --- Plan handler ---

pub fn handle_load_plan(state: &mut WorkerState, request: &Request) -> Response {
    match serde_json::from_str::<CompensationPlan>(request.params.get()) {
        Ok(plan) => {
            state.plan = Some(plan);
            Response::success(request.id.clone(), serde_json::json!({"loaded": true}))
        }
        Err(e) => Response::error(
            request.id.clone(),
            "INVALID_PLAN",
            format!("failed to deserialize plan: {}", e),
        ),
    }
}

// --- Tree lifecycle handler ---

pub fn handle_create_tree(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let tree_type = match params.get("tree_type").and_then(|v| v.as_str()) {
        Some(t) => t,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing tree_type (unilevel, binary, or matrix)",
            );
        }
    };

    if state.trees.contains_key(&structure) {
        return Response::error(
            request.id.clone(),
            "TREE_EXISTS",
            format!("tree '{}' already exists", structure),
        );
    }

    let instance = match tree_type {
        "unilevel" => TreeInstance::Unilevel(UnilevelTree::new()),
        "binary" => TreeInstance::Binary(BinaryTree::new()),
        "matrix" => {
            let width = match params
                .get("width")
                .and_then(|v| v.as_u64())
                .and_then(|v| u8::try_from(v).ok())
            {
                Some(w) => w,
                None => {
                    return Response::error(
                        request.id.clone(),
                        "MISSING_PARAM",
                        "missing or invalid width (must be integer >= 2)",
                    );
                }
            };
            let spillover_str = match params.get("spillover").and_then(|v| v.as_str()) {
                Some(s) => s,
                None => {
                    return Response::error(
                        request.id.clone(),
                        "MISSING_PARAM",
                        "missing spillover (must be \"breadth_first\")",
                    );
                }
            };
            let spillover = match spillover_str {
                "breadth_first" => SpilloverDirection::BreadthFirst,
                other => {
                    return Response::error(
                        request.id.clone(),
                        "INVALID_PARAMS",
                        format!("unsupported spillover: {}", other),
                    );
                }
            };
            match MatrixTree::new(width, spillover) {
                Ok(t) => TreeInstance::Matrix(t),
                Err(e) => return tree_error_to_response(&request.id, e),
            }
        }
        _ => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!("unknown tree_type: {}", tree_type),
            );
        }
    };

    state.trees.insert(structure, instance);
    Response::success(request.id.clone(), serde_json::json!({"created": true}))
}

// --- Tree mutation handlers ---

pub fn handle_add_root(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let enrolled_at = match params.get("enrolled_at").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid enrolled_at (must be integer)",
            );
        }
    };

    let tree = match get_tree_mut(state, &structure, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    match tree {
        TreeInstance::Unilevel(t) => match t.add_root(user_id, enrolled_at) {
            Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
            Err(e) => tree_error_to_response(&request.id, e),
        },
        TreeInstance::Binary(t) => match t.add_root(user_id, enrolled_at) {
            Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
            Err(e) => tree_error_to_response(&request.id, e),
        },
        TreeInstance::Matrix(t) => match t.add_root(user_id, enrolled_at) {
            Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
            Err(e) => tree_error_to_response(&request.id, e),
        },
        TreeInstance::BoardPlan(_) | TreeInstance::Streamline(_) => Response::error(
            request.id.clone(),
            "UNSUPPORTED_OP",
            "add_root is not supported for this structure type",
        ),
    }
}

pub fn handle_add_node(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let sponsor_id = match parse_uuid(&params, "sponsor_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let enrolled_at = match params.get("enrolled_at").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid enrolled_at (must be integer)",
            );
        }
    };

    let tree = match get_tree_mut(state, &structure, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    match tree {
        TreeInstance::Unilevel(t) => {
            let parent_id = match parse_uuid(&params, "parent_id", &request.id) {
                Ok(id) => id,
                Err(resp) => return resp,
            };
            match t.add_node(user_id, parent_id, sponsor_id, enrolled_at) {
                Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
                Err(e) => tree_error_to_response(&request.id, e),
            }
        }
        TreeInstance::Binary(t) => {
            let parent_id = match parse_uuid(&params, "parent_id", &request.id) {
                Ok(id) => id,
                Err(resp) => return resp,
            };
            let position = match params.get("position").and_then(|v| v.as_u64()) {
                Some(p) => match usize::try_from(p) {
                    Ok(pos) => pos,
                    Err(_) => {
                        return Response::error(
                            request.id.clone(),
                            "INVALID_PARAMS",
                            format!("position {} exceeds platform limit", p),
                        );
                    }
                },
                None => {
                    return Response::error(
                        request.id.clone(),
                        "MISSING_PARAM",
                        "missing position (required for binary)",
                    );
                }
            };
            match t.add_node(user_id, parent_id, position, sponsor_id, enrolled_at) {
                Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
                Err(e) => tree_error_to_response(&request.id, e),
            }
        }
        TreeInstance::Matrix(t) => match t.add_node(user_id, sponsor_id, enrolled_at) {
            Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
            Err(e) => tree_error_to_response(&request.id, e),
        },
        TreeInstance::BoardPlan(_) | TreeInstance::Streamline(_) => Response::error(
            request.id.clone(),
            "UNSUPPORTED_OP",
            "add_node is not supported for this structure type",
        ),
    }
}

pub fn handle_add_node_at(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let sponsor_id = match parse_uuid(&params, "sponsor_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let parent_id = match parse_uuid(&params, "parent_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let position = match params.get("position").and_then(|v| v.as_u64()) {
        Some(p) => match u8::try_from(p) {
            Ok(pos) => pos,
            Err(_) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("position {} exceeds u8 limit", p),
                );
            }
        },
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing position (required for add_node_at)",
            );
        }
    };
    let enrolled_at = match params.get("enrolled_at").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid enrolled_at (must be integer)",
            );
        }
    };

    let tree = match get_tree_mut(state, &structure, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    match tree {
        TreeInstance::Matrix(t) => {
            match t.add_node_at(user_id, sponsor_id, parent_id, position, enrolled_at) {
                Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
                Err(e) => tree_error_to_response(&request.id, e),
            }
        }
        _ => Response::error(
            request.id.clone(),
            "INVALID_PARAMS",
            "add_node_at is only supported for matrix trees",
        ),
    }
}

pub fn handle_remove_node(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let tree = match get_tree_mut(state, &structure, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    match tree {
        TreeInstance::Unilevel(t) => match t.remove_node(user_id) {
            Ok(()) => Response::success(request.id.clone(), serde_json::json!({"removed": true})),
            Err(e) => tree_error_to_response(&request.id, e),
        },
        TreeInstance::Binary(t) => match t.remove_node(user_id) {
            Ok(()) => Response::success(request.id.clone(), serde_json::json!({"removed": true})),
            Err(e) => tree_error_to_response(&request.id, e),
        },
        TreeInstance::Matrix(t) => {
            let mode = match parse_pruning_mode(&params, &request.id) {
                Ok(m) => m,
                Err(resp) => return resp,
            };
            match t.remove_node(user_id, mode) {
                Ok(result) => {
                    let promoted = result.promoted.map(|u| u.to_string());
                    let repositioned: Vec<String> =
                        result.repositioned.iter().map(|u| u.to_string()).collect();
                    let moved_to_tank: Vec<String> =
                        result.moved_to_tank.iter().map(|u| u.to_string()).collect();
                    Response::success(
                        request.id.clone(),
                        serde_json::json!({
                            "removed": result.removed.to_string(),
                            "promoted": promoted,
                            "repositioned": repositioned,
                            "moved_to_tank": moved_to_tank,
                        }),
                    )
                }
                Err(e) => tree_error_to_response(&request.id, e),
            }
        }
        TreeInstance::BoardPlan(_) | TreeInstance::Streamline(_) => Response::error(
            request.id.clone(),
            "UNSUPPORTED_OP",
            "remove_node is not supported for this structure type",
        ),
    }
}

// --- Holding tank handlers (matrix-only) ---

pub fn handle_get_holding_tank(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    match tree {
        TreeInstance::Matrix(t) => {
            let sponsor_id = match params.get("sponsor_id") {
                Some(v) => {
                    let s = match v.as_str() {
                        Some(s) => s,
                        None => {
                            return Response::error(
                                request.id.clone(),
                                "INVALID_UUID",
                                "invalid sponsor_id",
                            );
                        }
                    };
                    match Uuid::parse_str(s) {
                        Ok(id) => Some(id),
                        Err(_) => {
                            return Response::error(
                                request.id.clone(),
                                "INVALID_UUID",
                                format!("invalid sponsor_id: {}", s),
                            );
                        }
                    }
                }
                None => None,
            };

            let entries = t.get_holding_tank(sponsor_id);
            let items: Vec<serde_json::Value> = entries
                .iter()
                .map(|e| {
                    serde_json::json!({
                        "user_id": e.user_id.to_string(),
                        "sponsor_user_id": e.sponsor_user_id.map(|id| id.to_string()),
                        "enrolled_at": e.enrolled_at,
                    })
                })
                .collect();

            Response::success(request.id.clone(), serde_json::Value::Array(items))
        }
        _ => Response::error(
            request.id.clone(),
            "INVALID_PARAMS",
            "get_holding_tank is only supported for matrix trees",
        ),
    }
}

pub fn handle_place_from_tank(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let parent_id = match parse_uuid(&params, "parent_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let position = match params.get("position").and_then(|v| v.as_u64()) {
        Some(p) => match u8::try_from(p) {
            Ok(pos) => pos,
            Err(_) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("position {} exceeds u8 limit", p),
                );
            }
        },
        None => {
            return Response::error(request.id.clone(), "MISSING_PARAM", "missing position");
        }
    };

    let tree = match get_tree_mut(state, &structure, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    match tree {
        TreeInstance::Matrix(t) => match t.place_from_tank(user_id, parent_id, position) {
            Ok(_) => Response::success(request.id.clone(), serde_json::json!({"placed": true})),
            Err(e) => tree_error_to_response(&request.id, e),
        },
        _ => Response::error(
            request.id.clone(),
            "INVALID_PARAMS",
            "place_from_tank is only supported for matrix trees",
        ),
    }
}

// --- Tree query handlers ---

pub fn handle_get_parent(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    let nav = match tree.as_navigator() {
        Some(n) => n,
        None => {
            return Response::error(
                request.id.clone(),
                "UNSUPPORTED_OP",
                "operation not supported for board plan structures",
            );
        }
    };

    match nav.get_parent(user_id) {
        Ok(Some(node)) => Response::success(
            request.id.clone(),
            serde_json::to_value(NodeResponse::from_node(node))
                .expect("serialization of NodeResponse is infallible"),
        ),
        Ok(None) => Response::success(request.id.clone(), serde_json::Value::Null),
        Err(e) => tree_error_to_response(&request.id, e),
    }
}

pub fn handle_get_children(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    let nav = match tree.as_navigator() {
        Some(n) => n,
        None => {
            return Response::error(
                request.id.clone(),
                "UNSUPPORTED_OP",
                "operation not supported for board plan structures",
            );
        }
    };

    match nav.get_children(user_id) {
        Ok(nodes) => {
            let items: Vec<NodeResponse> =
                nodes.iter().map(|n| NodeResponse::from_node(n)).collect();
            Response::success(
                request.id.clone(),
                serde_json::to_value(items)
                    .expect("serialization of Vec<NodeResponse> is infallible"),
            )
        }
        Err(e) => tree_error_to_response(&request.id, e),
    }
}

pub fn handle_get_upline(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let depth = parse_u32_param(&params, "depth").unwrap_or(0);

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    let nav = match tree.as_navigator() {
        Some(n) => n,
        None => {
            return Response::error(
                request.id.clone(),
                "UNSUPPORTED_OP",
                "operation not supported for board plan structures",
            );
        }
    };

    match nav.get_upline(user_id, depth) {
        Ok(nodes) => {
            let items: Vec<NodeResponse> =
                nodes.iter().map(|n| NodeResponse::from_node(n)).collect();
            Response::success(
                request.id.clone(),
                serde_json::to_value(items)
                    .expect("serialization of Vec<NodeResponse> is infallible"),
            )
        }
        Err(e) => tree_error_to_response(&request.id, e),
    }
}

pub fn handle_get_downline(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let depth = parse_u32_param(&params, "depth").unwrap_or(0);

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    let nav = match tree.as_navigator() {
        Some(n) => n,
        None => {
            return Response::error(
                request.id.clone(),
                "UNSUPPORTED_OP",
                "operation not supported for board plan structures",
            );
        }
    };

    match nav.get_downline(user_id, depth) {
        Ok(nodes) => {
            let items: Vec<NodeResponse> =
                nodes.iter().map(|n| NodeResponse::from_node(n)).collect();
            Response::success(
                request.id.clone(),
                serde_json::to_value(items)
                    .expect("serialization of Vec<NodeResponse> is infallible"),
            )
        }
        Err(e) => tree_error_to_response(&request.id, e),
    }
}

pub fn handle_get_position(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    let nav = match tree.as_navigator() {
        Some(n) => n,
        None => {
            return Response::error(
                request.id.clone(),
                "UNSUPPORTED_OP",
                "operation not supported for board plan structures",
            );
        }
    };

    match nav.get_position(user_id) {
        Ok(pos) => {
            // Convert downline_counts from HashMap<usize, usize> to a JSON object
            // with string keys (JSON requires string keys).
            let downline_counts: HashMap<String, usize> = pos
                .downline_counts
                .iter()
                .map(|(k, v)| (k.to_string(), *v))
                .collect();

            let parent_user_id = pos.parent_user_id.map(|id| id.to_string());
            let sponsor_user_id = pos.sponsor_user_id.map(|id| id.to_string());

            Response::success(
                request.id.clone(),
                serde_json::json!({
                    "user_id": pos.user_id.to_string(),
                    "parent_user_id": parent_user_id,
                    "sponsor_user_id": sponsor_user_id,
                    "position": pos.position,
                    "depth": pos.depth,
                    "child_count": pos.child_count,
                    "downline_counts": downline_counts,
                    "enrolled_at": pos.enrolled_at,
                }),
            )
        }
        Err(e) => tree_error_to_response(&request.id, e),
    }
}

pub fn handle_is_descendant_of(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let ancestor_id = match parse_uuid(&params, "ancestor_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    let nav = match tree.as_navigator() {
        Some(n) => n,
        None => {
            return Response::error(
                request.id.clone(),
                "UNSUPPORTED_OP",
                "operation not supported for board plan structures",
            );
        }
    };

    match nav.is_descendant_of(user_id, ancestor_id) {
        Ok(is_desc) => Response::success(
            request.id.clone(),
            serde_json::json!({"is_descendant": is_desc}),
        ),
        Err(e) => tree_error_to_response(&request.id, e),
    }
}

// --- Sponsor query handlers ---

pub fn handle_get_sponsor(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    let nav = match tree.as_navigator() {
        Some(n) => n,
        None => {
            return Response::error(
                request.id.clone(),
                "UNSUPPORTED_OP",
                "operation not supported for board plan structures",
            );
        }
    };

    match nav.get_sponsor(user_id) {
        Ok(Some(node)) => Response::success(
            request.id.clone(),
            serde_json::to_value(NodeResponse::from_node(node))
                .expect("serialization of NodeResponse is infallible"),
        ),
        Ok(None) => Response::success(request.id.clone(), serde_json::Value::Null),
        Err(e) => tree_error_to_response(&request.id, e),
    }
}

pub fn handle_get_sponsor_upline(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let depth = parse_u32_param(&params, "depth").unwrap_or(0);

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    let nav = match tree.as_navigator() {
        Some(n) => n,
        None => {
            return Response::error(
                request.id.clone(),
                "UNSUPPORTED_OP",
                "operation not supported for board plan structures",
            );
        }
    };

    match nav.get_sponsor_upline(user_id, depth) {
        Ok(nodes) => {
            let items: Vec<NodeResponse> =
                nodes.iter().map(|n| NodeResponse::from_node(n)).collect();
            Response::success(
                request.id.clone(),
                serde_json::to_value(items)
                    .expect("serialization of Vec<NodeResponse> is infallible"),
            )
        }
        Err(e) => tree_error_to_response(&request.id, e),
    }
}

pub fn handle_get_sponsored(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let tree = match get_tree(state, &params, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    let nav = match tree.as_navigator() {
        Some(n) => n,
        None => {
            return Response::error(
                request.id.clone(),
                "UNSUPPORTED_OP",
                "operation not supported for board plan structures",
            );
        }
    };

    match nav.get_sponsored(user_id) {
        Ok(nodes) => {
            let items: Vec<NodeResponse> =
                nodes.iter().map(|n| NodeResponse::from_node(n)).collect();
            Response::success(
                request.id.clone(),
                serde_json::to_value(items)
                    .expect("serialization of Vec<NodeResponse> is infallible"),
            )
        }
        Err(e) => tree_error_to_response(&request.id, e),
    }
}

// --- Commission handlers ---

/// Request parameters for calculating unilevel commissions.
#[derive(serde::Deserialize)]
struct CalculateUnilevelParams {
    #[serde(rename = "structure")]
    structure_name: String,
    snapshots: HashMap<Uuid, DistributorSnapshot>,
    volume: Vec<VolumeSource>,
}

pub fn handle_calculate_unilevel(state: &WorkerState, request: &Request) -> Response {
    let plan = match &state.plan {
        Some(p) => p,
        None => return Response::error(request.id.clone(), "NO_PLAN", "no plan loaded"),
    };

    let params: CalculateUnilevelParams = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string());
        }
    };

    // Look up the named tree and verify it is a unilevel tree.
    let tree_instance = match state.trees.get(&params.structure_name) {
        Some(t) => t,
        None => {
            return Response::error(
                request.id.clone(),
                "STRUCTURE_NOT_FOUND",
                format!("no tree named '{}'", params.structure_name),
            );
        }
    };
    let tree = match tree_instance {
        TreeInstance::Unilevel(t) => t,
        TreeInstance::Binary(_) => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!(
                    "tree '{}' is binary, but calculate_unilevel requires a unilevel tree",
                    params.structure_name
                ),
            );
        }
        TreeInstance::Matrix(_) => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!(
                    "tree '{}' is matrix, but calculate_unilevel requires a unilevel tree",
                    params.structure_name
                ),
            );
        }
        TreeInstance::BoardPlan(_) | TreeInstance::Streamline(_) => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!(
                    "structure '{}' is not a unilevel tree; calculate_unilevel requires one",
                    params.structure_name
                ),
            );
        }
    };

    // Find the matching unilevel structure config by name.
    let structure = plan.structures.iter().find_map(|s| match s {
        StructureConfig::Unilevel(u) if u.name == params.structure_name => Some(u),
        _ => None,
    });
    let structure = match structure {
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
    earnings: Vec<network_engine::commission::BinaryCommissionEarning>,
    carry_forward: HashMap<String, LegVolumes>,
}

pub fn handle_calculate_binary_pairing(state: &WorkerState, request: &Request) -> Response {
    let plan = match &state.plan {
        Some(p) => p,
        None => return Response::error(request.id.clone(), "NO_PLAN", "no plan loaded"),
    };

    let params: CalculateBinaryPairingParams = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string());
        }
    };

    // Look up the named tree and verify it is a binary tree.
    let tree_instance = match state.trees.get(&params.structure_name) {
        Some(t) => t,
        None => {
            return Response::error(
                request.id.clone(),
                "STRUCTURE_NOT_FOUND",
                format!("no tree named '{}'", params.structure_name),
            );
        }
    };
    let tree = match tree_instance {
        TreeInstance::Binary(t) => t,
        TreeInstance::Unilevel(_) => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!(
                    "tree '{}' is unilevel, but calculate_binary_pairing requires a binary tree",
                    params.structure_name
                ),
            );
        }
        TreeInstance::Matrix(_) => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!(
                    "tree '{}' is matrix, but calculate_binary_pairing requires a binary tree",
                    params.structure_name
                ),
            );
        }
        TreeInstance::BoardPlan(_) | TreeInstance::Streamline(_) => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!(
                    "structure '{}' is not a binary tree; calculate_binary_pairing requires one",
                    params.structure_name
                ),
            );
        }
    };

    // Find the matching binary structure config by name.
    let structure = find_binary_structure(plan, &params.structure_name);
    let structure = match structure {
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

/// Finds a binary structure config by name in the plan.
fn find_binary_structure<'a>(
    plan: &'a CompensationPlan,
    name: &str,
) -> Option<&'a BinaryStructureConfig> {
    plan.structures.iter().find_map(|s| match s {
        StructureConfig::Binary(b) if b.name == name => Some(b),
        _ => None,
    })
}

// --- Board plan handlers ---

/// Looks up a board plan engine by structure name (mutable).
fn get_board_plan_mut<'a>(
    state: &'a mut WorkerState,
    structure: &str,
    request_id: &str,
) -> Result<&'a mut BoardPlanEngine, Response> {
    match state.trees.get_mut(structure) {
        Some(TreeInstance::BoardPlan(engine)) => Ok(engine),
        Some(_) => Err(Response::error(
            request_id.to_string(),
            "INVALID_PARAMS",
            format!("structure '{}' is not a board plan", structure),
        )),
        None => Err(Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("structure '{}' not found", structure),
        )),
    }
}

/// Looks up a board plan engine by structure name (immutable).
fn get_board_plan<'a>(
    state: &'a WorkerState,
    structure: &str,
    request_id: &str,
) -> Result<&'a BoardPlanEngine, Response> {
    match state.trees.get(structure) {
        Some(TreeInstance::BoardPlan(engine)) => Ok(engine),
        Some(_) => Err(Response::error(
            request_id.to_string(),
            "INVALID_PARAMS",
            format!("structure '{}' is not a board plan", structure),
        )),
        None => Err(Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("structure '{}' not found", structure),
        )),
    }
}

/// Maps a `BoardPlanError` to a `Response` with an appropriate error code.
fn board_plan_error_to_response(
    request_id: &str,
    e: network_engine::board_plan::BoardPlanError,
) -> Response {
    let code = match &e {
        network_engine::board_plan::BoardPlanError::MemberAlreadyExists(_) => {
            "MEMBER_ALREADY_EXISTS"
        }
        network_engine::board_plan::BoardPlanError::SponsorNotFound(_) => "SPONSOR_NOT_FOUND",
        network_engine::board_plan::BoardPlanError::BoardNotFound(_) => "BOARD_NOT_FOUND",
        network_engine::board_plan::BoardPlanError::MemberNotFound(_) => "MEMBER_NOT_FOUND",
        network_engine::board_plan::BoardPlanError::NoBoardsAvailable => "NO_BOARDS_AVAILABLE",
        network_engine::board_plan::BoardPlanError::InvalidDimensions { .. } => {
            "INVALID_DIMENSIONS"
        }
        network_engine::board_plan::BoardPlanError::MemberNotDisplaced(_) => "MEMBER_NOT_DISPLACED",
    };
    Response::error(request_id.to_string(), code, e.to_string())
}

/// Creates a `BoardPlanEngine` and stores it as a `TreeInstance::BoardPlan`.
///
/// Params: structure, width, height, config (BoardPlanConfig), timestamp.
pub fn handle_create_board_plan(state: &mut WorkerState, request: &Request) -> Response {
    #[derive(serde::Deserialize)]
    struct Params {
        structure: String,
        width: u8,
        height: u8,
        config: BoardPlanConfig,
        #[serde(default)]
        timestamp: i64,
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
            format!("structure '{}' already exists", params.structure),
        );
    }

    match BoardPlanEngine::new(params.width, params.height, params.config, params.timestamp) {
        Ok(engine) => {
            state
                .trees
                .insert(params.structure, TreeInstance::BoardPlan(engine));
            Response::success(request.id.clone(), serde_json::json!({"created": true}))
        }
        Err(e) => board_plan_error_to_response(&request.id, e),
    }
}

/// Adds a member to a board plan structure.
///
/// Params: structure, user_id, sponsor_id, timestamp.
pub fn handle_board_add_member(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let sponsor_id = match parse_uuid(&params, "sponsor_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let timestamp = match params.get("timestamp").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid timestamp (must be integer)",
            );
        }
    };

    let engine = match get_board_plan_mut(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.add_member(user_id, sponsor_id, timestamp) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result).expect("serialization of AddMemberResult is infallible"),
        ),
        Err(e) => board_plan_error_to_response(&request.id, e),
    }
}

/// Removes a member from a board plan structure.
///
/// Params: structure, user_id, timestamp.
pub fn handle_board_remove_member(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let timestamp = match params.get("timestamp").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid timestamp (must be integer)",
            );
        }
    };

    let engine = match get_board_plan_mut(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.remove_member(user_id, timestamp) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result)
                .expect("serialization of RemoveMemberResult is infallible"),
        ),
        Err(e) => board_plan_error_to_response(&request.id, e),
    }
}

/// Compresses inactive members out of their boards.
///
/// Params: structure, member_ids (array of UUID strings), timestamp.
pub fn handle_board_compress_inactive(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let member_ids: Vec<Uuid> = match params.get("member_ids").and_then(|v| v.as_array()) {
        Some(arr) => {
            let mut ids = Vec::with_capacity(arr.len());
            for val in arr {
                match val.as_str().and_then(|s| Uuid::parse_str(s).ok()) {
                    Some(id) => ids.push(id),
                    None => {
                        return Response::error(
                            request.id.clone(),
                            "INVALID_UUID",
                            "member_ids must be an array of valid UUID strings",
                        );
                    }
                }
            }
            ids
        }
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing member_ids (must be array of UUID strings)",
            );
        }
    };
    let timestamp = match params.get("timestamp").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid timestamp (must be integer)",
            );
        }
    };

    let engine = match get_board_plan_mut(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.compress_inactive(member_ids, timestamp) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result)
                .expect("serialization of CompressionResult is infallible"),
        ),
        Err(e) => board_plan_error_to_response(&request.id, e),
    }
}

/// Detects boards that have stalled (no activity since cutoff).
///
/// Params: structure, cutoff_timestamp.
pub fn handle_board_detect_stalled(state: &WorkerState, request: &Request) -> Response {
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
    let cutoff_timestamp = match params.get("cutoff_timestamp").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid cutoff_timestamp (must be integer)",
            );
        }
    };

    let engine = match get_board_plan(state, structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    let stalled = engine.detect_stalled_boards(cutoff_timestamp);
    Response::success(
        request.id.clone(),
        serde_json::to_value(&stalled).expect("serialization of Vec<StalledBoard> is infallible"),
    )
}

/// Dissolves a board, moving its members to the displaced pool.
///
/// Params: structure, board_id, timestamp.
pub fn handle_board_dissolve(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let board_id = match parse_uuid(&params, "board_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let timestamp = match params.get("timestamp").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid timestamp (must be integer)",
            );
        }
    };

    let engine = match get_board_plan_mut(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.dissolve_board(board_id, timestamp) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result)
                .expect("serialization of DissolutionResult is infallible"),
        ),
        Err(e) => board_plan_error_to_response(&request.id, e),
    }
}

/// Returns a board's state by board_id.
///
/// Params: structure, board_id.
pub fn handle_board_get_state(state: &WorkerState, request: &Request) -> Response {
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
    let board_id = match parse_uuid(&params, "board_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let engine = match get_board_plan(state, structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.get_board(board_id) {
        Some(board) => Response::success(
            request.id.clone(),
            serde_json::to_value(board).expect("serialization of Board is infallible"),
        ),
        None => Response::error(
            request.id.clone(),
            "BOARD_NOT_FOUND",
            format!("board '{}' not found", board_id),
        ),
    }
}

/// Returns which board a member is on.
///
/// Params: structure, user_id.
pub fn handle_board_get_member(state: &WorkerState, request: &Request) -> Response {
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
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let engine = match get_board_plan(state, structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.get_member_board(user_id) {
        Some(board_id) => Response::success(
            request.id.clone(),
            serde_json::json!({"board_id": board_id.to_string()}),
        ),
        None => Response::success(request.id.clone(), serde_json::Value::Null),
    }
}

/// Returns all board summaries for a board plan structure.
///
/// Params: structure.
pub fn handle_board_list(state: &WorkerState, request: &Request) -> Response {
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

    let engine = match get_board_plan(state, structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    let boards = engine.list_boards();
    Response::success(
        request.id.clone(),
        serde_json::to_value(&boards).expect("serialization of Vec<BoardSummary> is infallible"),
    )
}

/// Calculates board cycle commissions for a set of cycle events.
///
/// Params: cycle_events, period_cycle_counts, config.
pub fn handle_board_calculate_commissions(_state: &WorkerState, request: &Request) -> Response {
    #[derive(serde::Deserialize)]
    struct Params {
        cycle_events: Vec<network_engine::board_plan::CycleEvent>,
        #[serde(default)]
        period_cycle_counts: HashMap<Uuid, u32>,
        config: BoardPlanConfig,
    }

    let params: Params = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string());
        }
    };

    let result = calculate_board_commissions(
        &params.cycle_events,
        &params.period_cycle_counts,
        &params.config,
    );

    Response::success(
        request.id.clone(),
        serde_json::to_value(&result)
            .expect("serialization of BoardCommissionResult is infallible"),
    )
}

// --- Streamline handlers ---

fn get_streamline_mut<'a>(
    state: &'a mut WorkerState,
    structure: &str,
    request_id: &str,
) -> Result<&'a mut StreamlineEngine, Response> {
    match state.trees.get_mut(structure) {
        Some(TreeInstance::Streamline(engine)) => Ok(engine),
        Some(_) => Err(Response::error(
            request_id.to_string(),
            "INVALID_PARAMS",
            format!("structure '{}' is not a streamline", structure),
        )),
        None => Err(Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("structure '{}' not found", structure),
        )),
    }
}

fn get_streamline_ref<'a>(
    state: &'a WorkerState,
    structure: &str,
    request_id: &str,
) -> Result<&'a StreamlineEngine, Response> {
    match state.trees.get(structure) {
        Some(TreeInstance::Streamline(engine)) => Ok(engine),
        Some(_) => Err(Response::error(
            request_id.to_string(),
            "INVALID_PARAMS",
            format!("structure '{}' is not a streamline", structure),
        )),
        None => Err(Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("structure '{}' not found", structure),
        )),
    }
}

fn streamline_error_to_response(
    request_id: &str,
    err: network_engine::streamline::StreamlineError,
) -> Response {
    Response::error(request_id.to_string(), "STREAMLINE_ERROR", err.to_string())
}

pub fn handle_create_streamline(state: &mut WorkerState, request: &Request) -> Response {
    #[derive(serde::Deserialize)]
    struct Params {
        structure: String,
        #[serde(default)]
        assignment_mode: Option<String>,
        #[serde(default)]
        enrollment_stream_choice: bool,
        #[serde(default = "default_true")]
        freeze_on_demotion: bool,
        #[serde(default)]
        timestamp: i64,
    }

    fn default_true() -> bool {
        true
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
            format!("structure '{}' already exists", params.structure),
        );
    }

    let assignment_mode = match params
        .assignment_mode
        .as_deref()
        .unwrap_or("sponsor_stream")
    {
        "sponsor_stream" => StreamAssignmentMode::SponsorStream,
        "round_robin" => StreamAssignmentMode::RoundRobin,
        other => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!("unknown assignment_mode: {}", other),
            );
        }
    };

    let config = StreamlineConfig {
        assignment_mode,
        enrollment_stream_choice: params.enrollment_stream_choice,
        freeze_on_demotion: params.freeze_on_demotion,
    };

    let engine = StreamlineEngine::new(config, params.timestamp);
    state
        .trees
        .insert(params.structure, TreeInstance::Streamline(engine));
    Response::success(request.id.clone(), serde_json::json!({"created": true}))
}

pub fn handle_streamline_add_member(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let sponsor_id = match parse_uuid(&params, "sponsor_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let timestamp = match params.get("timestamp").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid timestamp (must be integer)",
            );
        }
    };
    let stream_id_override = parse_u32_param(&params, "stream_id_override");

    let engine = match get_streamline_mut(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.add_member(user_id, sponsor_id, timestamp, stream_id_override) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result).expect("serialization infallible"),
        ),
        Err(e) => streamline_error_to_response(&request.id, e),
    }
}

pub fn handle_streamline_remove_member(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let timestamp = match params.get("timestamp").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid timestamp (must be integer)",
            );
        }
    };

    let engine = match get_streamline_mut(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.remove_member(user_id, timestamp) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result).expect("serialization infallible"),
        ),
        Err(e) => streamline_error_to_response(&request.id, e),
    }
}

pub fn handle_streamline_expand_streams(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let total_allowed = match parse_u32_param(&params, "total_allowed") {
        Some(v) => v,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid total_allowed",
            );
        }
    };
    let timestamp = match params.get("timestamp").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid timestamp (must be integer)",
            );
        }
    };

    let engine = match get_streamline_mut(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.expand_streams(user_id, total_allowed, timestamp) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result).expect("serialization infallible"),
        ),
        Err(e) => streamline_error_to_response(&request.id, e),
    }
}

pub fn handle_streamline_update_allowance(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let total_allowed = match parse_u32_param(&params, "total_allowed") {
        Some(v) => v,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid total_allowed",
            );
        }
    };
    let timestamp = match params.get("timestamp").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid timestamp (must be integer)",
            );
        }
    };

    let engine = match get_streamline_mut(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.update_stream_allowance(user_id, total_allowed, timestamp) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result).expect("serialization infallible"),
        ),
        Err(e) => streamline_error_to_response(&request.id, e),
    }
}

pub fn handle_streamline_list_streams(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };

    let engine = match get_streamline_ref(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    let summaries = engine.list_streams();
    Response::success(
        request.id.clone(),
        serde_json::to_value(&summaries).expect("serialization infallible"),
    )
}

pub fn handle_streamline_get_member(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let engine = match get_streamline_ref(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.get_member_info(user_id) {
        Ok(info) => Response::success(
            request.id.clone(),
            serde_json::to_value(&info).expect("serialization infallible"),
        ),
        Err(e) => streamline_error_to_response(&request.id, e),
    }
}

pub fn handle_streamline_get_stream(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let stream_id = match parse_u32_param(&params, "stream_id") {
        Some(v) => v,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid stream_id",
            );
        }
    };

    let engine = match get_streamline_ref(state, &structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match engine.get_stream(stream_id) {
        Some(stream) => {
            let summary = serde_json::json!({
                "id": stream.id,
                "owner_id": stream.owner_id.to_string(),
                "member_count": stream.tree.user_ids().len(),
                "frozen": stream.frozen,
                "created_at": stream.created_at,
            });
            Response::success(request.id.clone(), summary)
        }
        None => Response::error(
            request.id.clone(),
            "STREAMLINE_ERROR",
            format!("stream {} not found", stream_id),
        ),
    }
}

pub fn handle_calculate_streamline(state: &WorkerState, request: &Request) -> Response {
    #[derive(serde::Deserialize)]
    struct Params {
        structure: String,
        plan: CompensationPlan,
        structure_config: StreamlineStructureConfig,
        snapshots: HashMap<Uuid, DistributorSnapshot>,
        volume: Vec<VolumeSource>,
    }

    let params: Params = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string());
        }
    };

    if params.structure_config.name != params.structure {
        return Response::error(
            request.id.clone(),
            "INVALID_PARAMS",
            format!(
                "structure_config.name '{}' does not match structure '{}'",
                params.structure_config.name, params.structure
            ),
        );
    }

    let engine = match get_streamline_ref(state, &params.structure, &request.id) {
        Ok(e) => e,
        Err(resp) => return resp,
    };

    match calculate_streamline(
        engine,
        &params.plan,
        &params.structure_config,
        &params.snapshots,
        &params.volume,
    ) {
        Ok(earnings) => Response::success(
            request.id.clone(),
            serde_json::to_value(&earnings).expect("serialization infallible"),
        ),
        Err(e) => Response::error(
            request.id.clone(),
            "CALCULATION_ERROR",
            format!("streamline calculation error: {}", e),
        ),
    }
}

/// Serializes a tree or board plan engine for snapshot persistence.
///
/// Params: structure.
pub fn handle_take_snapshot(state: &WorkerState, request: &Request) -> Response {
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
pub fn handle_restore_snapshot(state: &mut WorkerState, request: &Request) -> Response {
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

// --- Helpers ---

/// Extracts the "structure" field as a `String` from parsed params.
/// Used by mutation handlers that need an owned structure name for mutable
/// tree lookup.
fn extract_structure_name(
    params: &serde_json::Value,
    request_id: &str,
) -> Result<String, Response> {
    params
        .get("structure")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string())
        .ok_or_else(|| {
            Response::error(
                request_id.to_string(),
                "MISSING_PARAM",
                "missing structure name",
            )
        })
}

/// Parses the raw params into a `serde_json::Value` for handlers that access
/// individual fields by name. Returns an error response if the params are not
/// valid JSON or not a JSON object.
fn parse_params(request: &Request) -> Result<serde_json::Value, Response> {
    let value: serde_json::Value = serde_json::from_str(request.params.get()).map_err(|e| {
        Response::error(
            request.id.clone(),
            "INVALID_PARAMS",
            format!("params is not valid JSON: {}", e),
        )
    })?;
    if !value.is_object() {
        return Err(Response::error(
            request.id.clone(),
            "INVALID_PARAMS",
            "params must be a JSON object",
        ));
    }
    Ok(value)
}

fn parse_uuid(params: &serde_json::Value, field: &str, request_id: &str) -> Result<Uuid, Response> {
    let s = params.get(field).and_then(|v| v.as_str()).ok_or_else(|| {
        Response::error(
            request_id.to_string(),
            "MISSING_PARAM",
            format!("missing {}", field),
        )
    })?;
    Uuid::parse_str(s).map_err(|e| {
        Response::error(
            request_id.to_string(),
            "INVALID_UUID",
            format!("invalid {}: {}", field, e),
        )
    })
}

/// Parses the `pruning_mode` parameter for matrix remove_node.
/// Returns an error response if the field is missing or has an unknown value.
fn parse_pruning_mode(
    params: &serde_json::Value,
    request_id: &str,
) -> Result<PruningMode, Response> {
    let mode_str = params
        .get("pruning_mode")
        .and_then(|v| v.as_str())
        .ok_or_else(|| {
            Response::error(
                request_id.to_string(),
                "MISSING_PARAM",
                "missing pruning_mode (must be \"promote_earliest\" or \"holding_tank\")",
            )
        })?;
    match mode_str {
        "promote_earliest" => Ok(PruningMode::PromoteEarliest),
        "holding_tank" => Ok(PruningMode::HoldingTank),
        other => Err(Response::error(
            request_id.to_string(),
            "INVALID_PARAMS",
            format!("unknown pruning_mode: {}", other),
        )),
    }
}

/// Parses an optional u32 parameter from the request params.
/// Returns `None` if the field is missing or not a valid number.
fn parse_u32_param(params: &serde_json::Value, field: &str) -> Option<u32> {
    params
        .get(field)
        .and_then(|v| v.as_u64())
        .and_then(|v| u32::try_from(v).ok())
}
