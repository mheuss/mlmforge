use std::collections::HashMap;

use network_engine::commission::{
    DistributorSnapshot, LegVolumes, VolumeSource, calculate_binary_pairing, calculate_unilevel,
};
use network_engine::config::{BinaryStructureConfig, CompensationPlan, StructureConfig};
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::error::TreeError;
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
                "missing tree_type (unilevel or binary)",
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
    let parent_id = match parse_uuid(&params, "parent_id", &request.id) {
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
            match t.add_node(user_id, parent_id, sponsor_id, enrolled_at) {
                Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
                Err(e) => tree_error_to_response(&request.id, e),
            }
        }
        TreeInstance::Binary(t) => {
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

    match tree.as_navigator().get_parent(user_id) {
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

    match tree.as_navigator().get_children(user_id) {
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

    match tree.as_navigator().get_upline(user_id, depth) {
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

    match tree.as_navigator().get_downline(user_id, depth) {
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

    match tree.as_navigator().get_position(user_id) {
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

    match tree.as_navigator().is_descendant_of(user_id, ancestor_id) {
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

    match tree.as_navigator().get_sponsor(user_id) {
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

    match tree.as_navigator().get_sponsor_upline(user_id, depth) {
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

    match tree.as_navigator().get_sponsored(user_id) {
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

/// Parses an optional u32 parameter from the request params.
/// Returns `None` if the field is missing or not a valid number.
fn parse_u32_param(params: &serde_json::Value, field: &str) -> Option<u32> {
    params
        .get(field)
        .and_then(|v| v.as_u64())
        .and_then(|v| u32::try_from(v).ok())
}
