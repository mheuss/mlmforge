use std::io::{BufRead, BufReader, Write};
use std::process::{Command, Stdio};

fn spawn_worker() -> std::process::Child {
    Command::new(env!("CARGO_BIN_EXE_network-engine-worker"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("failed to spawn worker")
}

fn send_receive(child: &mut std::process::Child, request: &str) -> String {
    let stdin = child.stdin.as_mut().unwrap();
    writeln!(stdin, "{}", request).unwrap();
    stdin.flush().unwrap();

    let stdout = child.stdout.as_mut().unwrap();
    let mut reader = BufReader::new(stdout);
    let mut line = String::new();
    reader.read_line(&mut line).unwrap();
    line.trim().to_string()
}

#[test]
fn ping_pong() {
    let mut child = spawn_worker();
    let response = send_receive(&mut child, r#"{"id":"1","op":"ping"}"#);
    assert!(response.contains(r#""ok":true"#));
    assert!(response.contains(r#""pong""#));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn unknown_op_returns_error() {
    let mut child = spawn_worker();
    let response = send_receive(&mut child, r#"{"id":"2","op":"bogus"}"#);
    assert!(response.contains(r#""ok":false"#));
    assert!(response.contains("UNKNOWN_OP"));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn load_plan_with_invalid_params_returns_error() {
    let mut child = spawn_worker();
    let response = send_receive(
        &mut child,
        r#"{"id":"3","op":"load_plan","params":{"not":"a plan"}}"#,
    );
    assert!(response.contains(r#""ok":false"#));
    assert!(response.contains("INVALID_PLAN"));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn invalid_json_returns_error() {
    let mut child = spawn_worker();
    let response = send_receive(&mut child, "not json at all");
    assert!(response.contains(r#""ok":false"#));
    assert!(response.contains("INVALID_REQUEST"));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn add_root_and_node() {
    let mut child = spawn_worker();

    let resp = send_receive(
        &mut child,
        r#"{"id":"1","op":"add_root","params":{"user_id":"00000000-0000-0000-0000-000000000001","enrolled_at":100}}"#,
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""added":true"#));

    let resp = send_receive(
        &mut child,
        r#"{"id":"2","op":"add_node","params":{"user_id":"00000000-0000-0000-0000-000000000002","parent_id":"00000000-0000-0000-0000-000000000001","enrolled_at":200}}"#,
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""added":true"#));

    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn add_node_without_tree_returns_error() {
    let mut child = spawn_worker();
    let resp = send_receive(
        &mut child,
        r#"{"id":"1","op":"add_node","params":{"user_id":"00000000-0000-0000-0000-000000000002","parent_id":"00000000-0000-0000-0000-000000000001"}}"#,
    );
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("NO_TREE"));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn remove_node_success() {
    let mut child = spawn_worker();

    // Build a small tree: root -> child
    send_receive(
        &mut child,
        r#"{"id":"1","op":"add_root","params":{"user_id":"00000000-0000-0000-0000-000000000001","enrolled_at":100}}"#,
    );
    send_receive(
        &mut child,
        r#"{"id":"2","op":"add_node","params":{"user_id":"00000000-0000-0000-0000-000000000002","parent_id":"00000000-0000-0000-0000-000000000001","enrolled_at":200}}"#,
    );

    // Remove the leaf node
    let resp = send_receive(
        &mut child,
        r#"{"id":"3","op":"remove_node","params":{"user_id":"00000000-0000-0000-0000-000000000002"}}"#,
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""removed":true"#));

    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn remove_node_without_tree_returns_error() {
    let mut child = spawn_worker();
    let resp = send_receive(
        &mut child,
        r#"{"id":"1","op":"remove_node","params":{"user_id":"00000000-0000-0000-0000-000000000001"}}"#,
    );
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("NO_TREE"));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn add_root_missing_user_id_returns_error() {
    let mut child = spawn_worker();
    let resp = send_receive(&mut child, r#"{"id":"1","op":"add_root","params":{}}"#);
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("MISSING_PARAM"));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn add_node_invalid_uuid_returns_error() {
    let mut child = spawn_worker();

    // First add a root so the tree exists
    send_receive(
        &mut child,
        r#"{"id":"1","op":"add_root","params":{"user_id":"00000000-0000-0000-0000-000000000001","enrolled_at":100}}"#,
    );

    let resp = send_receive(
        &mut child,
        r#"{"id":"2","op":"add_node","params":{"user_id":"not-a-uuid","parent_id":"00000000-0000-0000-0000-000000000001"}}"#,
    );
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("INVALID_UUID"));
    drop(child.stdin.take());
    child.wait().unwrap();
}

// --- Tree query handler integration tests ---
//
// All query tests share the same 3-node chain: root(001) -> child(002) -> grandchild(003).
// Each test spawns a fresh worker to avoid shared state.

const ROOT: &str = "00000000-0000-0000-0000-000000000001";
const CHILD: &str = "00000000-0000-0000-0000-000000000002";
const GRANDCHILD: &str = "00000000-0000-0000-0000-000000000003";

/// Builds a 3-node chain: root -> child -> grandchild.
fn build_three_node_chain(child: &mut std::process::Child) {
    send_receive(
        child,
        &format!(
            r#"{{"id":"setup-1","op":"add_root","params":{{"user_id":"{}","enrolled_at":100}}}}"#,
            ROOT
        ),
    );
    send_receive(
        child,
        &format!(
            r#"{{"id":"setup-2","op":"add_node","params":{{"user_id":"{}","parent_id":"{}","enrolled_at":200}}}}"#,
            CHILD, ROOT
        ),
    );
    send_receive(
        child,
        &format!(
            r#"{{"id":"setup-3","op":"add_node","params":{{"user_id":"{}","parent_id":"{}","enrolled_at":300}}}}"#,
            GRANDCHILD, CHILD
        ),
    );
}

#[test]
fn get_parent_of_grandchild_returns_child() {
    let mut worker = spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q1","op":"get_parent","params":{{"user_id":"{}"}}}}"#,
            GRANDCHILD
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(CHILD));
    assert!(resp.contains(r#""depth":1"#));

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn get_parent_of_root_returns_null() {
    let mut worker = spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q2","op":"get_parent","params":{{"user_id":"{}"}}}}"#,
            ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""result":null"#));

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn get_children_of_root_returns_child() {
    let mut worker = spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q3","op":"get_children","params":{{"user_id":"{}"}}}}"#,
            ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(CHILD));
    // Root's children should not include grandchild (that's child's child)
    assert!(!resp.contains(GRANDCHILD));

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn get_upline_of_grandchild_returns_chain_to_root() {
    let mut worker = spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q4","op":"get_upline","params":{{"user_id":"{}","depth":0}}}}"#,
            GRANDCHILD
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    // Upline order: [child, root] — immediate parent first
    assert!(resp.contains(CHILD));
    assert!(resp.contains(ROOT));

    // Verify the response is a JSON array with 2 elements by parsing
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    let result = parsed["result"].as_array().unwrap();
    assert_eq!(result.len(), 2);
    assert_eq!(result[0]["user_id"].as_str().unwrap(), CHILD);
    assert_eq!(result[1]["user_id"].as_str().unwrap(), ROOT);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn get_downline_of_root_with_depth_1_returns_child_only() {
    let mut worker = spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q5","op":"get_downline","params":{{"user_id":"{}","depth":1}}}}"#,
            ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#));

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    let result = parsed["result"].as_array().unwrap();
    assert_eq!(result.len(), 1);
    assert_eq!(result[0]["user_id"].as_str().unwrap(), CHILD);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn get_position_of_child_returns_correct_metadata() {
    let mut worker = spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q6","op":"get_position","params":{{"user_id":"{}"}}}}"#,
            CHILD
        ),
    );
    assert!(resp.contains(r#""ok":true"#));

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    let result = &parsed["result"];
    assert_eq!(result["user_id"].as_str().unwrap(), CHILD);
    assert_eq!(result["parent_user_id"].as_str().unwrap(), ROOT);
    assert_eq!(result["position"].as_u64().unwrap(), 0); // first child of root
    assert_eq!(result["depth"].as_u64().unwrap(), 1);
    assert_eq!(result["child_count"].as_u64().unwrap(), 1); // grandchild
    assert_eq!(result["enrolled_at"].as_i64().unwrap(), 200);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn is_descendant_of_grandchild_under_root_returns_true() {
    let mut worker = spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q7","op":"is_descendant_of","params":{{"user_id":"{}","ancestor_id":"{}"}}}}"#,
            GRANDCHILD, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""is_descendant":true"#));

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn is_descendant_of_root_under_grandchild_returns_false() {
    let mut worker = spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q8","op":"is_descendant_of","params":{{"user_id":"{}","ancestor_id":"{}"}}}}"#,
            ROOT, GRANDCHILD
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""is_descendant":false"#));

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn query_without_tree_returns_no_tree_error() {
    let mut worker = spawn_worker();

    // Try all query ops without initializing a tree
    for op in [
        "get_parent",
        "get_children",
        "get_upline",
        "get_downline",
        "get_position",
        "is_descendant_of",
    ] {
        let params = if op == "is_descendant_of" {
            format!(r#"{{"user_id":"{}","ancestor_id":"{}"}}"#, ROOT, CHILD)
        } else {
            format!(r#"{{"user_id":"{}"}}"#, ROOT)
        };
        let resp = send_receive(
            &mut worker,
            &format!(r#"{{"id":"err-{}","op":"{}","params":{}}}"#, op, op, params),
        );
        assert!(
            resp.contains("NO_TREE"),
            "{} should return NO_TREE without initialized tree, got: {}",
            op,
            resp
        );
    }

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

// --- Commission calculation integration tests ---
//
// These tests exercise the full pipeline: load a plan, build a tree,
// send volume, verify commission earnings.

/// A minimal compensation plan JSON with a single "Test" unilevel structure.
///
/// - One rank: "member" with rate 0.05 at levels 1-3
/// - Eligibility: min PV 0, everyone eligible
/// - No compression
///
/// Modeled after `build_test_plan` in `unilevel_commission_properties.rs`.
const TEST_PLAN_JSON: &str = r#"{
    "name": "Integration Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "unilevel",
            "config": {
                "name": "Test",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": {
                        "member": { "1": 0.05, "2": 0.05, "3": 0.05 }
                    }
                },
                "compression": null
            }
        }
    ],
    "period": {
        "length": "month",
        "start_date": "2026-03-01",
        "payout_lag_days": 14
    },
    "volume": {
        "inhibit_signup_volume": false,
        "base_currency": "USD",
        "volume_to_dollar_multiplier": 1.0,
        "deduct_qualifying_volume": false
    },
    "ranks": [
        {
            "name": "member",
            "ordinal": 1,
            "qualification": {
                "structures": [],
                "required_products": []
            },
            "qualified_structures": ["Test"],
            "demotion_policy": "promotion_only"
        }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": {
        "min_personal_volume": 0.0,
        "require_order_in_period": false,
        "eligible_statuses": [],
        "active_leg_tiers": []
    },
    "bonuses": {
        "matching": null,
        "sponsor": null,
        "fast_start": null,
        "rank_advancement": null,
        "leadership_development": null,
        "infinity": null,
        "lifestyle": null,
        "pool": null,
        "matrix_completion": null,
        "position": null,
        "board_cycling": null,
        "pass_up": null
    },
    "payout": {
        "base_currency": "USD",
        "minimum_amount": 50.0,
        "split_payouts_enabled": true,
        "methods": [
            { "type": "bank_transfer", "fee": 2.50 }
        ]
    },
    "caps": {
        "per_distributor_per_period": null,
        "company_payout_cap_percent": 0.42,
        "cap_enforcement": "pro_rata",
        "clawback_on_refund": false
    },
    "placement": {
        "donated_placement": null,
        "holding_tank": null,
        "binary_placement": null
    }
}"#;

/// Loads the test plan into the worker. Asserts success.
fn load_test_plan(worker: &mut std::process::Child) {
    // The wire protocol sends one JSON object per line. We need the plan JSON
    // on a single line inside the "params" field. Remove all newlines and
    // excess whitespace by minifying.
    let minified: String = TEST_PLAN_JSON
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let request = format!(
        r#"{{"id":"load-plan","op":"load_plan","params":{}}}"#,
        minified
    );
    let resp = send_receive(worker, &request);
    assert!(resp.contains(r#""ok":true"#), "load_plan failed: {}", resp);
}

#[test]
fn calculate_unilevel_three_node_chain() {
    let mut worker = spawn_worker();

    // 1. Load plan
    load_test_plan(&mut worker);

    // 2. Build tree: root(001) -> mid(002) -> leaf(003)
    build_three_node_chain(&mut worker);

    // 3. Calculate commissions
    //    Volume source: leaf(003) generates 100 CV
    //    Expected walk: mid(002) at level 1, root(001) at level 2
    //    Rate: 0.05 at all levels, broad_pct: 0.40, multiplier: 1.0
    //    Dollar amount: 100 * 0.40 * 1.0 * 0.05 = 2.0 for each
    let snap =
        r#"{"rank":"member","personal_volume":100.0,"status":"active","has_order_in_period":true}"#;
    let params = format!(
        r#"{{"structure_name":"Test","snapshots":{{"{root}":{snap},"{child}":{snap},"{gc}":{snap}}},"volume":[{{"source_id":"{gc}","cv_amount":100.0}}]}}"#,
        root = ROOT,
        child = CHILD,
        gc = GRANDCHILD,
        snap = snap,
    );
    let request = format!(
        r#"{{"id":"calc-1","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = send_receive(&mut worker, &request);

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap(),
        "calculate_unilevel failed: {}",
        resp
    );

    let earnings = parsed["result"].as_array().unwrap();
    assert_eq!(earnings.len(), 2, "expected 2 earnings, got: {}", resp);

    // Find mid(002) earning at level 1
    let mid_earning = earnings
        .iter()
        .find(|e| e["earner_id"].as_str().unwrap() == CHILD)
        .expect("mid should have earned");
    assert_eq!(mid_earning["level"].as_u64().unwrap(), 1);
    assert_eq!(mid_earning["source_id"].as_str().unwrap(), GRANDCHILD);
    let mid_dollar = mid_earning["dollar_amount"].as_f64().unwrap();
    assert!(
        (mid_dollar - 2.0).abs() < f64::EPSILON,
        "mid dollar_amount should be 2.0, got {}",
        mid_dollar
    );

    // Find root(001) earning at level 2
    let root_earning = earnings
        .iter()
        .find(|e| e["earner_id"].as_str().unwrap() == ROOT)
        .expect("root should have earned");
    assert_eq!(root_earning["level"].as_u64().unwrap(), 2);
    assert_eq!(root_earning["source_id"].as_str().unwrap(), GRANDCHILD);
    let root_dollar = root_earning["dollar_amount"].as_f64().unwrap();
    assert!(
        (root_dollar - 2.0).abs() < f64::EPSILON,
        "root dollar_amount should be 2.0, got {}",
        root_dollar
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_unilevel_without_plan_returns_no_plan() {
    let mut worker = spawn_worker();

    // Build a tree but don't load a plan
    build_three_node_chain(&mut worker);

    let params = r#"{"structure_name":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-err","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("NO_PLAN"), "expected NO_PLAN, got: {}", resp);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_unilevel_without_tree_returns_no_tree() {
    let mut worker = spawn_worker();

    // Load a plan but don't build a tree
    load_test_plan(&mut worker);

    let params = r#"{"structure_name":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-err","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("NO_TREE"), "expected NO_TREE, got: {}", resp);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_unilevel_unknown_structure_returns_not_found() {
    let mut worker = spawn_worker();

    load_test_plan(&mut worker);
    build_three_node_chain(&mut worker);

    let params = r#"{"structure_name":"Nonexistent","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-err","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("STRUCTURE_NOT_FOUND"),
        "expected STRUCTURE_NOT_FOUND, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_unilevel_invalid_params_returns_error() {
    let mut worker = spawn_worker();

    load_test_plan(&mut worker);
    build_three_node_chain(&mut worker);

    let request = r#"{"id":"calc-err","op":"calculate_unilevel","params":{"bad":"data"}}"#;
    let resp = send_receive(&mut worker, request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("INVALID_PARAMS"),
        "expected INVALID_PARAMS, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Verifies the worker process survives malformed JSON and continues
/// processing subsequent valid requests.
#[test]
fn malformed_json_does_not_crash_worker() {
    let mut child = spawn_worker();
    let resp = send_receive(&mut child, "not json at all");
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("INVALID_REQUEST"));
    // Worker should still be alive — send a follow-up ping
    let resp2 = send_receive(&mut child, r#"{"id":"2","op":"ping"}"#);
    assert!(resp2.contains(r#""ok":true"#));
    assert!(resp2.contains(r#""pong""#));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn calculate_unilevel_empty_volume_returns_empty_earnings() {
    let mut worker = spawn_worker();

    load_test_plan(&mut worker);
    build_three_node_chain(&mut worker);

    let params = r#"{"structure_name":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-empty","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = send_receive(&mut worker, &request);

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap());
    let earnings = parsed["result"].as_array().unwrap();
    assert!(
        earnings.is_empty(),
        "expected empty earnings, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}
