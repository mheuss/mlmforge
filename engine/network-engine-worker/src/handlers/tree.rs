use std::collections::HashMap;

use network_engine::config::matrix::SpilloverDirection;
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::matrix::MatrixTree;
use network_engine::tree::unilevel::UnilevelTree;
use uuid::Uuid;

use super::common::{
    NodeResponse, extract_structure_name, get_tree, get_tree_mut, parse_params, parse_pruning_mode,
    parse_u32_param, parse_uuid, tree_error_to_response,
};
use crate::protocol::{Request, Response};
use crate::state::{TreeInstance, WorkerState};

// --- Tree lifecycle handler ---

pub(crate) fn handle_create_tree(state: &mut WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_add_root(state: &mut WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_add_node(state: &mut WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_add_node_at(state: &mut WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_remove_node(state: &mut WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_get_holding_tank(state: &WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_place_from_tank(state: &mut WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_get_parent(state: &WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_get_children(state: &WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_get_upline(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let depth = match parse_u32_param(&params, "depth", &request.id) {
        Ok(d) => d.unwrap_or(0),
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

pub(crate) fn handle_get_downline(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let depth = match parse_u32_param(&params, "depth", &request.id) {
        Ok(d) => d.unwrap_or(0),
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

pub(crate) fn handle_get_position(state: &WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_is_descendant_of(state: &WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_get_sponsor(state: &WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_get_sponsor_upline(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let depth = match parse_u32_param(&params, "depth", &request.id) {
        Ok(d) => d.unwrap_or(0),
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

pub(crate) fn handle_get_sponsored(state: &WorkerState, request: &Request) -> Response {
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
