mod handlers;
mod protocol;
mod signals;
mod state;

use std::io::{self, BufRead, Write};
use std::panic;

use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;

use protocol::{Request, Response};
use state::WorkerState;

fn dispatch(state: &mut WorkerState, request: &Request) -> Response {
    match request.op.as_str() {
        "ping" => Response::success(request.id.clone(), serde_json::json!("pong")),
        "load_plan" => handlers::common::handle_load_plan(state, request),
        // Tree lifecycle
        "create_tree" => handlers::tree::handle_create_tree(state, request),
        // Tree mutations
        "add_root" => handlers::tree::handle_add_root(state, request),
        "add_node" => handlers::tree::handle_add_node(state, request),
        "add_node_at" => handlers::tree::handle_add_node_at(state, request),
        "remove_node" => handlers::tree::handle_remove_node(state, request),
        "place_from_tank" => handlers::tree::handle_place_from_tank(state, request),
        "get_holding_tank" => handlers::tree::handle_get_holding_tank(state, request),
        // Tree queries
        "get_parent" => handlers::tree::handle_get_parent(state, request),
        "get_children" => handlers::tree::handle_get_children(state, request),
        "get_upline" => handlers::tree::handle_get_upline(state, request),
        "get_downline" => handlers::tree::handle_get_downline(state, request),
        "get_position" => handlers::tree::handle_get_position(state, request),
        "is_descendant_of" => handlers::tree::handle_is_descendant_of(state, request),
        // Sponsor queries
        "get_sponsor" => handlers::tree::handle_get_sponsor(state, request),
        "get_sponsor_upline" => handlers::tree::handle_get_sponsor_upline(state, request),
        "get_sponsored" => handlers::tree::handle_get_sponsored(state, request),
        // Commission calculations
        "calculate_unilevel" => handlers::commission::handle_calculate_unilevel(state, request),
        "calculate_binary_pairing" => {
            handlers::commission::handle_calculate_binary_pairing(state, request)
        }
        "calculate_generation" => handlers::commission::handle_calculate_generation(state, request),
        "calculate_matrix" => handlers::commission::handle_calculate_matrix(state, request),
        "calculate_stairstep" => handlers::commission::handle_calculate_stairstep(state, request),
        // Board plan operations
        "create_board_plan" => handlers::board_plan::handle_create_board_plan(state, request),
        "board_add_member" => handlers::board_plan::handle_board_add_member(state, request),
        "board_remove_member" => handlers::board_plan::handle_board_remove_member(state, request),
        "board_compress_inactive" => {
            handlers::board_plan::handle_board_compress_inactive(state, request)
        }
        "board_detect_stalled" => handlers::board_plan::handle_board_detect_stalled(state, request),
        "board_dissolve" => handlers::board_plan::handle_board_dissolve(state, request),
        "board_get_state" => handlers::board_plan::handle_board_get_state(state, request),
        "board_get_member" => handlers::board_plan::handle_board_get_member(state, request),
        "board_list" => handlers::board_plan::handle_board_list(state, request),
        "board_calculate_commissions" => {
            handlers::board_plan::handle_board_calculate_commissions(state, request)
        }
        // Streamline operations
        "create_streamline" => handlers::streamline::handle_create_streamline(state, request),
        "streamline_add_member" => {
            handlers::streamline::handle_streamline_add_member(state, request)
        }
        "streamline_remove_member" => {
            handlers::streamline::handle_streamline_remove_member(state, request)
        }
        "streamline_expand_streams" => {
            handlers::streamline::handle_streamline_expand_streams(state, request)
        }
        "streamline_update_allowance" => {
            handlers::streamline::handle_streamline_update_allowance(state, request)
        }
        "streamline_list_streams" => {
            handlers::streamline::handle_streamline_list_streams(state, request)
        }
        "streamline_get_member" => {
            handlers::streamline::handle_streamline_get_member(state, request)
        }
        "streamline_get_stream" => {
            handlers::streamline::handle_streamline_get_stream(state, request)
        }
        "calculate_streamline" => handlers::streamline::handle_calculate_streamline(state, request),
        // Snapshot operations
        "take_snapshot" => handlers::snapshot::handle_take_snapshot(state, request),
        "restore_snapshot" => handlers::snapshot::handle_restore_snapshot(state, request),
        // Rank evaluation
        "evaluate_ranks" => handlers::rank::handle_evaluate_ranks(state, request),
        _ => Response::error(
            request.id.clone(),
            "UNKNOWN_OP",
            format!("unknown operation: {}", request.op),
        ),
    }
}

fn main() {
    // The worker emits engine warnings as NDJSON `signal` messages on stdout,
    // interleaved with protocol responses. Go demuxes by the "type":"signal"
    // discriminator. stdout is a LineWriter behind a reentrant lock, so a signal
    // written during dispatch flushes ahead of the response on the same thread.
    // This assumes a single-threaded worker: `main` holds the stdout lock for the
    // whole loop, so a background thread emitting an event would block. Revisit
    // if concurrency is ever added. See design-rationale 019.
    tracing_subscriber::registry()
        .with(signals::SignalLayer::new(std::io::stdout))
        .init();
    // Belt-and-suspenders: `.init()` already installs the log->tracing bridge via
    // tracing-subscriber's default `tracing-log` feature. This keeps dependency
    // `log` records routing even if that feature is turned off. No-op when the
    // bridge is already installed.
    let _ = tracing_log::LogTracer::init();

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
                // Set the trace context so signals emitted during dispatch carry
                // the caller's trace_id/span_id. Cleared after catch_unwind, not
                // inside it, so the clear runs even when a handler panics. That
                // keeps a stale context from leaking into the next request on this
                // long-lived thread.
                signals::set_trace_context(request.trace_id.clone(), request.span_id.clone());
                // Catch panics so a bug in one handler doesn't crash the
                // long-lived worker process and break the Go subprocess connection.
                let resp = match panic::catch_unwind(panic::AssertUnwindSafe(|| {
                    dispatch(&mut state, &request)
                })) {
                    Ok(resp) => resp,
                    Err(_) => Response::error(
                        request.id.clone(),
                        "INTERNAL_ERROR",
                        "handler panicked unexpectedly",
                    ),
                };
                signals::clear_trace_context();
                resp
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
