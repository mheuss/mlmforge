use std::collections::HashMap;

use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_streamline};
use network_engine::config::streamline::StreamAssignmentMode;
use network_engine::config::{CompensationPlan, StreamlineStructureConfig};
use network_engine::streamline::StreamlineEngine;
use network_engine::streamline::engine::StreamlineConfig;
use uuid::Uuid;

use super::common::{extract_structure_name, parse_params, parse_u32_param, parse_uuid};
use crate::protocol::{Request, Response};
use crate::state::{TreeInstance, WorkerState};

// --- Streamline helpers ---

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
    e: network_engine::streamline::StreamlineError,
) -> Response {
    use network_engine::streamline::StreamlineError;
    let code = match &e {
        StreamlineError::MemberAlreadyExists(_) => "MEMBER_ALREADY_EXISTS",
        StreamlineError::MemberNotFound(_) => "MEMBER_NOT_FOUND",
        StreamlineError::SponsorNotFound(_) => "SPONSOR_NOT_FOUND",
        StreamlineError::StreamNotFound(_) => "STREAM_NOT_FOUND",
        StreamlineError::StreamFrozen(_) => "STREAM_FROZEN",
        StreamlineError::SponsorDoesNotOwnStream(_, _) => "SPONSOR_NOT_OWNER",
        StreamlineError::NoStreamsAvailable => "NO_STREAMS_AVAILABLE",
        StreamlineError::NoOwnedStreams(_) => "NO_OWNED_STREAMS",
        StreamlineError::StreamChoiceNotAllowed => "STREAM_CHOICE_NOT_ALLOWED",
        StreamlineError::TreeError(_) => "TREE_ERROR",
    };
    Response::error(request_id.to_string(), code, e.to_string())
}

// --- Streamline handlers ---

pub(crate) fn handle_create_streamline(state: &mut WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_streamline_add_member(state: &mut WorkerState, request: &Request) -> Response {
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
    let stream_id_override = match parse_u32_param(&params, "stream_id_override", &request.id) {
        Ok(v) => v,
        Err(resp) => return resp,
    };

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

pub(crate) fn handle_streamline_remove_member(
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

pub(crate) fn handle_streamline_expand_streams(
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
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let total_allowed = match parse_u32_param(&params, "total_allowed", &request.id) {
        Ok(Some(v)) => v,
        Ok(None) => {
            return Response::error(request.id.clone(), "MISSING_PARAM", "missing total_allowed");
        }
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

    match engine.expand_streams(user_id, total_allowed, timestamp) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result).expect("serialization infallible"),
        ),
        Err(e) => streamline_error_to_response(&request.id, e),
    }
}

pub(crate) fn handle_streamline_update_allowance(
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
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let total_allowed = match parse_u32_param(&params, "total_allowed", &request.id) {
        Ok(Some(v)) => v,
        Ok(None) => {
            return Response::error(request.id.clone(), "MISSING_PARAM", "missing total_allowed");
        }
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

    match engine.update_stream_allowance(user_id, total_allowed, timestamp) {
        Ok(result) => Response::success(
            request.id.clone(),
            serde_json::to_value(&result).expect("serialization infallible"),
        ),
        Err(e) => streamline_error_to_response(&request.id, e),
    }
}

pub(crate) fn handle_streamline_list_streams(state: &WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_streamline_get_member(state: &WorkerState, request: &Request) -> Response {
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

pub(crate) fn handle_streamline_get_stream(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match extract_structure_name(&params, &request.id) {
        Ok(s) => s,
        Err(resp) => return resp,
    };
    let stream_id = match parse_u32_param(&params, "stream_id", &request.id) {
        Ok(Some(v)) => v,
        Ok(None) => {
            return Response::error(request.id.clone(), "MISSING_PARAM", "missing stream_id");
        }
        Err(resp) => return resp,
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

pub(crate) fn handle_calculate_streamline(state: &WorkerState, request: &Request) -> Response {
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
