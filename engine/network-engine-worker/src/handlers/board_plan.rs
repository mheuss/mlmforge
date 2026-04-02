use std::collections::HashMap;

use network_engine::board_plan::BoardPlanEngine;
use network_engine::commission::calculate_board_commissions;
use network_engine::config::board_plan::BoardPlanConfig;
use uuid::Uuid;

use super::common::{extract_structure_name, parse_params, parse_uuid};
use crate::protocol::{Request, Response};
use crate::state::{TreeInstance, WorkerState};

// --- Board plan helpers ---

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

// --- Board plan handlers ---

/// Creates a `BoardPlanEngine` and stores it as a `TreeInstance::BoardPlan`.
///
/// Params: structure, width, height, config (BoardPlanConfig), timestamp.
pub(crate) fn handle_create_board_plan(state: &mut WorkerState, request: &Request) -> Response {
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
pub(crate) fn handle_board_add_member(state: &mut WorkerState, request: &Request) -> Response {
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
pub(crate) fn handle_board_remove_member(state: &mut WorkerState, request: &Request) -> Response {
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
pub(crate) fn handle_board_compress_inactive(
    state: &mut WorkerState,
    request: &Request,
) -> Response {
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
pub(crate) fn handle_board_detect_stalled(state: &WorkerState, request: &Request) -> Response {
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
pub(crate) fn handle_board_dissolve(state: &mut WorkerState, request: &Request) -> Response {
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
pub(crate) fn handle_board_get_state(state: &WorkerState, request: &Request) -> Response {
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
pub(crate) fn handle_board_get_member(state: &WorkerState, request: &Request) -> Response {
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
pub(crate) fn handle_board_list(state: &WorkerState, request: &Request) -> Response {
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
pub(crate) fn handle_board_calculate_commissions(
    _state: &WorkerState,
    request: &Request,
) -> Response {
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
