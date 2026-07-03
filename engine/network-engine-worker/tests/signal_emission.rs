//! Integration test for the signal subscriber installed in the worker main loop.
//!
//! Proves the end-to-end Rust path: an engine warning during a commission
//! calculation surfaces as an NDJSON `signal` line on stdout, carrying the trace
//! context set from the request. See design-rationale 019 and Task 6.

mod common;

use std::collections::BTreeMap;

use network_engine::config::commission::CompressionMode;
use network_engine::config::{
    CommissionEligibility, CompressionConfig, LevelCommissionConfig, StructureConfig,
    UnilevelStructureConfig,
};
use network_engine::test_support::build_test_plan;

const STRUCTURE: &str = "Uni";
const TRACE_ID: &str = "0af7651916cd43dd8448eb211c80319c";
const SPAN_ID: &str = "b7ad6b7169203331";

/// Builds a unilevel plan whose compression is `SkipBelowRank` with no
/// `rank_threshold`. `calculate_unilevel` warns about this misconfiguration
/// during walk setup, which the installed subscriber turns into a signal.
///
/// Serialized from the typed struct so the adjacently-tagged `StructureConfig`
/// round-trips correctly (no `serde_json::Value` intermediate).
fn warn_triggering_plan_json() -> String {
    let structure = UnilevelStructureConfig {
        name: STRUCTURE.to_string(),
        level_commission: LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: None,
            max_depth: 5,
            rate_table: BTreeMap::new(),
        },
        compression: Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: None,
        }),
        pass_up: None,
    };
    let eligibility = CommissionEligibility {
        minimum_pv: 0.0,
        require_order_in_period: false,
        eligible_statuses: vec![],
        active_leg_tiers: vec![],
    };
    let plan = build_test_plan(eligibility, StructureConfig::Unilevel(structure), STRUCTURE);
    serde_json::to_string(&plan).expect("plan serializes")
}

#[test]
fn engine_warning_surfaces_as_signal_with_trace_context() {
    let mut child = common::spawn_worker();

    // Create the unilevel tree the calculation runs against.
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"tree","op":"create_tree","params":{{"structure":"{}","tree_type":"unilevel"}}}}"#,
            STRUCTURE
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "create_tree failed: {}",
        resp
    );

    // Load the warn-triggering plan.
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"plan","op":"load_plan","params":{}}}"#,
            warn_triggering_plan_json()
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "load_plan failed: {}", resp);

    // Calculate with a trace context on the request. Empty volume still reaches
    // the compression-config warning during walk setup.
    let request = format!(
        r#"{{"id":"calc","op":"calculate_unilevel","trace_id":"{}","span_id":"{}","params":{{"structure":"{}","snapshots":{{}},"volume":[]}}}}"#,
        TRACE_ID, SPAN_ID, STRUCTURE
    );
    let lines = common::send_collect(&mut child, &request);

    // Partition the collected output into the signal and the response.
    let mut signal = None;
    let mut response = None;
    for line in &lines {
        let value: serde_json::Value = serde_json::from_str(line).expect("line is valid JSON");
        match value.get("type").and_then(|t| t.as_str()) {
            Some("signal") => signal = Some(value),
            _ => response = Some(value),
        }
    }

    let signal = signal.expect("a signal line was emitted for the engine warning");
    assert_eq!(signal["level"], "warn");
    assert_eq!(
        signal["trace_id"], TRACE_ID,
        "signal carries the request trace_id"
    );
    assert_eq!(
        signal["span_id"], SPAN_ID,
        "signal carries the request span_id"
    );

    let response = response.expect("the calc response line was emitted");
    assert_eq!(response["id"], "calc");
    assert_eq!(response["ok"], true);

    drop(child.stdin.take());
    child.wait().unwrap();
}
