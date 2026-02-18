mod handlers;
mod protocol;
mod state;

use std::io::{self, BufRead, Write};
use std::panic;

use network_engine::config::CompensationPlan;
use protocol::{Request, Response};
use state::WorkerState;

fn dispatch(state: &mut WorkerState, request: &Request) -> Response {
    match request.op.as_str() {
        "ping" => Response::success(request.id.clone(), serde_json::json!("pong")),
        "load_plan" => handle_load_plan(state, request),
        // Tree mutations
        "add_root" => handlers::handle_add_root(state, request),
        "add_node" => handlers::handle_add_node(state, request),
        "remove_node" => handlers::handle_remove_node(state, request),
        // Tree queries
        "get_parent" => handlers::handle_get_parent(state, request),
        "get_children" => handlers::handle_get_children(state, request),
        "get_upline" => handlers::handle_get_upline(state, request),
        "get_downline" => handlers::handle_get_downline(state, request),
        "get_position" => handlers::handle_get_position(state, request),
        "is_descendant_of" => handlers::handle_is_descendant_of(state, request),
        // Commission calculations
        "calculate_unilevel" => handlers::handle_calculate_unilevel(state, request),
        _ => Response::error(
            request.id.clone(),
            "UNKNOWN_OP",
            format!("unknown operation: {}", request.op),
        ),
    }
}

fn handle_load_plan(state: &mut WorkerState, request: &Request) -> Response {
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

fn main() {
    let mut state = WorkerState::default();
    let stdin = io::stdin().lock();
    let mut stdout = io::stdout().lock();

    for line in stdin.lines() {
        let line = match line {
            Ok(l) => l,
            Err(e) => {
                eprintln!("stdin read error: {e}");
                break;
            }
        };

        let response = match serde_json::from_str::<Request>(&line) {
            Ok(request) => {
                // Catch panics so a bug in one handler doesn't crash the
                // long-lived worker process and break the Go subprocess connection.
                match panic::catch_unwind(panic::AssertUnwindSafe(|| {
                    dispatch(&mut state, &request)
                })) {
                    Ok(resp) => resp,
                    Err(_) => Response::error(
                        request.id.clone(),
                        "INTERNAL_ERROR",
                        "handler panicked unexpectedly",
                    ),
                }
            }
            Err(e) => Response::error(
                String::new(),
                "INVALID_REQUEST",
                format!("failed to parse request: {}", e),
            ),
        };

        let json = serde_json::to_string(&response).expect("response serialization is infallible");
        if writeln!(stdout, "{}", json).is_err() || stdout.flush().is_err() {
            break;
        }
    }
}
