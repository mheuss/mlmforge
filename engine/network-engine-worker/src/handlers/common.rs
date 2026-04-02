use network_engine::config::CompensationPlan;
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::error::TreeError;
use network_engine::tree::matrix::PruningMode;
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
pub(crate) struct NodeResponse {
    user_id: String,
    depth: u32,
    /// Unix timestamp in seconds when the user was enrolled.
    enrolled_at: i64,
}

impl NodeResponse {
    pub(crate) fn from_node(node: &Node) -> Self {
        Self {
            user_id: node.user_id.to_string(),
            depth: node.depth,
            enrolled_at: node.enrolled_at,
        }
    }
}

// --- Tree lookup helpers ---

/// Reads the "structure" param and looks up the named tree (immutable).
pub(crate) fn get_tree<'a>(
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
pub(crate) fn get_tree_mut<'a>(
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
pub(crate) fn tree_error_to_response(request_id: &str, e: TreeError) -> Response {
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

pub(crate) fn handle_load_plan(state: &mut WorkerState, request: &Request) -> Response {
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

// --- Require helpers for commission handlers ---

pub(crate) fn require_plan<'a>(
    state: &'a WorkerState,
    request_id: &str,
) -> Result<&'a CompensationPlan, Response> {
    state
        .plan
        .as_ref()
        .ok_or_else(|| Response::error(request_id.to_string(), "NO_PLAN", "no plan loaded"))
}

pub(crate) fn require_unilevel_tree<'a>(
    state: &'a WorkerState,
    name: &str,
    request_id: &str,
) -> Result<&'a UnilevelTree, Response> {
    match state.trees.get(name) {
        Some(TreeInstance::Unilevel(t)) => Ok(t),
        Some(_) => Err(Response::error(
            request_id.to_string(),
            "INVALID_PARAMS",
            format!("structure '{}' is not a unilevel tree", name),
        )),
        None => Err(Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("no tree named '{}'", name),
        )),
    }
}

pub(crate) fn require_binary_tree<'a>(
    state: &'a WorkerState,
    name: &str,
    request_id: &str,
) -> Result<&'a BinaryTree, Response> {
    match state.trees.get(name) {
        Some(TreeInstance::Binary(t)) => Ok(t),
        Some(_) => Err(Response::error(
            request_id.to_string(),
            "INVALID_PARAMS",
            format!("structure '{}' is not a binary tree", name),
        )),
        None => Err(Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("no tree named '{}'", name),
        )),
    }
}

// --- Param parsing helpers ---

/// Extracts the "structure" field as a `String` from parsed params.
/// Used by mutation handlers that need an owned structure name for mutable
/// tree lookup.
pub(crate) fn extract_structure_name(
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
pub(crate) fn parse_params(request: &Request) -> Result<serde_json::Value, Response> {
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

pub(crate) fn parse_uuid(
    params: &serde_json::Value,
    field: &str,
    request_id: &str,
) -> Result<Uuid, Response> {
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
pub(crate) fn parse_pruning_mode(
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
///
/// Returns `Ok(None)` when the field is absent, `Ok(Some(n))` on success,
/// and `Err(Response)` when the field is present but not a valid u32.
pub(crate) fn parse_u32_param(
    params: &serde_json::Value,
    field: &str,
    request_id: &str,
) -> Result<Option<u32>, Response> {
    match params.get(field) {
        None => Ok(None),
        Some(v) => {
            let n = v.as_u64().ok_or_else(|| {
                Response::error(
                    request_id.to_string(),
                    "INVALID_PARAMS",
                    format!("{} must be a non-negative integer", field),
                )
            })?;
            let n = u32::try_from(n).map_err(|_| {
                Response::error(
                    request_id.to_string(),
                    "INVALID_PARAMS",
                    format!("{} value {} exceeds u32 range", field, n),
                )
            })?;
            Ok(Some(n))
        }
    }
}
