use std::collections::HashMap;

use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_unilevel};
use network_engine::config::{CompensationPlan, StructureConfig};
use network_engine::tree::node::Node;
use network_engine::tree::unilevel::UnilevelTree;
use uuid::Uuid;

use crate::protocol::{Request, Response};
use crate::state::WorkerState;

// Note: The Rust library also provides get_branch, count_downline, and count_branch
// operations. These are intentionally not exposed through the NDJSON protocol — they
// are server-side operations used for internal calculations (e.g., commission walks).
// Expose through handlers only when a Go-side caller needs them.

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

// --- Tree mutation handlers ---

pub fn handle_add_root(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
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

    let tree = state.unilevel_tree.get_or_insert_with(UnilevelTree::new);
    match tree.add_root(user_id, enrolled_at) {
        Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
        Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
    }
}

pub fn handle_add_node(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
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

    let tree = match state.unilevel_tree.as_mut() {
        Some(t) => t,
        None => {
            return Response::error(
                request.id.clone(),
                "NO_TREE",
                "no tree initialized; call add_root first",
            );
        }
    };

    match tree.add_node(user_id, parent_id, enrolled_at) {
        Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
        Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
    }
}

pub fn handle_remove_node(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };

    let tree = match state.unilevel_tree.as_mut() {
        Some(t) => t,
        None => return Response::error(request.id.clone(), "NO_TREE", "no tree initialized"),
    };

    match tree.remove_node(user_id) {
        Ok(()) => Response::success(request.id.clone(), serde_json::json!({"removed": true})),
        Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
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

    let tree = match state.unilevel_tree.as_ref() {
        Some(t) => t,
        None => return Response::error(request.id.clone(), "NO_TREE", "no tree initialized"),
    };

    match tree.get_parent(user_id) {
        Ok(Some(node)) => Response::success(
            request.id.clone(),
            serde_json::to_value(NodeResponse::from_node(node)).unwrap(),
        ),
        Ok(None) => Response::success(request.id.clone(), serde_json::Value::Null),
        Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
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

    let tree = match state.unilevel_tree.as_ref() {
        Some(t) => t,
        None => return Response::error(request.id.clone(), "NO_TREE", "no tree initialized"),
    };

    match tree.get_children(user_id) {
        Ok(nodes) => {
            let items: Vec<NodeResponse> =
                nodes.iter().map(|n| NodeResponse::from_node(n)).collect();
            Response::success(request.id.clone(), serde_json::to_value(items).unwrap())
        }
        Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
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

    let tree = match state.unilevel_tree.as_ref() {
        Some(t) => t,
        None => return Response::error(request.id.clone(), "NO_TREE", "no tree initialized"),
    };

    match tree.get_upline(user_id, depth) {
        Ok(nodes) => {
            let items: Vec<NodeResponse> =
                nodes.iter().map(|n| NodeResponse::from_node(n)).collect();
            Response::success(request.id.clone(), serde_json::to_value(items).unwrap())
        }
        Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
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

    let tree = match state.unilevel_tree.as_ref() {
        Some(t) => t,
        None => return Response::error(request.id.clone(), "NO_TREE", "no tree initialized"),
    };

    match tree.get_downline(user_id, depth) {
        Ok(nodes) => {
            let items: Vec<NodeResponse> =
                nodes.iter().map(|n| NodeResponse::from_node(n)).collect();
            Response::success(request.id.clone(), serde_json::to_value(items).unwrap())
        }
        Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
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

    let tree = match state.unilevel_tree.as_ref() {
        Some(t) => t,
        None => return Response::error(request.id.clone(), "NO_TREE", "no tree initialized"),
    };

    match tree.get_position(user_id) {
        Ok(pos) => {
            // Convert downline_counts from HashMap<usize, usize> to a JSON object
            // with string keys (JSON requires string keys).
            let downline_counts: HashMap<String, usize> = pos
                .downline_counts
                .iter()
                .map(|(k, v)| (k.to_string(), *v))
                .collect();

            let parent_user_id = pos.parent_user_id.map(|id| id.to_string());

            Response::success(
                request.id.clone(),
                serde_json::json!({
                    "user_id": pos.user_id.to_string(),
                    "parent_user_id": parent_user_id,
                    "position": pos.position,
                    "depth": pos.depth,
                    "child_count": pos.child_count,
                    "downline_counts": downline_counts,
                    "enrolled_at": pos.enrolled_at,
                }),
            )
        }
        Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
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

    let tree = match state.unilevel_tree.as_ref() {
        Some(t) => t,
        None => return Response::error(request.id.clone(), "NO_TREE", "no tree initialized"),
    };

    match tree.is_descendant_of(user_id, ancestor_id) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::json!({"is_descendant": result}),
        ),
        Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
    }
}

// --- Commission handlers ---

/// Request parameters for calculating unilevel commissions.
#[derive(serde::Deserialize)]
struct CalculateUnilevelParams {
    structure_name: String,
    snapshots: HashMap<Uuid, DistributorSnapshot>,
    volume: Vec<VolumeSource>,
}

pub fn handle_calculate_unilevel(state: &WorkerState, request: &Request) -> Response {
    let plan = match &state.plan {
        Some(p) => p,
        None => return Response::error(request.id.clone(), "NO_PLAN", "no plan loaded"),
    };
    let tree = match &state.unilevel_tree {
        Some(t) => t,
        None => return Response::error(request.id.clone(), "NO_TREE", "no tree initialized"),
    };

    let params: CalculateUnilevelParams = match serde_json::from_str(request.params.get()) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(request.id.clone(), "INVALID_PARAMS", e.to_string());
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
        Ok(earnings) => {
            Response::success(request.id.clone(), serde_json::to_value(&earnings).unwrap())
        }
        Err(e) => Response::error(request.id.clone(), "CALCULATION_ERROR", e.to_string()),
    }
}

// --- Helpers ---

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

/// Parses an optional u32 parameter from the request params.
/// Returns `None` if the field is missing or not a valid number.
fn parse_u32_param(params: &serde_json::Value, field: &str) -> Option<u32> {
    params
        .get(field)
        .and_then(|v| v.as_u64())
        .and_then(|v| u32::try_from(v).ok())
}
