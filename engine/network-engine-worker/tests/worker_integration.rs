mod common;

use network_engine::config::StructureConfig;

const ROOT: &str = "00000000-0000-0000-0000-000000000001";
const CHILD: &str = "00000000-0000-0000-0000-000000000002";
const GRANDCHILD: &str = "00000000-0000-0000-0000-000000000003";

/// The default tree name used across integration tests.
const TREE_NAME: &str = "Test";

/// Creates a named unilevel tree on the worker.
fn create_tree(child: &mut std::process::Child, name: &str) {
    let resp = common::send_receive(
        child,
        &format!(
            r#"{{"id":"setup-tree","op":"create_tree","params":{{"structure":"{}","tree_type":"unilevel"}}}}"#,
            name
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "create_tree failed: {}",
        resp
    );
}

/// Builds a 3-node chain: root -> child -> grandchild.
/// Creates the tree first, then adds nodes with sponsor_id.
fn build_three_node_chain(child: &mut std::process::Child) {
    create_tree(child, TREE_NAME);
    let resp = common::send_receive(
        child,
        &format!(
            r#"{{"id":"setup-1","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            TREE_NAME, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
    let resp = common::send_receive(
        child,
        &format!(
            r#"{{"id":"setup-2","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":200}}}}"#,
            TREE_NAME, CHILD, ROOT, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
    let resp = common::send_receive(
        child,
        &format!(
            r#"{{"id":"setup-3","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":300}}}}"#,
            TREE_NAME, GRANDCHILD, CHILD, CHILD
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
}

#[test]
fn ping_pong() {
    let mut child = common::spawn_worker();
    let response = common::send_receive(&mut child, r#"{"id":"1","op":"ping"}"#);
    assert!(response.contains(r#""ok":true"#));
    assert!(response.contains(r#""pong""#));

    let parsed: serde_json::Value = serde_json::from_str(&response).unwrap();
    assert_eq!(parsed["id"], "1");

    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn unknown_op_returns_error() {
    let mut child = common::spawn_worker();
    let response = common::send_receive(&mut child, r#"{"id":"2","op":"bogus"}"#);
    assert!(response.contains(r#""ok":false"#));
    assert!(response.contains("UNKNOWN_OP"));

    let parsed: serde_json::Value = serde_json::from_str(&response).unwrap();
    assert_eq!(parsed["id"], "2");

    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn load_plan_with_invalid_params_returns_error() {
    let mut child = common::spawn_worker();
    let response = common::send_receive(
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
    let mut child = common::spawn_worker();
    let response = common::send_receive(&mut child, "not json at all");
    assert!(response.contains(r#""ok":false"#));
    assert!(response.contains("INVALID_REQUEST"));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn add_root_and_node() {
    let mut child = common::spawn_worker();
    create_tree(&mut child, TREE_NAME);

    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"1","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            TREE_NAME, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""added":true"#));

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert_eq!(parsed["id"], "1");

    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"2","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":200}}}}"#,
            TREE_NAME, CHILD, ROOT, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""added":true"#));

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert_eq!(parsed["id"], "2");

    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn add_node_without_tree_returns_error() {
    let mut child = common::spawn_worker();
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"1","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":200}}}}"#,
            TREE_NAME, CHILD, ROOT, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("STRUCTURE_NOT_FOUND"),
        "expected STRUCTURE_NOT_FOUND, got: {}",
        resp
    );

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert_eq!(parsed["id"], "1");

    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn remove_node_success() {
    let mut child = common::spawn_worker();
    create_tree(&mut child, TREE_NAME);

    // Build a small tree: root -> child
    common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"1","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            TREE_NAME, ROOT
        ),
    );
    common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"2","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":200}}}}"#,
            TREE_NAME, CHILD, ROOT, ROOT
        ),
    );

    // Remove the leaf node
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"3","op":"remove_node","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            TREE_NAME, CHILD
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""removed":true"#));

    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn remove_node_without_tree_returns_error() {
    let mut child = common::spawn_worker();
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"1","op":"remove_node","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            TREE_NAME, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("STRUCTURE_NOT_FOUND"),
        "expected STRUCTURE_NOT_FOUND, got: {}",
        resp
    );
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn add_root_missing_user_id_returns_error() {
    let mut child = common::spawn_worker();
    let resp = common::send_receive(&mut child, r#"{"id":"1","op":"add_root","params":{}}"#);
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("MISSING_PARAM"));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn add_node_invalid_uuid_returns_error() {
    let mut child = common::spawn_worker();
    create_tree(&mut child, TREE_NAME);

    // First add a root so the tree exists
    common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"1","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            TREE_NAME, ROOT
        ),
    );

    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"2","op":"add_node","params":{{"structure":"{}","user_id":"not-a-uuid","parent_id":"{}","sponsor_id":"{}","enrolled_at":200}}}}"#,
            TREE_NAME, ROOT, ROOT
        ),
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

#[test]
fn get_parent_of_grandchild_returns_child() {
    let mut worker = common::spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q1","op":"get_parent","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            TREE_NAME, GRANDCHILD
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
    let mut worker = common::spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q2","op":"get_parent","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            TREE_NAME, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""result":null"#));

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn get_children_of_root_returns_child() {
    let mut worker = common::spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q3","op":"get_children","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            TREE_NAME, ROOT
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
    let mut worker = common::spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q4","op":"get_upline","params":{{"structure":"{}","user_id":"{}","depth":0}}}}"#,
            TREE_NAME, GRANDCHILD
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    // Upline order: [child, root] -- immediate parent first
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
    let mut worker = common::spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q5","op":"get_downline","params":{{"structure":"{}","user_id":"{}","depth":1}}}}"#,
            TREE_NAME, ROOT
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
    let mut worker = common::spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q6","op":"get_position","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            TREE_NAME, CHILD
        ),
    );
    assert!(resp.contains(r#""ok":true"#));

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    let result = &parsed["result"];
    assert_eq!(result["user_id"].as_str().unwrap(), CHILD);
    assert_eq!(result["parent_user_id"].as_str().unwrap(), ROOT);
    assert_eq!(result["sponsor_user_id"].as_str().unwrap(), ROOT);
    assert_eq!(result["position"].as_u64().unwrap(), 0); // first child of root
    assert_eq!(result["depth"].as_u64().unwrap(), 1);
    assert_eq!(result["child_count"].as_u64().unwrap(), 1); // grandchild
    assert_eq!(result["enrolled_at"].as_i64().unwrap(), 200);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn is_descendant_of_grandchild_under_root_returns_true() {
    let mut worker = common::spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q7","op":"is_descendant_of","params":{{"structure":"{}","user_id":"{}","ancestor_id":"{}"}}}}"#,
            TREE_NAME, GRANDCHILD, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""is_descendant":true"#));

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn is_descendant_of_root_under_grandchild_returns_false() {
    let mut worker = common::spawn_worker();
    build_three_node_chain(&mut worker);

    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"q8","op":"is_descendant_of","params":{{"structure":"{}","user_id":"{}","ancestor_id":"{}"}}}}"#,
            TREE_NAME, ROOT, GRANDCHILD
        ),
    );
    assert!(resp.contains(r#""ok":true"#));
    assert!(resp.contains(r#""is_descendant":false"#));

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn query_without_tree_returns_structure_not_found() {
    let mut worker = common::spawn_worker();

    // Try all query ops without creating a tree. All should return STRUCTURE_NOT_FOUND.
    for op in [
        "get_parent",
        "get_children",
        "get_upline",
        "get_downline",
        "get_position",
        "is_descendant_of",
        "get_sponsor",
        "get_sponsor_upline",
        "get_sponsored",
    ] {
        let params = if op == "is_descendant_of" {
            format!(
                r#"{{"structure":"{}","user_id":"{}","ancestor_id":"{}"}}"#,
                TREE_NAME, ROOT, CHILD
            )
        } else {
            format!(r#"{{"structure":"{}","user_id":"{}"}}"#, TREE_NAME, ROOT)
        };
        let resp = common::send_receive(
            &mut worker,
            &format!(r#"{{"id":"err-{}","op":"{}","params":{}}}"#, op, op, params),
        );
        assert!(
            resp.contains("STRUCTURE_NOT_FOUND"),
            "{} should return STRUCTURE_NOT_FOUND without created tree, got: {}",
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
        "board_cycling": null
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
    let resp = common::send_receive(worker, &request);
    assert!(resp.contains(r#""ok":true"#), "load_plan failed: {}", resp);
}

/// Minifies a plan JSON body and sends it via `load_plan`, returning the raw
/// response. Unlike `load_test_plan`, it does not assert success — the
/// validation-gate tests need to inspect the rejection.
fn send_load_plan(worker: &mut std::process::Child, plan_json: &str) -> String {
    let minified: String = plan_json
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let request = format!(
        r#"{{"id":"load-plan","op":"load_plan","params":{}}}"#,
        minified
    );
    common::send_receive(worker, &request)
}

#[test]
fn load_plan_accepts_valid_baseline_plan() {
    // Guards against the HEU-517 validator over-rejecting the known-good plan.
    let mut worker = common::spawn_worker();
    let resp = send_load_plan(&mut worker, TEST_PLAN_JSON);
    assert!(
        resp.contains(r#""ok":true"#),
        "baseline plan should load: {}",
        resp
    );
    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn load_plan_rejects_unsupported_version() {
    let mut worker = common::spawn_worker();
    // A valid future-version plan is not malformed, so it gets its own code.
    let plan = TEST_PLAN_JSON.replace(r#""version": 1"#, r#""version": 2"#);
    let resp = send_load_plan(&mut worker, &plan);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("UNSUPPORTED_PLAN_VERSION"),
        "expected UNSUPPORTED_PLAN_VERSION, got: {}",
        resp
    );
    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn load_plan_rejects_out_of_range_percent() {
    let mut worker = common::spawn_worker();
    // broad_commission_percent must be a fraction in [0, 1]; 1.5 is out of range.
    let plan = TEST_PLAN_JSON.replace(
        r#""broad_commission_percent": 0.40"#,
        r#""broad_commission_percent": 1.5"#,
    );
    let resp = send_load_plan(&mut worker, &plan);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PLAN"),
        "expected INVALID_PLAN, got: {}",
        resp
    );
    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_unilevel_three_node_chain() {
    let mut worker = common::spawn_worker();

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
        r#"{{"structure":"Test","snapshots":{{"{root}":{snap},"{child}":{snap},"{gc}":{snap}}},"volume":[{{"source_id":"{gc}","cv_amount":100.0}}]}}"#,
        root = ROOT,
        child = CHILD,
        gc = GRANDCHILD,
        snap = snap,
    );
    let request = format!(
        r#"{{"id":"calc-1","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap(),
        "calculate_unilevel failed: {}",
        resp
    );
    assert_eq!(parsed["id"], "calc-1");

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
        (mid_dollar - 2.0).abs() < 1e-10,
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
        (root_dollar - 2.0).abs() < 1e-10,
        "root dollar_amount should be 2.0, got {}",
        root_dollar
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_unilevel_without_plan_returns_no_plan() {
    let mut worker = common::spawn_worker();

    // Build a tree but don't load a plan
    build_three_node_chain(&mut worker);

    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-err","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("NO_PLAN"), "expected NO_PLAN, got: {}", resp);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_unilevel_without_tree_returns_structure_not_found() {
    let mut worker = common::spawn_worker();

    // Load a plan but don't build a tree
    load_test_plan(&mut worker);

    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-err","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
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
fn calculate_unilevel_unknown_structure_returns_not_found() {
    let mut worker = common::spawn_worker();

    load_test_plan(&mut worker);
    build_three_node_chain(&mut worker);

    let params = r#"{"structure":"Nonexistent","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-err","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
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
    let mut worker = common::spawn_worker();

    load_test_plan(&mut worker);
    build_three_node_chain(&mut worker);

    let request = r#"{"id":"calc-err","op":"calculate_unilevel","params":{"bad":"data"}}"#;
    let resp = common::send_receive(&mut worker, request);
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
    let mut child = common::spawn_worker();
    let resp = common::send_receive(&mut child, "not json at all");
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("INVALID_REQUEST"));
    // Worker should still be alive -- send a follow-up ping
    let resp2 = common::send_receive(&mut child, r#"{"id":"2","op":"ping"}"#);
    assert!(resp2.contains(r#""ok":true"#));
    assert!(resp2.contains(r#""pong""#));
    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn calculate_unilevel_empty_volume_returns_empty_earnings() {
    let mut worker = common::spawn_worker();

    load_test_plan(&mut worker);
    build_three_node_chain(&mut worker);

    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-empty","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);

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

/// A nil Go collection marshals to `null`, not `{}` or `[]`, and both request
/// collections must read that as empty rather than rejecting it. This is the
/// shape of a first-period call.
///
/// The Go twin is `TestEngineClient_CalculateUnilevel_NilCollections`.
#[test]
fn calculate_unilevel_accepts_null_collections() {
    let mut worker = common::spawn_worker();
    load_test_plan(&mut worker);
    build_three_node_chain(&mut worker);

    let request = format!(
        r#"{{"id":"u-null","op":"calculate_unilevel","params":{{"structure":"{}","snapshots":null,"volume":null}}}}"#,
        TREE_NAME
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":true"#),
        "null collections must read as empty, got: {}",
        resp
    );
    assert!(
        resp.contains(r#""result":[]"#),
        "no volume means no earnings, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Omitting `snapshots` stays an error. The other half of
/// `calculate_unilevel_accepts_null_collections`: widening null must not
/// quietly widen absent, or a caller who forgets the field is paid zero
/// instead of being told.
#[test]
fn calculate_unilevel_still_requires_snapshots() {
    let mut worker = common::spawn_worker();
    load_test_plan(&mut worker);
    build_three_node_chain(&mut worker);

    let request = format!(
        r#"{{"id":"u-nosnap","op":"calculate_unilevel","params":{{"structure":"{}","volume":[]}}}}"#,
        TREE_NAME
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PARAMS"),
        "a missing snapshots must still fail, got: {}",
        resp
    );
    assert!(
        resp.contains("snapshots"),
        "the error should name the field that is missing, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Omitting `volume` stays an error. The mirror of
/// `calculate_unilevel_still_requires_snapshots` — one guard per required
/// field, so adding `default` to either one fails loudly.
#[test]
fn calculate_unilevel_still_requires_volume() {
    let mut worker = common::spawn_worker();
    load_test_plan(&mut worker);
    build_three_node_chain(&mut worker);

    let request = format!(
        r#"{{"id":"u-novol","op":"calculate_unilevel","params":{{"structure":"{}","snapshots":{{}}}}}}"#,
        TREE_NAME
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PARAMS"),
        "a missing volume must still fail, got: {}",
        resp
    );
    assert!(
        resp.contains("volume"),
        "the error should name the field that is missing, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

// --- Generation integration tests ---
//
// `calculate_generation` had no integration coverage before HEU-626. It
// appeared only as a name in the dispatch-completeness gate.

/// Pulled in with `include_str!` rather than pasted as a second copy of a large
/// plan literal. HEU-604 tracks the near-identical plan copies already spread
/// across Go, Rust, and fixtures — don't add another.
///
/// **This file is generated.** `internal/config/genfixtures_test.go` writes it
/// from `internal/config/testdata/valid/generation-plan.yaml` through the real
/// Go pipeline. Edit the YAML, not the JSON.
///
/// The fixture belongs to the integer-width contract (UC-NET-011), where
/// deserializability is the requirement, not validity. It happens to load clean
/// through `load_plan`. Its siblings do not all share that property —
/// `stairstep.json` fails validation with `differential min_override must be a
/// fraction in [0.0, 1.0], got 10` — so don't reach for the others without
/// checking.
const GENERATION_TEST_PLAN_JSON: &str =
    include_str!("../../testdata/config_contract/fixtures/generation.json");

/// Matches the generation structure inside `GENERATION_TEST_PLAN_JSON`, the way
/// `SL_STRUCTURE` matches its streamline twin.
const GEN_STRUCTURE: &str = "GenTree";

/// Loads `GENERATION_TEST_PLAN_JSON` and asserts it took.
fn load_generation_test_plan(worker: &mut std::process::Child) {
    let resp = send_load_plan(worker, GENERATION_TEST_PLAN_JSON);
    assert!(
        resp.contains(r#""ok":true"#),
        "generation plan should load, got: {}",
        resp
    );
}

/// A nil Go collection marshals to `null`, not `{}` or `[]`, and both request
/// collections must read that as empty rather than rejecting it. This is the
/// shape of a first-period call.
///
/// The Go twin is `TestEngineClient_CalculateGeneration_NilCollections`.
#[test]
fn calculate_generation_accepts_null_collections() {
    let mut worker = common::spawn_worker();
    load_generation_test_plan(&mut worker);
    create_tree(&mut worker, GEN_STRUCTURE);

    let request = format!(
        r#"{{"id":"g-null","op":"calculate_generation","params":{{"structure":"{}","snapshots":null,"volume":null}}}}"#,
        GEN_STRUCTURE
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":true"#),
        "null collections must read as empty, got: {}",
        resp
    );
    assert!(
        resp.contains(r#""result":[]"#),
        "an empty tree means no earnings, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Omitting `snapshots` stays an error. The other half of
/// `calculate_generation_accepts_null_collections`: widening null must not
/// quietly widen absent, or a caller who forgets the field is paid zero
/// instead of being told.
#[test]
fn calculate_generation_still_requires_snapshots() {
    let mut worker = common::spawn_worker();
    load_generation_test_plan(&mut worker);
    create_tree(&mut worker, GEN_STRUCTURE);

    let request = format!(
        r#"{{"id":"g-nosnap","op":"calculate_generation","params":{{"structure":"{}","volume":[]}}}}"#,
        GEN_STRUCTURE
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PARAMS"),
        "a missing snapshots must still fail, got: {}",
        resp
    );
    assert!(
        resp.contains("snapshots"),
        "the error should name the field that is missing, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Omitting `volume` stays an error. The mirror of
/// `calculate_generation_still_requires_snapshots` — one guard per required
/// field, so adding `default` to either one fails loudly.
#[test]
fn calculate_generation_still_requires_volume() {
    let mut worker = common::spawn_worker();
    load_generation_test_plan(&mut worker);
    create_tree(&mut worker, GEN_STRUCTURE);

    let request = format!(
        r#"{{"id":"g-novol","op":"calculate_generation","params":{{"structure":"{}","snapshots":{{}}}}}}"#,
        GEN_STRUCTURE
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PARAMS"),
        "a missing volume must still fail, got: {}",
        resp
    );
    assert!(
        resp.contains("volume"),
        "the error should name the field that is missing, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

// --- Binary tree integration tests ---
//
// These tests exercise binary tree operations through the NDJSON protocol
// to verify the worker dispatches correctly to BinaryTree.

const BINARY_TREE: &str = "BinaryTest";
const NODE_A: &str = "00000000-0000-0000-0000-00000000000a";
const NODE_B: &str = "00000000-0000-0000-0000-00000000000b";
const NODE_C: &str = "00000000-0000-0000-0000-00000000000c";

/// Creates a named binary tree on the worker.
fn create_binary_tree(child: &mut std::process::Child, name: &str) {
    let resp = common::send_receive(
        child,
        &format!(
            r#"{{"id":"setup-bin","op":"create_tree","params":{{"structure":"{}","tree_type":"binary"}}}}"#,
            name
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "create_tree (binary) failed: {}",
        resp
    );
}

#[test]
fn binary_create_tree_and_add_root() {
    let mut worker = common::spawn_worker();
    create_binary_tree(&mut worker, BINARY_TREE);

    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b1","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            BINARY_TREE, NODE_A
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "add_root failed: {}", resp);
    assert!(resp.contains(r#""added":true"#));

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert_eq!(parsed["id"], "b1");

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn binary_add_node_with_position() {
    let mut worker = common::spawn_worker();
    create_binary_tree(&mut worker, BINARY_TREE);

    // Add root
    common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-root","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            BINARY_TREE, NODE_A
        ),
    );

    // Add left child at position 0
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-left","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":0,"enrolled_at":200}}}}"#,
            BINARY_TREE, NODE_B, NODE_A, NODE_A
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "add left child failed: {}",
        resp
    );
    assert!(resp.contains(r#""added":true"#));

    // Add right child at position 1
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-right","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":1,"enrolled_at":300}}}}"#,
            BINARY_TREE, NODE_C, NODE_A, NODE_A
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "add right child failed: {}",
        resp
    );
    assert!(resp.contains(r#""added":true"#));

    // Verify root has 2 children
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-children","op":"get_children","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            BINARY_TREE, NODE_A
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    let children = parsed["result"].as_array().unwrap();
    assert_eq!(children.len(), 2);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn binary_position_occupied_error() {
    let mut worker = common::spawn_worker();
    create_binary_tree(&mut worker, BINARY_TREE);

    // Add root
    common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-root","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            BINARY_TREE, NODE_A
        ),
    );

    // Add left child at position 0
    common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-first","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":0,"enrolled_at":200}}}}"#,
            BINARY_TREE, NODE_B, NODE_A, NODE_A
        ),
    );

    // Try to add another node at position 0 — should fail with POSITION_OCCUPIED
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-dup","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":0,"enrolled_at":300}}}}"#,
            BINARY_TREE, NODE_C, NODE_A, NODE_A
        ),
    );
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("POSITION_OCCUPIED"),
        "expected POSITION_OCCUPIED, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn binary_get_position_returns_slot_positions() {
    let mut worker = common::spawn_worker();
    create_binary_tree(&mut worker, BINARY_TREE);

    // Build: root(A) -> left(B) at 0, right(C) at 1
    common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-root","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            BINARY_TREE, NODE_A
        ),
    );
    common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-left","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":0,"enrolled_at":200}}}}"#,
            BINARY_TREE, NODE_B, NODE_A, NODE_A
        ),
    );
    common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-right","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":1,"enrolled_at":300}}}}"#,
            BINARY_TREE, NODE_C, NODE_A, NODE_A
        ),
    );

    // Check left child position
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-pos-left","op":"get_position","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            BINARY_TREE, NODE_B
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap(),
        "get_position failed: {}",
        resp
    );
    assert_eq!(parsed["result"]["position"].as_u64().unwrap(), 0);
    assert_eq!(parsed["result"]["parent_user_id"].as_str().unwrap(), NODE_A);

    // Check right child position
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-pos-right","op":"get_position","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            BINARY_TREE, NODE_C
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap(),
        "get_position failed: {}",
        resp
    );
    assert_eq!(parsed["result"]["position"].as_u64().unwrap(), 1);
    assert_eq!(parsed["result"]["parent_user_id"].as_str().unwrap(), NODE_A);

    // Check root's downline_counts includes both slots
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"b-pos-root","op":"get_position","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            BINARY_TREE, NODE_A
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap());
    let dc = &parsed["result"]["downline_counts"];
    assert_eq!(
        dc["0"].as_u64().unwrap(),
        0,
        "left child has no subtree descendants"
    );
    assert_eq!(
        dc["1"].as_u64().unwrap(),
        0,
        "right child has no subtree descendants"
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

// --- Sponsor query integration tests ---
//
// These tests verify sponsor queries through the NDJSON protocol.
// A 3-node unilevel tree is built where the grandchild's sponsor differs
// from its parent to distinguish sponsor from placement lineage.

const SPONSOR_TREE: &str = "SponsorTest";
const S_ROOT: &str = "00000000-0000-0000-0000-000000000001";
const S_CHILD: &str = "00000000-0000-0000-0000-000000000002";
const S_GRANDCHILD: &str = "00000000-0000-0000-0000-000000000003";

/// Builds a 3-node chain where the grandchild's sponsor is root (not child).
/// Placement: root -> child -> grandchild
/// Sponsorship: root sponsors child, root sponsors grandchild
fn build_sponsor_test_tree(worker: &mut std::process::Child) {
    create_tree(worker, SPONSOR_TREE);
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"ss-1","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            SPONSOR_TREE, S_ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"ss-2","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":200}}}}"#,
            SPONSOR_TREE, S_CHILD, S_ROOT, S_ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
    // Grandchild is placed under child but sponsored by root.
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"ss-3","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":300}}}}"#,
            SPONSOR_TREE, S_GRANDCHILD, S_CHILD, S_ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
}

#[test]
fn get_sponsor_returns_sponsor_node() {
    let mut worker = common::spawn_worker();
    build_sponsor_test_tree(&mut worker);

    // Grandchild's sponsor is root, not child (parent).
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sp1","op":"get_sponsor","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            SPONSOR_TREE, S_GRANDCHILD
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "get_sponsor failed: {}",
        resp
    );

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert_eq!(
        parsed["result"]["user_id"].as_str().unwrap(),
        S_ROOT,
        "sponsor of grandchild should be root, not child"
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn get_sponsor_upline_returns_chain() {
    let mut worker = common::spawn_worker();
    build_sponsor_test_tree(&mut worker);

    // Grandchild's sponsor upline: root only (root sponsors grandchild directly).
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sp2","op":"get_sponsor_upline","params":{{"structure":"{}","user_id":"{}","depth":0}}}}"#,
            SPONSOR_TREE, S_GRANDCHILD
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "get_sponsor_upline failed: {}",
        resp
    );

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    let result = parsed["result"].as_array().unwrap();
    // Sponsor chain: grandchild -> root (root is grandchild's sponsor).
    // Root has no sponsor, so chain length is 1.
    assert_eq!(result.len(), 1, "sponsor upline should have 1 node");
    assert_eq!(result[0]["user_id"].as_str().unwrap(), S_ROOT);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn get_sponsored_returns_recruits() {
    let mut worker = common::spawn_worker();
    build_sponsor_test_tree(&mut worker);

    // Root sponsored both child and grandchild.
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sp3","op":"get_sponsored","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            SPONSOR_TREE, S_ROOT
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "get_sponsored failed: {}",
        resp
    );

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    let result = parsed["result"].as_array().unwrap();
    assert_eq!(result.len(), 2, "root should have sponsored 2 people");

    // Verify both child and grandchild are in the results.
    let ids: Vec<&str> = result
        .iter()
        .map(|n| n["user_id"].as_str().unwrap())
        .collect();
    assert!(ids.contains(&S_CHILD), "child should be in sponsored list");
    assert!(
        ids.contains(&S_GRANDCHILD),
        "grandchild should be in sponsored list"
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

// --- create_tree overwrite guard test ---

#[test]
fn create_tree_duplicate_returns_error() {
    let mut worker = common::spawn_worker();

    // First create succeeds.
    create_tree(&mut worker, "DupTest");

    // Second create with the same name should fail.
    let resp = common::send_receive(
        &mut worker,
        r#"{"id":"dup","op":"create_tree","params":{"structure":"DupTest","tree_type":"unilevel"}}"#,
    );
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("TREE_EXISTS"),
        "expected TREE_EXISTS, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

// --- Binary pairing commission integration tests ---

/// A minimal compensation plan JSON with a single binary structure.
///
/// - One rank: "associate" with min PV 0, everyone eligible
/// - Pairing config: 10%, WeakerLeg, FullFlush
/// - No carry-forward cap
const BINARY_PLAN_JSON: &str = r#"{
    "name": "Binary Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "binary",
            "config": {
                "name": "BinaryCalc",
                "binary_commission": {
                    "volume_to_dollar_multiplier": null,
                    "mode": {
                        "pairing": {
                            "percent": 0.10,
                            "calculation": "weaker_leg",
                            "cap_per_period": null,
                            "volume_after_payout": "full_flush",
                            "carry_forward_cap": null
                        }
                    }
                }
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
            "name": "associate",
            "ordinal": 1,
            "qualification": {
                "structures": [],
                "required_products": []
            },
            "qualified_structures": ["BinaryCalc"],
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
        "board_cycling": null
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

/// Loads the binary test plan into the worker. Asserts success.
fn load_binary_test_plan(worker: &mut std::process::Child) {
    let minified: String = BINARY_PLAN_JSON
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let request = format!(
        r#"{{"id":"load-bin-plan","op":"load_plan","params":{}}}"#,
        minified
    );
    let resp = common::send_receive(worker, &request);
    assert!(
        resp.contains(r#""ok":true"#),
        "load_plan (binary) failed: {}",
        resp
    );
}

/// Builds a 3-node binary tree for commission tests:
/// root(A) -> left(B) at position 0, right(C) at position 1.
fn build_binary_calc_tree(worker: &mut std::process::Child) {
    let tree_name = "BinaryCalc";
    create_binary_tree(worker, tree_name);

    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"bc-1","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            tree_name, NODE_A
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"bc-2","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":0,"enrolled_at":200}}}}"#,
            tree_name, NODE_B, NODE_A, NODE_A
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"bc-3","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":1,"enrolled_at":300}}}}"#,
            tree_name, NODE_C, NODE_A, NODE_A
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
}

#[test]
fn calculate_binary_pairing_balanced_legs() {
    let mut worker = common::spawn_worker();

    // 1. Load plan
    load_binary_test_plan(&mut worker);

    // 2. Build tree: root(A) -> left(B), right(C)
    build_binary_calc_tree(&mut worker);

    // 3. Calculate commissions
    //    Left(B) generates 500 CV, Right(C) generates 500 CV.
    //    Root sees left=500, right=500. Matched=500.
    //    Earning: 500 * 0.10 * 1.0 * 1.0 = 50.0
    let snap = r#"{"rank":"associate","personal_volume":150.0,"status":"active","has_order_in_period":true}"#;
    let params = format!(
        r#"{{"structure":"BinaryCalc","snapshots":{{"{a}":{snap},"{b}":{snap},"{c}":{snap}}},"volume":[{{"source_id":"{b}","cv_amount":500.0}},{{"source_id":"{c}","cv_amount":500.0}}]}}"#,
        a = NODE_A,
        b = NODE_B,
        c = NODE_C,
        snap = snap,
    );
    let request = format!(
        r#"{{"id":"bp-1","op":"calculate_binary_pairing","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap(),
        "calculate_binary_pairing failed: {}",
        resp
    );
    assert_eq!(parsed["id"], "bp-1");

    let earnings = parsed["result"]["earnings"].as_array().unwrap();
    assert_eq!(earnings.len(), 1, "expected 1 earning, got: {}", resp);

    let earning = &earnings[0];
    assert_eq!(earning["earner_id"].as_str().unwrap(), NODE_A);
    assert_eq!(earning["left_volume"].as_f64().unwrap(), 500.0);
    assert_eq!(earning["right_volume"].as_f64().unwrap(), 500.0);
    assert_eq!(earning["matched_volume"].as_f64().unwrap(), 500.0);
    assert_eq!(earning["ratio"].as_f64().unwrap(), 1.0);
    assert_eq!(earning["percent"].as_f64().unwrap(), 0.10);

    let dollar = earning["dollar_amount"].as_f64().unwrap();
    assert!(
        (dollar - 50.0).abs() < 1e-10,
        "dollar_amount should be 50.0, got {}",
        dollar
    );
    assert!(!earning["capped"].as_bool().unwrap());

    // Verify carry_forward is present.
    let cf = parsed["result"]["carry_forward"].as_object().unwrap();
    assert!(!cf.is_empty(), "carry_forward should have entries");

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_binary_pairing_without_plan_returns_no_plan() {
    let mut worker = common::spawn_worker();

    // Build a tree but don't load a plan.
    build_binary_calc_tree(&mut worker);

    let params = r#"{"structure":"BinaryCalc","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"bp-err","op":"calculate_binary_pairing","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("NO_PLAN"), "expected NO_PLAN, got: {}", resp);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_binary_pairing_wrong_tree_type_returns_error() {
    let mut worker = common::spawn_worker();

    load_binary_test_plan(&mut worker);

    // Create a unilevel tree with the binary structure name.
    create_tree(&mut worker, "BinaryCalc");

    let params = r#"{"structure":"BinaryCalc","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"bp-type","op":"calculate_binary_pairing","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("INVALID_PARAMS"),
        "expected INVALID_PARAMS for wrong tree type, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// A nil Go collection marshals to `null`, not `{}` or `[]`, and both required
/// request collections must read that as empty rather than rejecting it. This
/// is the shape of a first-period call.
///
/// Binary's success shape differs from its siblings: it returns
/// `{"earnings":[...],"carry_forward":{...}}`, not a bare array. And
/// `carry_forward` is **not** empty here — `accumulate_leg_volumes` creates an
/// entry per live node and the post-payout phase emits all of them, zero-valued
/// rows included. So `build_binary_calc_tree`'s three nodes produce three zero
/// entries even with empty collections. That is existing, correct behavior; do
/// not "fix" the calculator to drop them.
///
/// Asserted structurally rather than by string match, because `HashMap` key
/// order is nondeterministic.
///
/// The Go twin is `TestEngineClient_CalculateBinaryPairing_NilCollections`.
#[test]
fn calculate_binary_pairing_accepts_null_collections() {
    let mut worker = common::spawn_worker();
    load_binary_test_plan(&mut worker);
    build_binary_calc_tree(&mut worker);

    let request = r#"{"id":"bp-null","op":"calculate_binary_pairing","params":{"structure":"BinaryCalc","snapshots":null,"volume":null}}"#;
    let resp = common::send_receive(&mut worker, request);

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap_or(false),
        "null collections must read as empty, got: {}",
        resp
    );

    let earnings = parsed["result"]["earnings"].as_array().unwrap();
    assert!(
        earnings.is_empty(),
        "no volume means no earnings, got: {}",
        resp
    );

    let carry = parsed["result"]["carry_forward"].as_object().unwrap();
    assert_eq!(carry.len(), 3, "one carry row per live node, got: {}", resp);
    for node in [NODE_A, NODE_B, NODE_C] {
        let legs = carry
            .get(node)
            .unwrap_or_else(|| panic!("carry_forward should name {}, got: {}", node, resp));
        assert_eq!(legs["left"].as_f64().unwrap(), 0.0, "got: {}", resp);
        assert_eq!(legs["right"].as_f64().unwrap(), 0.0, "got: {}", resp);
    }

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Omitting `snapshots` stays an error. The other half of
/// `calculate_binary_pairing_accepts_null_collections`: widening null must not
/// quietly widen absent, or a caller who forgets the field is paid zero
/// instead of being told.
#[test]
fn calculate_binary_pairing_still_requires_snapshots() {
    let mut worker = common::spawn_worker();
    load_binary_test_plan(&mut worker);
    build_binary_calc_tree(&mut worker);

    let request = r#"{"id":"bp-nosnap","op":"calculate_binary_pairing","params":{"structure":"BinaryCalc","volume":[]}}"#;
    let resp = common::send_receive(&mut worker, request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PARAMS"),
        "a missing snapshots must still fail, got: {}",
        resp
    );
    assert!(
        resp.contains("snapshots"),
        "the error should name the field that is missing, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Omitting `volume` stays an error. The mirror of
/// `calculate_binary_pairing_still_requires_snapshots` — one guard per required
/// field, so adding `default` to either one fails loudly.
#[test]
fn calculate_binary_pairing_still_requires_volume() {
    let mut worker = common::spawn_worker();
    load_binary_test_plan(&mut worker);
    build_binary_calc_tree(&mut worker);

    let request = r#"{"id":"bp-novol","op":"calculate_binary_pairing","params":{"structure":"BinaryCalc","snapshots":{}}}"#;
    let resp = common::send_receive(&mut worker, request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PARAMS"),
        "a missing volume must still fail, got: {}",
        resp
    );
    assert!(
        resp.contains("volume"),
        "the error should name the field that is missing, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

// --- Multi-position binary commission integration test ---

/// UUIDs for multi-position test nodes.
const MP_ROOT: &str = "00000000-0000-0000-0000-000000000050";
const MP_POS1: &str = "00000000-0000-0000-0000-000000000051";
const MP_POS2: &str = "00000000-0000-0000-0000-000000000052";
const MP_POS3: &str = "00000000-0000-0000-0000-000000000053";
const MP_LEFT1: &str = "00000000-0000-0000-0000-000000000054";
const MP_RIGHT1: &str = "00000000-0000-0000-0000-000000000055";
const MP_LEFT2: &str = "00000000-0000-0000-0000-000000000056";
// 0057 unused: pos3 occupies the right slot under pos2 instead of a leaf node.
const MP_LEFT3: &str = "00000000-0000-0000-0000-000000000058";
const MP_RIGHT3: &str = "00000000-0000-0000-0000-000000000059";
/// Owner UUIDs (not in the tree).
const OWNER_A: &str = "00000000-0000-0000-0000-0000000000a0";
const OWNER_B: &str = "00000000-0000-0000-0000-0000000000b0";

/// Multi-position binary test plan: 10%, WeakerLeg, FullFlush, aggregate cap 500.
const MP_BINARY_PLAN_JSON: &str = r#"{
    "name": "Multi-Position Binary Plan",
    "version": 1,
    "structures": [
        {
            "type": "binary",
            "config": {
                "name": "MPBinary",
                "binary_commission": {
                    "volume_to_dollar_multiplier": null,
                    "mode": {
                        "pairing": {
                            "percent": 0.10,
                            "calculation": "weaker_leg",
                            "cap_per_period": 500.0,
                            "volume_after_payout": "full_flush",
                            "carry_forward_cap": null,
                            "multi_position_cap_mode": "aggregate"
                        }
                    }
                }
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
            "name": "associate",
            "ordinal": 1,
            "qualification": {
                "structures": [],
                "required_products": []
            },
            "qualified_structures": ["MPBinary"],
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
        "board_cycling": null
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

/// Builds the multi-position tree:
///
/// ```text
///              root (MP_ROOT)
///             /              \
///       pos1 (MP_POS1)      pos2 (MP_POS2)
///       /    \               /    \
///    left1  right1        left2   pos3 (MP_POS3)
///                                 /    \
///                              left3  right3
/// ```
fn build_multi_position_tree(worker: &mut std::process::Child) {
    let tree = "MPBinary";
    create_binary_tree(worker, tree);

    // Root
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"mp-1","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            tree, MP_ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    // pos1 under root left
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"mp-2","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":0,"enrolled_at":200}}}}"#,
            tree, MP_POS1, MP_ROOT, MP_ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    // pos2 under root right
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"mp-3","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":1,"enrolled_at":200}}}}"#,
            tree, MP_POS2, MP_ROOT, MP_ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    // Children under pos1
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"mp-4","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":0,"enrolled_at":300}}}}"#,
            tree, MP_LEFT1, MP_POS1, MP_POS1
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"mp-5","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":1,"enrolled_at":300}}}}"#,
            tree, MP_RIGHT1, MP_POS1, MP_POS1
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    // Children under pos2
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"mp-6","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":0,"enrolled_at":300}}}}"#,
            tree, MP_LEFT2, MP_POS2, MP_POS2
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"mp-7","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":1,"enrolled_at":300}}}}"#,
            tree, MP_POS3, MP_POS2, MP_POS2
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    // Children under pos3
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"mp-8","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":0,"enrolled_at":400}}}}"#,
            tree, MP_LEFT3, MP_POS3, MP_POS3
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"mp-9","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","position":1,"enrolled_at":400}}}}"#,
            tree, MP_RIGHT3, MP_POS3, MP_POS3
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
}

#[test]
fn calculate_binary_pairing_multi_position_ownership() {
    let mut worker = common::spawn_worker();

    // 1. Load plan with aggregate cap mode
    let minified: String = MP_BINARY_PLAN_JSON
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let request = format!(
        r#"{{"id":"load-mp","op":"load_plan","params":{}}}"#,
        minified
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":true"#),
        "load_plan (multi-position) failed: {}",
        resp
    );

    // 2. Build multi-position tree
    build_multi_position_tree(&mut worker);

    // 3. Calculate with ownership map:
    //    pos1 -> owner_A, pos2 -> owner_A, pos3 -> owner_B
    let snap = r#"{"rank":"associate","personal_volume":150.0,"status":"active","has_order_in_period":true}"#;

    // Volume: 3000 CV from each leaf. pos1 balanced (3000/3000),
    // pos2 unbalanced (left2=3000 vs pos3 subtree=6000), pos3 balanced (3000/3000).
    let params = format!(
        concat!(
            r#"{{"structure":"MPBinary","snapshots":{{"{owner_a}":{snap},"{owner_b}":{snap},"#,
            r#""{root}":{snap},"{left1}":{snap},"{right1}":{snap},"{left2}":{snap},"#,
            r#""{left3}":{snap},"{right3}":{snap}}},"#,
            r#""volume":[{{"source_id":"{left1}","cv_amount":3000.0}},"#,
            r#"{{"source_id":"{right1}","cv_amount":3000.0}},"#,
            r#"{{"source_id":"{left2}","cv_amount":3000.0}},"#,
            r#"{{"source_id":"{left3}","cv_amount":3000.0}},"#,
            r#"{{"source_id":"{right3}","cv_amount":3000.0}}],"#,
            r#""ownership":{{"{pos1}":"{owner_a}","{pos2}":"{owner_a}","{pos3}":"{owner_b}"}}}}"#
        ),
        owner_a = OWNER_A,
        owner_b = OWNER_B,
        root = MP_ROOT,
        pos1 = MP_POS1,
        pos2 = MP_POS2,
        pos3 = MP_POS3,
        left1 = MP_LEFT1,
        right1 = MP_RIGHT1,
        left2 = MP_LEFT2,
        left3 = MP_LEFT3,
        right3 = MP_RIGHT3,
        snap = snap,
    );
    let request = format!(
        r#"{{"id":"mp-calc","op":"calculate_binary_pairing","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap(),
        "calculate_binary_pairing failed: {}",
        resp
    );

    let earnings = parsed["result"]["earnings"].as_array().unwrap();

    // Owner A's earnings (pos1 and pos2).
    let owner_a_earnings: Vec<_> = earnings
        .iter()
        .filter(|e| e["earner_id"].as_str().unwrap() == OWNER_A)
        .collect();

    // Owner A should have earnings from positions they own.
    assert!(
        !owner_a_earnings.is_empty(),
        "owner_a should have earnings, got: {}",
        resp
    );

    // Verify position_id is present and differs from earner_id.
    for e in &owner_a_earnings {
        assert_eq!(e["earner_id"].as_str().unwrap(), OWNER_A);
        let pos_id = e["position_id"].as_str().unwrap();
        assert!(
            pos_id == MP_POS1 || pos_id == MP_POS2,
            "position_id should be pos1 or pos2, got: {}",
            pos_id
        );
    }

    // Owner A aggregate cap: total should not exceed 500.0.
    let owner_a_total: f64 = owner_a_earnings
        .iter()
        .map(|e| e["dollar_amount"].as_f64().unwrap())
        .sum();
    assert!(
        (owner_a_total - 500.0).abs() < 1e-10,
        "owner_a aggregate should be exactly 500.0 after pro-rata cap, got {}",
        owner_a_total
    );

    // Owner B's earnings (pos3).
    let owner_b_earnings: Vec<_> = earnings
        .iter()
        .filter(|e| e["earner_id"].as_str().unwrap() == OWNER_B)
        .collect();
    assert_eq!(
        owner_b_earnings.len(),
        1,
        "owner_b should have 1 earning from pos3"
    );
    assert_eq!(
        owner_b_earnings[0]["position_id"].as_str().unwrap(),
        MP_POS3
    );

    // Carry-forward should be keyed by position_id, not owner_id.
    let cf = parsed["result"]["carry_forward"].as_object().unwrap();
    assert!(
        cf.contains_key(MP_POS1),
        "carry_forward should contain pos1"
    );
    assert!(
        cf.contains_key(MP_POS2),
        "carry_forward should contain pos2"
    );
    assert!(
        cf.contains_key(MP_POS3),
        "carry_forward should contain pos3"
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

// --- Pass-up (Australian X-Up) commission integration test ---
//
// Verifies end-to-end: a JSON config with pass_up on the unilevel structure
// deserializes correctly and produces the expected earnings with pass-up skip
// behavior.

/// UUIDs for the pass-up test tree.
const PU_S: &str = "00000000-0000-0000-0000-000000000010";
const PU_A: &str = "00000000-0000-0000-0000-000000000020";
const PU_R1: &str = "00000000-0000-0000-0000-000000000031";
const PU_R2: &str = "00000000-0000-0000-0000-000000000032";
const PU_R3: &str = "00000000-0000-0000-0000-000000000033";

/// Pass-up tree name used across the test.
const PU_TREE: &str = "PassUpTest";

/// A compensation plan with pass_up configured on the unilevel structure.
///
/// - count: 2, includes_commissions: false
/// - One rank: "member" with rate 0.05 at levels 1-3
/// - commissionable_depth: 3
/// - No compression
const PASS_UP_PLAN_JSON: &str = r#"{
    "name": "Pass-Up Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "unilevel",
            "config": {
                "name": "PassUpTest",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": {
                        "member": { "1": 0.05, "2": 0.05, "3": 0.05 }
                    }
                },
                "compression": null,
                "pass_up": {
                    "count": 2,
                    "includes_commissions": false
                }
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
            "qualified_structures": ["PassUpTest"],
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
        "board_cycling": null
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

/// Loads the pass-up test plan into the worker. Asserts success.
fn load_pass_up_plan(worker: &mut std::process::Child) {
    let minified: String = PASS_UP_PLAN_JSON
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let request = format!(
        r#"{{"id":"load-pu-plan","op":"load_plan","params":{}}}"#,
        minified
    );
    let resp = common::send_receive(worker, &request);
    assert!(
        resp.contains(r#""ok":true"#),
        "load_plan (pass-up) failed: {}",
        resp
    );
}

/// Builds the pass-up test tree:
///
///   S(010) -> A(020) -> R1(031, t=100), R2(032, t=200), R3(033, t=300)
///
/// A sponsors R1, R2, R3. With pass_up count=2, A's skip set = {R1, R2}.
fn build_pass_up_tree(worker: &mut std::process::Child) {
    create_tree(worker, PU_TREE);

    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"pu-1","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":50}}}}"#,
            PU_TREE, PU_S
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"pu-2","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":60}}}}"#,
            PU_TREE, PU_A, PU_S, PU_S
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"pu-3","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":100}}}}"#,
            PU_TREE, PU_R1, PU_A, PU_A
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"pu-4","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":200}}}}"#,
            PU_TREE, PU_R2, PU_A, PU_A
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);

    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"pu-5","op":"add_node","params":{{"structure":"{}","user_id":"{}","parent_id":"{}","sponsor_id":"{}","enrolled_at":300}}}}"#,
            PU_TREE, PU_R3, PU_A, PU_A
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "setup failed: {}", resp);
}

#[test]
fn calculate_unilevel_pass_up_skips_first_recruits() {
    let mut worker = common::spawn_worker();

    // 1. Load pass-up plan (count=2, includes_commissions=false)
    load_pass_up_plan(&mut worker);

    // 2. Build tree: S -> A -> [R1(t=100), R2(t=200), R3(t=300)]
    build_pass_up_tree(&mut worker);

    // 3. Calculate commissions with volume from R1 and R3.
    //
    //    R1 volume (100 CV):
    //      Walking upline from R1: A is next but R1 is in A's skip set
    //      (pass-up count=2, R1 is the 1st recruit). A is skipped without
    //      consuming a level. S earns at level 1.
    //      Dollar: 100 * 0.40 * 1.0 * 0.05 = 2.0
    //
    //    R3 volume (100 CV):
    //      Walking upline from R3: A is next. R3 is NOT in A's skip set
    //      (only R1 and R2 are). A earns at level 1.
    //      S earns at level 2.
    //      Dollar: 100 * 0.40 * 1.0 * 0.05 = 2.0 each
    let snap =
        r#"{"rank":"member","personal_volume":100.0,"status":"active","has_order_in_period":true}"#;
    let params = format!(
        r#"{{"structure":"{tree}","snapshots":{{"{s}":{snap},"{a}":{snap},"{r1}":{snap},"{r2}":{snap},"{r3}":{snap}}},"volume":[{{"source_id":"{r1}","cv_amount":100.0}},{{"source_id":"{r3}","cv_amount":100.0}}]}}"#,
        tree = PU_TREE,
        s = PU_S,
        a = PU_A,
        r1 = PU_R1,
        r2 = PU_R2,
        r3 = PU_R3,
        snap = snap,
    );
    let request = format!(
        r#"{{"id":"pu-calc","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap(),
        "calculate_unilevel (pass-up) failed: {}",
        resp
    );

    let earnings = parsed["result"].as_array().unwrap();

    // Expected earnings:
    //   From R1: S at level 1 (2.0)          -- A was skipped
    //   From R3: A at level 1 (2.0), S at level 2 (2.0) -- A earned normally
    // Total: 3 earnings
    assert_eq!(
        earnings.len(),
        3,
        "expected 3 earnings (S from R1, A from R3, S from R3), got: {}",
        resp
    );

    // --- Verify R1 volume: only S earns (A is skipped) ---
    let r1_earnings: Vec<&serde_json::Value> = earnings
        .iter()
        .filter(|e| e["source_id"].as_str().unwrap() == PU_R1)
        .collect();
    assert_eq!(
        r1_earnings.len(),
        1,
        "R1 volume should produce exactly 1 earning (A skipped), got: {:?}",
        r1_earnings
    );
    assert_eq!(
        r1_earnings[0]["earner_id"].as_str().unwrap(),
        PU_S,
        "S should earn from R1 volume"
    );
    assert_eq!(
        r1_earnings[0]["level"].as_u64().unwrap(),
        1,
        "S should earn at level 1 (A skipped without consuming a level)"
    );
    let s_r1_dollar = r1_earnings[0]["dollar_amount"].as_f64().unwrap();
    assert!(
        (s_r1_dollar - 2.0).abs() < 1e-10,
        "S dollar_amount from R1 should be 2.0, got {}",
        s_r1_dollar
    );

    // --- Verify R3 volume: A earns at level 1, S earns at level 2 ---
    let r3_earnings: Vec<&serde_json::Value> = earnings
        .iter()
        .filter(|e| e["source_id"].as_str().unwrap() == PU_R3)
        .collect();
    assert_eq!(
        r3_earnings.len(),
        2,
        "R3 volume should produce 2 earnings (A and S), got: {:?}",
        r3_earnings
    );

    let a_from_r3 = r3_earnings
        .iter()
        .find(|e| e["earner_id"].as_str().unwrap() == PU_A)
        .expect("A should earn from R3 volume");
    assert_eq!(a_from_r3["level"].as_u64().unwrap(), 1);
    let a_r3_dollar = a_from_r3["dollar_amount"].as_f64().unwrap();
    assert!(
        (a_r3_dollar - 2.0).abs() < 1e-10,
        "A dollar_amount from R3 should be 2.0, got {}",
        a_r3_dollar
    );

    let s_from_r3 = r3_earnings
        .iter()
        .find(|e| e["earner_id"].as_str().unwrap() == PU_S)
        .expect("S should earn from R3 volume");
    assert_eq!(s_from_r3["level"].as_u64().unwrap(), 2);
    let s_r3_dollar = s_from_r3["dollar_amount"].as_f64().unwrap();
    assert!(
        (s_r3_dollar - 2.0).abs() < 1e-10,
        "S dollar_amount from R3 should be 2.0, got {}",
        s_r3_dollar
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

// --- Streamline integration tests ---

const SL_USER1: &str = "00000000-0000-0000-0000-000000000011";
const SL_USER2: &str = "00000000-0000-0000-0000-000000000012";
const SL_USER3: &str = "00000000-0000-0000-0000-000000000013";
const SL_STRUCTURE: &str = "TestStreamline";

/// Minimal plan carrying a streamline structure named `TestStreamline` and a
/// companion unilevel. The streamline name matches `SL_STRUCTURE` so the plan
/// and `create_streamline` agree when a test needs both a loaded plan and a
/// live engine (HEU-583). Its level-1 `percent` of 0.10 is the value mutated to
/// exercise the load-time gate.
///
/// The unilevel is required, not decoration. Go's `validateStreamlineCompanion`
/// (`internal/config/rules.go:839`) requires every streamline structure to have
/// a companion unilevel, and Rust's `CompensationPlan::validate` does not
/// enforce that rule. Without it this constant encodes a plan production Go
/// rejects, which is what it did between HEU-583 and HEU-603.
///
/// It is the *second* structure on purpose. `load_plan_rejects_duplicate_structure_names`
/// pushes a third onto this list and asserts the exact resulting name order.
const STREAMLINE_TEST_PLAN_JSON: &str = r#"{
    "name": "Integration Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "streamline",
            "config": {
                "name": "TestStreamline",
                "streamline_commission": {
                    "volume_to_dollar_multiplier": 1.0,
                    "commissionable_depth": 5,
                    "dynamic_compression": [
                        { "level": 1, "min_rank": "member", "percent": 0.10 }
                    ],
                    "streams": null
                }
            }
        },
        {
            "type": "unilevel",
            "config": {
                "name": "TestUnilevel",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": { "member": { "1": 0.05, "2": 0.05, "3": 0.05 } }
                },
                "compression": null
            }
        }
    ],
    "period": { "length": "month", "start_date": "2026-03-01", "payout_lag_days": 14 },
    "volume": { "inhibit_signup_volume": false, "base_currency": "USD", "volume_to_dollar_multiplier": 1.0, "deduct_qualifying_volume": false },
    "ranks": [
        { "name": "member", "ordinal": 1, "qualification": { "structures": [], "required_products": [] }, "qualified_structures": ["TestStreamline", "TestUnilevel"], "demotion_policy": "promotion_only" }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": { "min_personal_volume": 0.0, "require_order_in_period": false, "eligible_statuses": [], "active_leg_tiers": [] },
    "bonuses": { "matching": null, "sponsor": null, "fast_start": null, "rank_advancement": null, "leadership_development": null, "infinity": null, "lifestyle": null, "pool": null, "matrix_completion": null, "position": null, "board_cycling": null },
    "payout": { "base_currency": "USD", "minimum_amount": 50.0, "split_payouts_enabled": true, "methods": [ { "type": "bank_transfer", "fee": 2.50 } ] },
    "caps": { "per_distributor_per_period": null, "company_payout_cap_percent": 0.42, "cap_enforcement": "pro_rata", "clawback_on_refund": false },
    "placement": { "donated_placement": null, "holding_tank": null, "binary_placement": null }
}"#;

/// Creates a streamline engine under an arbitrary name. Tests that need the
/// engine and the plan to disagree use this directly; everything else uses
/// `create_streamline`, which pins the name to `SL_STRUCTURE`.
fn create_streamline_named(worker: &mut std::process::Child, name: &str) {
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"sl-create","op":"create_streamline","params":{{"structure":"{}","assignment_mode":"sponsor_stream","freeze_on_demotion":true,"timestamp":1000}}}}"#,
            name
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "create_streamline failed: {}",
        resp
    );
}

fn create_streamline(worker: &mut std::process::Child) {
    create_streamline_named(worker, SL_STRUCTURE);
}

/// Loads `STREAMLINE_TEST_PLAN_JSON` and asserts it took. `calculate_streamline`
/// resolves its structure config from this plan (HEU-583), so a test that skips
/// this gets `NO_PLAN` rather than earnings.
fn load_streamline_test_plan(worker: &mut std::process::Child) {
    let resp = send_load_plan(worker, STREAMLINE_TEST_PLAN_JSON);
    assert!(
        resp.contains(r#""ok":true"#),
        "load_plan (streamline) failed: {}",
        resp
    );
}

fn sl_add_member(worker: &mut std::process::Child, id: &str, user: &str, sponsor: &str, ts: i64) {
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"{}","op":"streamline_add_member","params":{{"structure":"{}","user_id":"{}","sponsor_id":"{}","timestamp":{}}}}}"#,
            id, SL_STRUCTURE, user, sponsor, ts
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "streamline_add_member failed: {}",
        resp
    );
}

// --- Board plan commission integration tests ---

/// The board structure's name. Must match the `board_plan` structure inside
/// `BOARD_TEST_PLAN_JSON`, the way `SL_STRUCTURE` matches its streamline twin.
/// `load_plan_accepts_valid_board_plan` asserts the two agree, so drift fails
/// there rather than surfacing later as a confusing STRUCTURE_NOT_FOUND.
const BP_STRUCTURE: &str = "BoardTest";

/// Board plan test plan.
///
/// `board_calculate_commissions` resolves its `board_cycling` config from this
/// plan (HEU-603), so a test that skips `load_board_test_plan` gets NO_PLAN
/// instead of earnings. `board_calculate_without_plan_returns_no_plan` below
/// pins that.
///
/// The unilevel structure is not decoration. Go's `validateBoardPlanCompanion`
/// (`internal/config/rules.go:812`) requires every board plan to have a
/// companion unilevel, and Rust's `CompensationPlan::validate` does not enforce
/// that rule. Without it this constant would encode a plan production Go
/// rejects. `STREAMLINE_TEST_PLAN_JSON` above has the same treatment for the
/// twin rule `validateStreamlineCompanion`.
///
/// `cycle_commission: 500.0` and `max_cycles_per_period: 3` are the values the
/// contract fixture expects, and the paragraph below is why they must match.
///
/// `engine/testdata/contracts/board_calculate_commissions.json` embeds a
/// hand-maintained copy of this plan in its `setup_raw`. Nothing keeps the two
/// in sync. Two copies now, not three: the fixture's request carried its own
/// inline `config` until that field left the wire. A wrong value in the
/// embedded plan changes what the fixture pays, with nothing masking it.
/// Change one, change both. HEU-604 tracks consolidating the copies.
const BOARD_TEST_PLAN_JSON: &str = r#"{
    "name": "Integration Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "unilevel",
            "config": {
                "name": "TestUnilevel",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": { "member": { "1": 0.05, "2": 0.05, "3": 0.05 } }
                },
                "compression": null
            }
        },
        {
            "type": "board_plan",
            "config": {
                "name": "BoardTest",
                "width": 2,
                "height": 2,
                "board_cycling": {
                    "cycle_commission": 500.0,
                    "re_entry_enabled": true,
                    "re_entry_position": "bottom",
                    "max_cycles_per_period": 3,
                    "max_cascade_depth": 10,
                    "stall_threshold_periods": 3,
                    "inactive_compression": false
                }
            }
        }
    ],
    "period": { "length": "month", "start_date": "2026-03-01", "payout_lag_days": 14 },
    "volume": { "inhibit_signup_volume": false, "base_currency": "USD", "volume_to_dollar_multiplier": 1.0, "deduct_qualifying_volume": false },
    "ranks": [
        { "name": "member", "ordinal": 1, "qualification": { "structures": [], "required_products": [] }, "qualified_structures": ["TestUnilevel", "BoardTest"], "demotion_policy": "promotion_only" }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": { "min_personal_volume": 0.0, "require_order_in_period": false, "eligible_statuses": [], "active_leg_tiers": [] },
    "bonuses": { "matching": null, "sponsor": null, "fast_start": null, "rank_advancement": null, "leadership_development": null, "infinity": null, "lifestyle": null, "pool": null, "matrix_completion": null, "position": null, "board_cycling": null },
    "payout": { "base_currency": "USD", "minimum_amount": 50.0, "split_payouts_enabled": true, "methods": [ { "type": "bank_transfer", "fee": 2.50 } ] },
    "caps": { "per_distributor_per_period": null, "company_payout_cap_percent": 0.42, "cap_enforcement": "pro_rata", "clawback_on_refund": false },
    "placement": { "donated_placement": null, "holding_tank": null, "binary_placement": null }
}"#;

/// Loads `BOARD_TEST_PLAN_JSON` and asserts it took.
fn load_board_test_plan(worker: &mut std::process::Child) {
    let resp = send_load_plan(worker, BOARD_TEST_PLAN_JSON);
    assert!(
        resp.contains(r#""ok":true"#),
        "load_plan (board) failed: {}",
        resp
    );
}

/// Proves `BOARD_TEST_PLAN_JSON` is a valid plan, so that when the negative
/// `cycle_commission` gate test arrives it can attribute its rejection to the
/// mutated value rather than to drift in the constant. Mirrors
/// `load_plan_accepts_valid_streamline_plan`, which does the same job for the
/// streamline constant.
///
/// Also pins `BP_STRUCTURE` to the name inside the JSON. Nothing else ties the
/// two together, and a silent drift would surface later as a confusing
/// STRUCTURE_NOT_FOUND rather than a failure here.
#[test]
fn load_plan_accepts_valid_board_plan() {
    assert!(
        BOARD_TEST_PLAN_JSON.contains(&format!(r#""name": "{}""#, BP_STRUCTURE)),
        "BP_STRUCTURE ({}) does not appear as a structure name in \
         BOARD_TEST_PLAN_JSON. Either the name drifted, or the JSON was \
         reformatted away from the `\"name\": \"value\"` spacing this \
         substring match depends on.",
        BP_STRUCTURE
    );

    let mut worker = common::spawn_worker();
    load_board_test_plan(&mut worker);
    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// The plan gate. Without a loaded plan there is no config to rate with.
///
/// `require_plan` runs before the params are parsed, so the payload shape does
/// not affect this path. `board_calculate_ignores_request_scoped_config` owns
/// the legacy-shape behavior.
#[test]
fn board_calculate_without_plan_returns_no_plan() {
    let mut worker = common::spawn_worker();

    let request = format!(
        r#"{{"id":"bp-noplan","op":"board_calculate_commissions","params":{{"structure":"{}","cycle_events":[],"period_cycle_counts":{{}}}}}}"#,
        BP_STRUCTURE
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("NO_PLAN"),
        "expected NO_PLAN, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// A nil Go collection marshals to `null`, not `[]` or `{}`, and both request
/// collections must read that as empty rather than rejecting it.
///
/// This is the shape of a first-period call: no prior cycle counts, and if
/// nothing cycled, no events either. `#[serde(default)]` does not cover it —
/// that handles an *absent* key, while Go sends the key with a null value.
///
/// The Go twin is `TestEngineClient_CalculateBoardCommissions_NilCollections`.
/// Go also omits `period_cycle_counts` when nil, so both sides are covered:
/// Go stops sending the bad shape, and the worker stops rejecting it whoever
/// sends it.
#[test]
fn board_calculate_accepts_null_collections() {
    let mut worker = common::spawn_worker();
    load_board_test_plan(&mut worker);

    let request = format!(
        r#"{{"id":"bp-nullevents","op":"board_calculate_commissions","params":{{"structure":"{}","cycle_events":null,"period_cycle_counts":null}}}}"#,
        BP_STRUCTURE
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":true"#),
        "null collections must read as empty, got: {}",
        resp
    );
    assert!(
        resp.contains(r#""earnings":[]"#),
        "no cycle events means no earnings, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Omitting `period_cycle_counts` is legitimate and must succeed. It is the
/// optional half of the pair, so it carries `serde(default)` where
/// `cycle_events` deliberately does not.
///
/// This is the shape Go actually sends most often: `wire_types.go` puts
/// `omitempty` on the field, which drops the key whenever the map is nil, and
/// a nil map is every first period. Without this test, stripping `default`
/// from the annotation leaves the whole suite green while breaking the most
/// common production call.
#[test]
fn board_calculate_accepts_absent_period_cycle_counts() {
    let mut worker = common::spawn_worker();
    load_board_test_plan(&mut worker);

    let request = format!(
        r#"{{"id":"bp-nocounts","op":"board_calculate_commissions","params":{{"structure":"{}","cycle_events":[]}}}}"#,
        BP_STRUCTURE
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":true"#),
        "an absent period_cycle_counts must default to empty, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Omitting `cycle_events` altogether stays an error. This is the other half of
/// `board_calculate_accepts_null_collections`: widening null must not quietly
/// widen absent, or a caller that forgets the field gets a zero payout instead
/// of a complaint.
#[test]
fn board_calculate_still_requires_cycle_events() {
    let mut worker = common::spawn_worker();
    load_board_test_plan(&mut worker);

    let request = format!(
        r#"{{"id":"bp-noevents","op":"board_calculate_commissions","params":{{"structure":"{}","period_cycle_counts":{{}}}}}}"#,
        BP_STRUCTURE
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PARAMS"),
        "a missing cycle_events must still fail, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// The structure gate. A plan IS loaded, so the error cannot come from
/// `require_plan`. Written without `load_board_test_plan` this test returns
/// NO_PLAN, passes, and proves nothing — the same trap HEU-583 hit with
/// `get_streamline_ref`. The load call is the point of the test.
#[test]
fn board_calculate_unknown_structure_returns_not_found() {
    let mut worker = common::spawn_worker();
    load_board_test_plan(&mut worker);

    let request = r#"{"id":"bp-nostruct","op":"board_calculate_commissions","params":{"structure":"NoSuchBoard","cycle_events":[],"period_cycle_counts":{}}}"#;
    let resp = common::send_receive(&mut worker, request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("STRUCTURE_NOT_FOUND"),
        "expected STRUCTURE_NOT_FOUND, got: {}",
        resp
    );
    assert!(
        resp.contains("NoSuchBoard"),
        "the error should name the structure that missed, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// The money path. A negative cycle_commission must be rejected at load_plan,
/// which is the only gate now that the handler sources config from the plan.
/// The request-scoped `config` param has since been deleted from the wire.
///
/// Asserts the rejection *message*, not just the code. `handle_load_plan`
/// returns INVALID_PLAN for a deserialize failure as well as a validation
/// failure (`handlers/common.rs:123-137`), so a code-only check would stay
/// green if the plan constant drifted and `check_non_negative` were never
/// reached. `load_plan_accepts_valid_board_plan` is the companion that proves
/// the unmutated constant loads.
#[test]
fn load_plan_rejects_board_cycle_commission_negative() {
    let mut worker = common::spawn_worker();

    let bad = BOARD_TEST_PLAN_JSON.replace(
        "\"cycle_commission\": 500.0",
        "\"cycle_commission\": -500.0",
    );
    assert!(
        bad.contains("\"cycle_commission\": -500.0"),
        "the cycle_commission replacement did not match; the plan constant changed shape"
    );

    let resp = send_load_plan(&mut worker, &bad);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PLAN"),
        "expected INVALID_PLAN, got: {}",
        resp
    );
    assert!(
        resp.contains("cycle_commission") && resp.contains("must be finite and non-negative"),
        "expected the board dollar-law gate to reject it, not a deserialize \
         failure or an unrelated check, got: {}",
        resp
    );

    // The rejected plan stored nothing, so this fresh worker still has no plan.
    // A failed load_plan does NOT clear a previously loaded plan; state.plan is
    // only assigned after validation passes. That is why this assertion is only
    // meaningful on a worker that never loaded a good plan.
    let request = format!(
        r#"{{"id":"bp-after-reject","op":"board_calculate_commissions","params":{{"structure":"{}","cycle_events":[],"period_cycle_counts":{{}}}}}}"#,
        BP_STRUCTURE
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("NO_PLAN"),
        "a rejected plan must leave nothing to calculate with, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// The exact legacy shape: config, no structure. An unmigrated caller must
/// fail, not silently continue. This is the case that proves the compatibility
/// requirement; the hybrid test below does not, because it supplies `structure`.
#[test]
fn board_calculate_rejects_legacy_shape_without_structure() {
    let mut worker = common::spawn_worker();
    load_board_test_plan(&mut worker);

    let request = r#"{"id":"bp-legacy","op":"board_calculate_commissions","params":{"cycle_events":[],"period_cycle_counts":{},"config":{"cycle_commission":999999.0,"re_entry_enabled":true,"re_entry_position":"bottom","max_cycles_per_period":99,"max_cascade_depth":10,"stall_threshold_periods":3,"inactive_compression":false}}}"#;
    let resp = common::send_receive(&mut worker, request);
    // Asserts the field name, not just the code. Every deserialize failure in
    // this handler returns INVALID_PARAMS, so a code-only check cannot tell a
    // missing `structure` from any other malformed param. The legacy `config`
    // is an unknown field now and is ignored, which leaves the missing
    // `structure` as the only thing under test. Same reasoning as the
    // money-path test above.
    assert!(
        resp.contains(r#""ok":false"#)
            && resp.contains("INVALID_PARAMS")
            && resp.contains("structure"),
        "a request with no structure must be rejected, naming the missing \
         field, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// The hybrid shape: a valid structure alongside a hostile legacy config.
/// Catches the field being re-added **and honoured**.
///
/// Both hostile values differ from the plan's, and both are asserted. The
/// commission proves `cycle_commission` comes from the plan; the cap proves
/// `max_cycles_per_period` does too. The calculator reads exactly these two
/// fields (`commission/board_plan.rs:35-36`), so together they cover it.
///
/// `config` has since been deleted from `Params`. This test still sends it,
/// and that is the point: nothing sets `deny_unknown_fields`, so the worker
/// ignores the stray field. If anyone adds `deny_unknown_fields`, this flips
/// to INVALID_PARAMS by design rather than as a regression.
#[test]
fn board_calculate_ignores_request_scoped_config() {
    let mut worker = common::spawn_worker();
    load_board_test_plan(&mut worker);

    // Plan says 500.0 and a cap of 3. Hostile config says 999999.0 and 99.
    // Four cycle events for one member: under the plan's cap of 3 the fourth
    // is capped and pays 0. Under the hostile cap of 99 it would pay.
    let hostile = r#""config":{"cycle_commission":999999.0,"re_entry_enabled":true,"re_entry_position":"bottom","max_cycles_per_period":99,"max_cascade_depth":10,"stall_threshold_periods":3,"inactive_compression":false},"#;
    let events = (0..4)
        .map(|_| {
            format!(
                r#"{{"board_id":"00000000-0000-0000-0000-000000000010","cycled_member":"{}","new_boards":[],"re_entry_board":null}}"#,
                ROOT
            )
        })
        .collect::<Vec<_>>()
        .join(",");
    let request = format!(
        r#"{{"id":"bp-hybrid","op":"board_calculate_commissions","params":{{{}"structure":"{}","cycle_events":[{}],"period_cycle_counts":{{}}}}}}"#,
        hostile, BP_STRUCTURE, events
    );
    let resp = common::send_receive(&mut worker, &request);
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap_or(false),
        "legacy-shaped config must be ignored, not rejected, got: {}",
        resp
    );
    let earnings = parsed["result"]["earnings"].as_array().expect(&resp);
    assert_eq!(earnings.len(), 4, "expected four earnings, got: {}", resp);

    let first = earnings[0]["dollar_amount"].as_f64().expect(&resp);
    assert!(
        (first - 500.0).abs() < 1e-10,
        "the request-scoped cycle_commission reached the calculator, got {}: {}",
        first,
        resp
    );

    // Pins the cap at exactly 3, not merely somewhere in 1..=3. The
    // fourth-cycle check alone stays green under a drifted cap of 1 or 2. It
    // also stays green if the `>` at `commission/board_plan.rs:35` became
    // `>=`. Only this assertion catches either.
    let third = earnings[2]["dollar_amount"].as_f64().expect(&resp);
    assert!(
        (third - 500.0).abs() < 1e-10 && !earnings[2]["capped"].as_bool().expect(&resp),
        "the third cycle is within the plan's cap of 3 and must pay, got: {}",
        resp
    );

    let fourth = earnings[3]["dollar_amount"].as_f64().expect(&resp);
    assert!(
        (fourth - 0.0).abs() < 1e-10 && earnings[3]["capped"].as_bool().expect(&resp),
        "the request-scoped max_cycles_per_period reached the calculator; the \
         fourth cycle should be capped under the plan's cap of 3, got: {}",
        resp
    );

    // Records that index is event order rather than leaving it implied.
    assert_eq!(
        earnings[3]["cycle_number"].as_u64().expect(&resp),
        4,
        "earnings must come back in event order, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Catches the field being re-added **at all**, which is the leading indicator:
/// a field usually reappears unused before anything reads it. The value here is
/// a bare number, so if `Params` ever declares `config` again, at any struct
/// type, this request fails deserialization and this test goes red.
///
/// A scalar on purpose, not an object full of junk keys. `{"bogus":true}` also
/// fails today, but only because `BoardPlanConfig`'s fields lack defaults. Add
/// `#[serde(default)]` at the container level, a routine forward-compat edit,
/// and an object payload starts deserializing into a default config while this
/// guard stays green forever. No ordinary derive attribute makes a struct
/// accept a number.
///
/// Do not "improve" it into a realistic config either. A valid one would
/// deserialize cleanly, the payout would still be $500, and the guard would
/// silently stop working.
///
/// `board_calculate_ignores_request_scoped_config` is the other half, catching
/// re-added *and honoured*. The boundary of the pair runs along two axes. By
/// name: both send the literal key `config`, so an override re-added under
/// another name and honoured passes both. By type: this one needs the field to
/// be struct-typed, so a `config` returning as `serde_json::Value` swallows the
/// number and stays green. The honoured half still catches that.
#[test]
fn board_calculate_ignores_malformed_request_config() {
    let mut worker = common::spawn_worker();
    load_board_test_plan(&mut worker);

    // Deliberately un-deserializable at any struct type. Read the note above
    // before changing this.
    let bogus = r#""config":42,"#;
    let request = format!(
        r#"{{"id":"bp-bogus","op":"board_calculate_commissions","params":{{{}"structure":"{}","cycle_events":[{{"board_id":"00000000-0000-0000-0000-000000000010","cycled_member":"{}","new_boards":[],"re_entry_board":null}}],"period_cycle_counts":{{}}}}}}"#,
        bogus, BP_STRUCTURE, ROOT
    );
    let resp = common::send_receive(&mut worker, &request);
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap_or(false),
        "a malformed config must be ignored, not deserialized, got: {}",
        resp
    );
    let dollar = parsed["result"]["earnings"][0]["dollar_amount"]
        .as_f64()
        .expect(&resp);
    assert!(
        (dollar - 500.0).abs() < 1e-10,
        "expected the plan's 500.0, got {}: {}",
        dollar,
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn streamline_create_and_add_members() {
    let mut worker = common::spawn_worker();
    create_streamline(&mut worker);

    sl_add_member(&mut worker, "sl-add-1", SL_USER1, ROOT, 1001);
    sl_add_member(&mut worker, "sl-add-2", SL_USER2, SL_USER1, 1002);
    sl_add_member(&mut worker, "sl-add-3", SL_USER3, SL_USER1, 1003);

    // Verify list_streams shows 1 stream with 3 members.
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sl-list","op":"streamline_list_streams","params":{{"structure":"{}"}}}}"#,
            SL_STRUCTURE
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap());
    let streams = parsed["result"].as_array().unwrap();
    assert_eq!(streams.len(), 1);
    assert_eq!(streams[0]["member_count"].as_u64().unwrap(), 3);

    // Verify get_member shows user2's position.
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sl-member","op":"streamline_get_member","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            SL_STRUCTURE, SL_USER2
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap());
    let streams = parsed["result"]["streams"].as_array().unwrap();
    assert_eq!(streams.len(), 1);
    assert_eq!(streams[0]["stream_id"].as_u64().unwrap(), 1);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn streamline_expand_and_freeze() {
    let mut worker = common::spawn_worker();
    create_streamline(&mut worker);
    sl_add_member(&mut worker, "sl-add-1", SL_USER1, ROOT, 1001);

    // Expand to 3 streams.
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sl-expand","op":"streamline_expand_streams","params":{{"structure":"{}","user_id":"{}","total_allowed":3,"timestamp":1002}}}}"#,
            SL_STRUCTURE, SL_USER1
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap());
    let new_ids = parsed["result"]["new_stream_ids"].as_array().unwrap();
    assert_eq!(new_ids.len(), 2);

    // Freeze back to 1.
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sl-freeze","op":"streamline_update_allowance","params":{{"structure":"{}","user_id":"{}","total_allowed":1,"timestamp":2000}}}}"#,
            SL_STRUCTURE, SL_USER1
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap());
    let frozen = parsed["result"]["frozen"].as_array().unwrap();
    assert_eq!(frozen.len(), 2);

    // Verify stream 2 is frozen.
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sl-stream2","op":"streamline_get_stream","params":{{"structure":"{}","stream_id":2}}}}"#,
            SL_STRUCTURE
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap());
    assert!(parsed["result"]["frozen"].as_bool().unwrap());

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn streamline_snapshot_round_trip() {
    let mut worker = common::spawn_worker();
    create_streamline(&mut worker);
    sl_add_member(&mut worker, "sl-add-1", SL_USER1, ROOT, 1001);
    sl_add_member(&mut worker, "sl-add-2", SL_USER2, SL_USER1, 1002);

    // Take snapshot.
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sl-snap","op":"take_snapshot","params":{{"structure":"{}"}}}}"#,
            SL_STRUCTURE
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap());
    assert_eq!(
        parsed["result"]["tree_type"].as_str().unwrap(),
        "streamline"
    );
    let snapshot_data = &parsed["result"]["data"];

    // Restore under a different name.
    let restore_name = "Restored";
    let restore_req = serde_json::json!({
        "id": "sl-restore",
        "op": "restore_snapshot",
        "params": {
            "structure": restore_name,
            "tree_type": "streamline",
            "data": snapshot_data,
        }
    });
    let resp = common::send_receive(&mut worker, &restore_req.to_string());
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap(), "restore failed: {}", resp);

    // Verify the restored structure has the right member.
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"sl-verify","op":"streamline_get_member","params":{{"structure":"{}","user_id":"{}"}}}}"#,
            restore_name, SL_USER2
        ),
    );
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(parsed["ok"].as_bool().unwrap());
    let streams = parsed["result"]["streams"].as_array().unwrap();
    assert_eq!(streams.len(), 1);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// The HEU-517 gate rejects an out-of-range streamline percent at load time.
/// HEU-583 makes `calculate_streamline` resolve its config from the loaded plan
/// rather than from request params, at which point this gate is the only thing
/// standing between a bad percent and a payout. Pinned before that change so the
/// guard is already in place when the handler stops validating.
///
/// Asserts the rejection *message*, not just the code. `handle_load_plan`
/// returns `INVALID_PLAN` for a deserialize failure as well as a validation
/// failure, so a code-only check would stay green if a schema change broke the
/// plan constant and the fraction gate were never reached.
#[test]
fn load_plan_rejects_streamline_percent_out_of_range() {
    let mut worker = common::spawn_worker();

    let bad = STREAMLINE_TEST_PLAN_JSON.replace("\"percent\": 0.10", "\"percent\": 5.0");
    assert!(
        bad.contains("\"percent\": 5.0"),
        "the percent replacement did not match; the plan constant changed shape"
    );

    let resp = send_load_plan(&mut worker, &bad);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PLAN"),
        "expected INVALID_PLAN, got: {}",
        resp
    );
    assert!(
        resp.contains("dynamic_compression") && resp.contains("must be a fraction"),
        "expected the streamline fraction gate to reject it, not a deserialize \
         failure or an unrelated fraction check, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Companion to the rejection test above. Proves `STREAMLINE_TEST_PLAN_JSON` is
/// otherwise valid, so that rejection comes from the mutated percent and not
/// from drift in the constant. Mirrors `load_plan_accepts_valid_baseline_plan`,
/// which does the same job for `TEST_PLAN_JSON`.
#[test]
fn load_plan_accepts_valid_streamline_plan() {
    let mut worker = common::spawn_worker();
    let resp = send_load_plan(&mut worker, STREAMLINE_TEST_PLAN_JSON);
    assert!(
        resp.contains(r#""ok":true"#),
        "streamline plan should load: {}",
        resp
    );
    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// HEU-605: two structures sharing a name make the `find_*_structure` helpers,
/// which take the first match, pick one commission config over another by plan
/// order with no error anywhere. `CompensationPlan::validate` rejects that, and
/// this pins the wiring: `handle_load_plan` has to surface it as `INVALID_PLAN`
/// instead of storing an ambiguous plan.
///
/// The duplicate is cross-type on purpose. A unilevel and a streamline both
/// named `TestStreamline` reach calculate time through separate lookup helpers,
/// so nothing would collide there. The rule is uniqueness across the whole
/// plan, matching Go's `duplicate_structure_name`.
#[test]
fn load_plan_rejects_duplicate_structure_names() {
    let mut worker = common::spawn_worker();

    // Built by editing the parsed plan rather than by string replacement, which
    // the sibling gate tests use. `"structures": [` also matches the empty
    // `qualification.structures` array on every rank, so a textual insert lands
    // in two places.
    let duplicate_unilevel: serde_json::Value = serde_json::from_str(
        r#"{
            "type": "unilevel",
            "config": {
                "name": "TestStreamline",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": { "member": { "1": 0.05 } }
                },
                "compression": null
            }
        }"#,
    )
    .unwrap();
    let mut plan: serde_json::Value = serde_json::from_str(STREAMLINE_TEST_PLAN_JSON).unwrap();
    let structures = plan["structures"]
        .as_array_mut()
        .expect("the plan constant changed shape: structures is not an array");
    structures.push(duplicate_unilevel);
    let names: Vec<&str> = structures
        .iter()
        .map(|s| s["config"]["name"].as_str().unwrap())
        .collect();
    assert_eq!(
        names,
        vec!["TestStreamline", "TestUnilevel", "TestStreamline"],
        "the plan should hold three structures, two of them sharing one name"
    );
    let bad = plan.to_string();

    let resp = send_load_plan(&mut worker, &bad);
    assert!(
        resp.contains(r#""ok":false"#) && resp.contains("INVALID_PLAN"),
        "expected INVALID_PLAN, got: {}",
        resp
    );
    assert!(
        resp.contains("duplicate structure name") && resp.contains("TestStreamline"),
        "expected the duplicate-name gate to reject it, not a deserialize failure \
         or an unrelated check, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// T5: no plan loaded at all. `require_plan` is the first thing the handler does.
#[test]
fn calculate_streamline_without_plan_returns_no_plan() {
    let mut worker = common::spawn_worker();
    create_streamline(&mut worker);

    let params = format!(
        r#"{{"structure":"{}","snapshots":{{}},"volume":[]}}"#,
        SL_STRUCTURE
    );
    let request = format!(
        r#"{{"id":"calc-sl-np","op":"calculate_streamline","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#),
        "expected failure, got: {}",
        resp
    );
    assert!(resp.contains("NO_PLAN"), "expected NO_PLAN, got: {}", resp);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// T2: a failed load stores nothing, so the following calculate sees no plan.
/// Runs in a fresh worker: `handle_load_plan` writes `state.plan` only on
/// success, so a reused worker with an earlier valid plan would not show
/// `NO_PLAN` here.
#[test]
fn calculate_streamline_after_rejected_plan_returns_no_plan() {
    let mut worker = common::spawn_worker();

    let bad = STREAMLINE_TEST_PLAN_JSON.replace("\"percent\": 0.10", "\"percent\": 5.0");
    let load_resp = send_load_plan(&mut worker, &bad);
    assert!(
        load_resp.contains("INVALID_PLAN"),
        "expected the load to be rejected, got: {}",
        load_resp
    );

    create_streamline(&mut worker);
    let params = format!(
        r#"{{"structure":"{}","snapshots":{{}},"volume":[]}}"#,
        SL_STRUCTURE
    );
    let request = format!(
        r#"{{"id":"calc-sl-rej","op":"calculate_streamline","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#),
        "expected failure, got: {}",
        resp
    );
    assert!(
        resp.contains("NO_PLAN"),
        "an out-of-range percent must not reach the calculator, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// T3: the happy path. Config comes from the loaded plan, not the request.
#[test]
fn calculate_streamline_uses_the_loaded_plan_structure() {
    let mut worker = common::spawn_worker();
    load_streamline_test_plan(&mut worker);
    create_streamline(&mut worker);
    // ROOT is not a stream member, so SL_USER1 becomes the stream root and
    // SL_USER2 sits one level below it. Same shape the other streamline tests
    // use (streamline_create_and_add_members).
    sl_add_member(&mut worker, "sl-m1", SL_USER1, ROOT, 1001);
    sl_add_member(&mut worker, "sl-m2", SL_USER2, SL_USER1, 1002);

    let params = format!(
        r#"{{"structure":"{}","snapshots":{{"{}":{{"rank":"member","personal_volume":100.0,"status":"active","has_order_in_period":true}},"{}":{{"rank":"member","personal_volume":100.0,"status":"active","has_order_in_period":true}}}},"volume":[{{"source_id":"{}","cv_amount":100.0}}]}}"#,
        SL_STRUCTURE, SL_USER1, SL_USER2, SL_USER2
    );
    let request = format!(
        r#"{{"id":"calc-sl-ok","op":"calculate_streamline","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap_or(false),
        "expected success, got: {}",
        resp
    );
    // Assert the whole earning, not just that some field contains "10.0" —
    // a `contains` check prefix-matches 10.05 and would not notice a spurious
    // second earning alongside the correct one.
    // `.expect(&resp)` rather than `.unwrap()` throughout: a bare unwrap fires
    // before assert_eq! formats its message, so a renamed field would panic
    // with "called Option::unwrap() on a None value" and the response body —
    // the only useful part — would never print.
    let earnings = parsed["result"].as_array().expect(&resp);
    assert_eq!(
        earnings.len(),
        1,
        "expected exactly one earning, got: {}",
        resp
    );
    assert_eq!(earnings[0]["earner_id"].as_str().expect(&resp), SL_USER1);
    assert_eq!(earnings[0]["source_id"].as_str().expect(&resp), SL_USER2);
    assert_eq!(earnings[0]["level"].as_u64().expect(&resp), 1);
    // 100 CV * 1.0 multiplier * 0.10 level-1 percent = 10.0. Compared with a
    // tolerance so a change to the multiplication order inside the calculator
    // can't fail this test by one ULP — the config source is what it guards.
    let dollar = earnings[0]["dollar_amount"].as_f64().expect(&resp);
    assert!(
        (dollar - 10.0).abs() < 1e-10,
        "dollar_amount should be 10.0, got {}: {}",
        dollar,
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// The regression guard for the HEU-583 vulnerability itself.
///
/// Sends the legacy wire shape — the request still carrying `plan` and a
/// `structure_config` whose percent is 500% — and asserts the payout still comes
/// from the loaded plan. That is the shape the Go client sent before HEU-583,
/// and since the worker sets no `deny_unknown_fields`, nothing stops a
/// hand-rolled or third-party client from sending it again. Nothing else in this
/// suite would catch a regression: if someone re-adds `structure_config` to
/// `Params` or flattens it back in, every other streamline test stays green and
/// only this one fails.
///
/// The two halves of the payload catch different regressions, so keep both:
///
/// - `structure_config` is *valid*, so it catches a field re-added **and
///   honoured** — the calculation would pay 500000.0 instead of 10.0.
/// - `plan` is deliberately **bogus** (`{"bogus":true}`), so it catches a field
///   re-added **at all**: deserializing it into `CompensationPlan` fails and the
///   request is rejected outright. That is the leading indicator, since a field
///   usually reappears unused before anything reads it.
///
/// Do not "improve" the bogus plan into a realistic one. A valid plan would
/// deserialize cleanly, the payout would still be $10, and the second half of
/// this guard would silently stop working.
#[test]
fn calculate_streamline_ignores_request_scoped_config() {
    let mut worker = common::spawn_worker();
    load_streamline_test_plan(&mut worker);
    create_streamline(&mut worker);
    sl_add_member(&mut worker, "sl-m1", SL_USER1, ROOT, 1001);
    sl_add_member(&mut worker, "sl-m2", SL_USER2, SL_USER1, 1002);

    // percent 5.0 and a 1000x multiplier: if either were honoured the payout
    // would be wildly larger than the plan's $10.
    let hostile_config = r#""structure_config":{"name":"TestStreamline","streamline_commission":{"volume_to_dollar_multiplier":1000.0,"commissionable_depth":5,"dynamic_compression":[{"level":1,"min_rank":"member","percent":5.0}],"streams":null}},"plan":{"bogus":true},"#;
    let params = format!(
        r#"{{{}"structure":"{}","snapshots":{{"{}":{{"rank":"member","personal_volume":100.0,"status":"active","has_order_in_period":true}},"{}":{{"rank":"member","personal_volume":100.0,"status":"active","has_order_in_period":true}}}},"volume":[{{"source_id":"{}","cv_amount":100.0}}]}}"#,
        hostile_config, SL_STRUCTURE, SL_USER1, SL_USER2, SL_USER2
    );
    let request = format!(
        r#"{{"id":"calc-sl-legacy","op":"calculate_streamline","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap_or(false),
        "legacy-shaped params must be ignored, not rejected, got: {}",
        resp
    );
    let earnings = parsed["result"].as_array().expect(&resp);
    assert_eq!(
        earnings.len(),
        1,
        "expected exactly one earning, got: {}",
        resp
    );
    let dollar = earnings[0]["dollar_amount"].as_f64().expect(&resp);
    assert!(
        (dollar - 10.0).abs() < 1e-10,
        "the request-scoped percent reached the calculator, got {}: {}",
        dollar,
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// The engine-side miss, twin of `calculate_streamline_unknown_structure_returns_not_found`.
/// The plan has the structure; no engine was created. Both misses return
/// STRUCTURE_NOT_FOUND and differ only by message, so pinning both messages is
/// what makes the pair meaningful. Mirrors
/// `calculate_unilevel_without_tree_returns_structure_not_found`.
///
/// This pair does NOT pin the order of the two lookups: each test arranges for
/// exactly one of them to miss, so the same one misses either way. Swapping them
/// leaves both green. `calculate_streamline_both_lookups_miss_reports_the_engine`
/// is the test that locks the order.
#[test]
fn calculate_streamline_without_engine_returns_not_found() {
    let mut worker = common::spawn_worker();
    load_streamline_test_plan(&mut worker);
    // Deliberately no create_streamline.

    let params = format!(
        r#"{{"structure":"{}","snapshots":{{}},"volume":[]}}"#,
        SL_STRUCTURE
    );
    let request = format!(
        r#"{{"id":"calc-sl-noengine","op":"calculate_streamline","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#),
        "expected failure, got: {}",
        resp
    );
    assert!(
        resp.contains("STRUCTURE_NOT_FOUND"),
        "expected STRUCTURE_NOT_FOUND, got: {}",
        resp
    );
    assert!(
        resp.contains(&format!("structure '{}' not found", SL_STRUCTURE)),
        "expected the get_streamline_ref miss message, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Locks the order of the two lookups, which the single-miss pair above cannot.
///
/// Neither the engine nor the plan knows "Nowhere", so both lookups miss and the
/// response reports whichever ran first. `get_streamline_ref` runs first, so the
/// engine-side message wins. Swap the two lookups in the handler and only this
/// test fails.
///
/// The order matters because it decides which error a caller sees for a wholly
/// unknown structure, and it matches all five sibling commission handlers, which
/// resolve the tree before the structure.
#[test]
fn calculate_streamline_both_lookups_miss_reports_the_engine() {
    let mut worker = common::spawn_worker();
    load_streamline_test_plan(&mut worker);
    // No engine, and "Nowhere" is not in the plan either.

    let params = r#"{"structure":"Nowhere","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-sl-both","op":"calculate_streamline","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains("STRUCTURE_NOT_FOUND"),
        "expected STRUCTURE_NOT_FOUND, got: {}",
        resp
    );
    assert!(
        resp.contains("structure 'Nowhere' not found"),
        "expected get_streamline_ref to answer first, got: {}",
        resp
    );
    assert!(
        !resp.contains("no streamline structure named"),
        "find_streamline_structure answered first; the lookups are out of order: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// T4: the plan-side miss, distinguished from the engine-side miss. A "Ghost"
/// streamline engine exists, but the plan's only streamline structure is
/// "TestStreamline". `get_streamline_ref` hits; `find_streamline_structure`
/// misses. Mirrors `calculate_matrix_unknown_structure_returns_not_found`.
#[test]
fn calculate_streamline_unknown_structure_returns_not_found() {
    let mut worker = common::spawn_worker();
    load_streamline_test_plan(&mut worker);
    create_streamline_named(&mut worker, "Ghost");

    let params = r#"{"structure":"Ghost","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-sl-us","op":"calculate_streamline","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#),
        "expected failure, got: {}",
        resp
    );
    assert!(
        resp.contains("STRUCTURE_NOT_FOUND"),
        "expected STRUCTURE_NOT_FOUND, got: {}",
        resp
    );
    // Distinct from the no-engine case: this is the find_streamline_structure
    // miss. get_streamline_ref's message is "structure 'Ghost' not found".
    assert!(
        resp.contains("no streamline structure named 'Ghost'"),
        "expected the find_streamline_structure miss message, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Minimal plan used by `evaluate_ranks` integration tests. The single rank
/// "associate" requires PV=50 inside structure "Test", so a distributor with
/// PV=0 falls through to `Unranked`. The plan is shared by the happy-path
/// test and the STRUCTURE_NOT_FOUND error-path test.
const RANK_TEST_PLAN_JSON: &str = r#"{
    "name": "RankTest",
    "version": 1,
    "structures": [
        {"type": "unilevel", "config": {
            "name": "Test",
            "level_commission": {
                "broad_commission_percent": 0.4,
                "volume_to_dollar_multiplier": null,
                "commissionable_depth": 3,
                "rate_table": {"associate": {"1": 0.05}}
            },
            "compression": null
        }}
    ],
    "period": {"length": "month", "start_date": "2026-03-01", "payout_lag_days": 14},
    "volume": {"inhibit_signup_volume": false, "base_currency": "USD", "volume_to_dollar_multiplier": 1.0, "deduct_qualifying_volume": false},
    "ranks": [
        {"name": "associate", "ordinal": 1,
         "qualification": {"structures": [{"structure": "Test", "personal_volume": 50.0, "group_volume": 0.0, "max_group_volume_per_leg": 1e12, "min_retail_volume": 0.0, "distributor_count": null}], "required_products": []},
         "qualified_structures": ["Test"],
         "demotion_policy": "promotion_only"}
    ],
    "rank_tracking": {"track_achieved_rank": false},
    "rank_features": {"constraints_enabled": false, "overrides_enabled": false},
    "commission_eligibility": {"min_personal_volume": 0.0, "require_order_in_period": false, "eligible_statuses": [], "active_leg_tiers": []},
    "bonuses": {"matching": null, "sponsor": null, "fast_start": null, "rank_advancement": null, "leadership_development": null, "infinity": null, "lifestyle": null, "pool": null, "matrix_completion": null, "position": null, "board_cycling": null},
    "payout": {"base_currency": "USD", "minimum_amount": 50.0, "split_payouts_enabled": true, "methods": [{"type": "bank_transfer", "fee": 0.0}]},
    "caps": {"per_distributor_per_period": null, "company_payout_cap_percent": 0.42, "cap_enforcement": "pro_rata", "clawback_on_refund": false},
    "placement": {"donated_placement": null, "holding_tank": null, "binary_placement": null}
}"#;

#[test]
fn evaluate_ranks_returns_unranked_for_zero_pv_distributor() {
    let mut child = common::spawn_worker();

    // The wire protocol sends one JSON object per line. Minify the plan JSON
    // so embedded newlines don't fragment the request.
    let minified_plan: String = RANK_TEST_PLAN_JSON
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"1","op":"load_plan","params":{}}}"#,
            minified_plan
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "load_plan failed: {}", resp);

    create_tree(&mut child, "Test");
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"2","op":"add_root","params":{{"structure":"Test","user_id":"{}","enrolled_at":100}}}}"#,
            ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "add_root failed: {}", resp);

    let req = format!(
        r#"{{"id":"3","op":"evaluate_ranks","params":{{"distributors":{{"{}":{{"personal_volume":0.0,"retail_volume":0.0,"status":"active","has_order_in_period":false,"active_products":[]}}}},"volume_sources":[]}}}}"#,
        ROOT
    );
    let resp = common::send_receive(&mut child, &req);
    // Parse structurally so future fields with "kind" in their name can't
    // false-positive a substring match.
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert_eq!(
        parsed["ok"].as_bool(),
        Some(true),
        "evaluate_ranks failed: {}",
        resp
    );
    assert_eq!(
        parsed["result"]["ranks"][ROOT]["kind"], "unranked",
        "expected unranked result for ROOT, got: {}",
        resp
    );

    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn evaluate_ranks_returns_structure_not_found_when_tree_missing() {
    // The rank ladder references structure "Test" but we deliberately skip
    // create_tree, so the handler must surface STRUCTURE_NOT_FOUND. This
    // exercises the wire contract for the error path Task 21's Go DTOs need
    // to mirror.
    let mut child = common::spawn_worker();

    let minified_plan: String = RANK_TEST_PLAN_JSON
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"1","op":"load_plan","params":{}}}"#,
            minified_plan
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "load_plan failed: {}", resp);

    // No create_tree("Test") here.

    let req = format!(
        r#"{{"id":"2","op":"evaluate_ranks","params":{{"distributors":{{"{}":{{"personal_volume":0.0,"retail_volume":0.0,"status":"active","has_order_in_period":false,"active_products":[]}}}},"volume_sources":[]}}}}"#,
        ROOT
    );
    let resp = common::send_receive(&mut child, &req);
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert_eq!(
        parsed["ok"].as_bool(),
        Some(false),
        "expected ok=false, got: {}",
        resp
    );
    assert_eq!(
        parsed["error"]["code"], "STRUCTURE_NOT_FOUND",
        "expected STRUCTURE_NOT_FOUND error code, got: {}",
        resp
    );
    let message = parsed["error"]["message"]
        .as_str()
        .unwrap_or_else(|| panic!("error.message must be a string, got: {}", resp));
    assert!(
        message.contains("Test"),
        "expected error message to reference structure 'Test', got: {}",
        message
    );

    drop(child.stdin.take());
    child.wait().unwrap();
}

/// Minimal plan for windowed-gate tests. Two ranks on structure "Test":
/// - `associate` (ordinal 1): PV >= 0 — serves as `threshold_rank` for the window.
/// - `silver` (ordinal 2): PV >= 0 + window gate (2-of-3 periods at >= associate).
///
/// Both ranks list structure "Test" so they are evaluated; the window gate is
/// the only differentiator. Key ordering is authored with `type` before
/// `config` deliberately: `StructureConfig` is an adjacent-tagged enum whose
/// `config` content holds the integer-keyed `rate_table`. If `config` precedes
/// `type`, serde buffers the content before the variant is known and the
/// string->u8 key coercion is stripped, so deserialization fails (see
/// docs/development/network-engine.md and UC-NET-007). Verified empirically.
const WINDOWED_RANK_TEST_PLAN_JSON: &str = r#"{
    "name": "WindowedRankTest",
    "version": 1,
    "structures": [
        {"type": "unilevel", "config": {
            "name": "Test",
            "level_commission": {
                "broad_commission_percent": 0.4,
                "volume_to_dollar_multiplier": null,
                "commissionable_depth": 3,
                "rate_table": {"associate": {"1": 0.05}}
            },
            "compression": null
        }}
    ],
    "period": {"length": "month", "start_date": "2026-03-01", "payout_lag_days": 14},
    "volume": {"inhibit_signup_volume": false, "base_currency": "USD", "volume_to_dollar_multiplier": 1.0, "deduct_qualifying_volume": false},
    "ranks": [
        {"name": "associate", "ordinal": 1,
         "qualification": {"structures": [{"structure": "Test", "personal_volume": 0.0, "group_volume": 0.0, "max_group_volume_per_leg": 1e12, "min_retail_volume": 0.0, "distributor_count": null}], "required_products": []},
         "qualified_structures": ["Test"],
         "demotion_policy": "promotion_only"},
        {"name": "silver", "ordinal": 2,
         "qualification": {"structures": [{"structure": "Test", "personal_volume": 0.0, "group_volume": 0.0, "max_group_volume_per_leg": 1e12, "min_retail_volume": 0.0, "distributor_count": null}], "required_products": [], "window": {"threshold_rank": "associate", "qualifying_periods": 2, "window_periods": 3}},
         "qualified_structures": ["Test"],
         "demotion_policy": "promotion_only"}
    ],
    "rank_tracking": {"track_achieved_rank": false},
    "rank_features": {"constraints_enabled": false, "overrides_enabled": false},
    "commission_eligibility": {"min_personal_volume": 0.0, "require_order_in_period": false, "eligible_statuses": [], "active_leg_tiers": []},
    "bonuses": {"matching": null, "sponsor": null, "fast_start": null, "rank_advancement": null, "leadership_development": null, "infinity": null, "lifestyle": null, "pool": null, "matrix_completion": null, "position": null, "board_cycling": null},
    "payout": {"base_currency": "USD", "minimum_amount": 50.0, "split_payouts_enabled": true, "methods": [{"type": "bank_transfer", "fee": 0.0}]},
    "caps": {"per_distributor_per_period": null, "company_payout_cap_percent": 0.42, "cap_enforcement": "pro_rata", "clawback_on_refund": false},
    "placement": {"donated_placement": null, "holding_tank": null, "binary_placement": null}
}"#;

#[test]
fn evaluate_ranks_honors_window_history() {
    // ROOT has ordinal 1 (associate) in all three history periods, which
    // satisfies the silver rank's 2-of-3 window gate. The test proves that
    // history fields carried in the evaluate_ranks request flow through the
    // worker handler unchanged.
    let mut child = common::spawn_worker();

    let minified_plan: String = WINDOWED_RANK_TEST_PLAN_JSON
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"1","op":"load_plan","params":{}}}"#,
            minified_plan
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "load_plan failed: {}", resp);

    create_tree(&mut child, "Test");
    let resp = common::send_receive(
        &mut child,
        &format!(
            r#"{{"id":"2","op":"add_root","params":{{"structure":"Test","user_id":"{}","enrolled_at":100}}}}"#,
            ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "add_root failed: {}", resp);

    // history: ROOT achieved ordinal 1 (associate) in every axis period →
    // 3/3 periods pass the threshold, satisfying the 2-of-3 window gate.
    let req = format!(
        r#"{{"id":"3","op":"evaluate_ranks","params":{{"distributors":{{"{}":{{"personal_volume":0.0,"retail_volume":0.0,"status":"active","has_order_in_period":false,"active_products":[]}}}},"volume_sources":[],"history_window":["2026-05","2026-04","2026-03"],"history":{{"{}":{{"2026-05":1,"2026-04":1,"2026-03":1}}}}}}}}"#,
        ROOT, ROOT
    );
    let resp = common::send_receive(&mut child, &req);
    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert_eq!(
        parsed["ok"].as_bool(),
        Some(true),
        "evaluate_ranks failed: {}",
        resp
    );
    assert_eq!(
        parsed["result"]["ranks"][ROOT]["kind"], "qualified",
        "expected qualified result for ROOT, got: {}",
        resp
    );
    assert_eq!(
        parsed["result"]["ranks"][ROOT]["rank"], "silver",
        "expected rank=silver for ROOT, got: {}",
        resp
    );
    assert_eq!(
        parsed["result"]["ranks"][ROOT]["ordinal"], 2,
        "expected ordinal=2 for ROOT, got: {}",
        resp
    );

    drop(child.stdin.take());
    child.wait().unwrap();
}

#[test]
fn calculate_unilevel_wrong_tree_type_reports_expected_vs_actual() {
    let mut worker = common::spawn_worker();
    load_test_plan(&mut worker);
    // Create a binary tree under the name the unilevel op will target.
    create_binary_tree(&mut worker, TREE_NAME);

    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-wrong","op":"calculate_unilevel","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("INVALID_PARAMS"),
        "expected INVALID_PARAMS, got: {}",
        resp
    );
    assert!(
        resp.contains("is a binary tree, not a unilevel tree"),
        "expected expected-vs-actual message, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Matrix test plan. Same envelope as TEST_PLAN_JSON, but structures[0] is a
/// matrix structure. level_commission is identical to the unilevel fixture, so
/// it pays the same way; matrix_params drives width/height.
const MATRIX_TEST_PLAN_JSON: &str = r#"{
    "name": "Integration Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "matrix",
            "config": {
                "name": "Test",
                "matrix_params": { "width": 3, "height": 3, "spillover_direction": "breadth_first" },
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": { "member": { "1": 0.05, "2": 0.05, "3": 0.05 } }
                },
                "compression": null,
                "pruning": null
            }
        }
    ],
    "period": { "length": "month", "start_date": "2026-03-01", "payout_lag_days": 14 },
    "volume": { "inhibit_signup_volume": false, "base_currency": "USD", "volume_to_dollar_multiplier": 1.0, "deduct_qualifying_volume": false },
    "ranks": [
        { "name": "member", "ordinal": 1, "qualification": { "structures": [], "required_products": [] }, "qualified_structures": ["Test"], "demotion_policy": "promotion_only" }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": { "min_personal_volume": 0.0, "require_order_in_period": false, "eligible_statuses": [], "active_leg_tiers": [] },
    "bonuses": { "matching": null, "sponsor": null, "fast_start": null, "rank_advancement": null, "leadership_development": null, "infinity": null, "lifestyle": null, "pool": null, "matrix_completion": null, "position": null, "board_cycling": null },
    "payout": { "base_currency": "USD", "minimum_amount": 50.0, "split_payouts_enabled": true, "methods": [ { "type": "bank_transfer", "fee": 2.50 } ] },
    "caps": { "per_distributor_per_period": null, "company_payout_cap_percent": 0.42, "cap_enforcement": "pro_rata", "clawback_on_refund": false },
    "placement": { "donated_placement": null, "holding_tank": null, "binary_placement": null }
}"#;

fn load_matrix_test_plan(worker: &mut std::process::Child) {
    let minified: String = MATRIX_TEST_PLAN_JSON
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let request = format!(
        r#"{{"id":"load-plan","op":"load_plan","params":{}}}"#,
        minified
    );
    let resp = common::send_receive(worker, &request);
    assert!(
        resp.contains(r#""ok":true"#),
        "load_plan (matrix) failed: {}",
        resp
    );
}

/// Creates a matrix tree. Matrix create_tree needs width (>= 2) and spillover.
fn create_matrix_tree(child: &mut std::process::Child, name: &str) {
    let resp = common::send_receive(
        child,
        &format!(
            r#"{{"id":"setup-mat","op":"create_tree","params":{{"structure":"{}","tree_type":"matrix","width":3,"spillover":"breadth_first"}}}}"#,
            name
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "create_tree (matrix) failed: {}",
        resp
    );
}

/// Creates a matrix tree with an explicit width, to exercise topology mismatch.
fn create_matrix_tree_with_width(child: &mut std::process::Child, name: &str, width: u8) {
    let resp = common::send_receive(
        child,
        &format!(
            r#"{{"id":"setup-mat-w","op":"create_tree","params":{{"structure":"{}","tree_type":"matrix","width":{},"spillover":"breadth_first"}}}}"#,
            name, width
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "create_tree (matrix width {}) failed: {}",
        width,
        resp
    );
}

#[test]
fn calculate_matrix_pays_upline() {
    let mut worker = common::spawn_worker();
    load_matrix_test_plan(&mut worker);
    create_matrix_tree(&mut worker, TREE_NAME);

    // root(001); child(002) sponsored by root -> auto-placed under root.
    // Matrix add_node takes no parent_id (placement is automatic).
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"m-root","op":"add_root","params":{{"structure":"{}","user_id":"{}","enrolled_at":100}}}}"#,
            TREE_NAME, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "add_root failed: {}", resp);
    let resp = common::send_receive(
        &mut worker,
        &format!(
            r#"{{"id":"m-child","op":"add_node","params":{{"structure":"{}","user_id":"{}","sponsor_id":"{}","enrolled_at":200}}}}"#,
            TREE_NAME, CHILD, ROOT
        ),
    );
    assert!(resp.contains(r#""ok":true"#), "add_node failed: {}", resp);

    // Volume at child -> root earns at level 1.
    let snap =
        r#"{"rank":"member","personal_volume":100.0,"status":"active","has_order_in_period":true}"#;
    let params = format!(
        r#"{{"structure":"Test","snapshots":{{"{root}":{snap},"{child}":{snap}}},"volume":[{{"source_id":"{child}","cv_amount":100.0}}]}}"#,
        root = ROOT,
        child = CHILD,
        snap = snap,
    );
    let request = format!(
        r#"{{"id":"calc-m","op":"calculate_matrix","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap(),
        "calculate_matrix failed: {}",
        resp
    );
    assert_eq!(parsed["id"], "calc-m");
    let earnings = parsed["result"].as_array().unwrap();
    // Volume at child (100 CV) pays only its upline, root, at level 1:
    // 100 * 0.40 (broad_pct) * 1.0 (multiplier) * 0.05 (rate) = 2.0.
    assert_eq!(
        earnings.len(),
        1,
        "expected exactly 1 matrix earning, got: {}",
        resp
    );
    let root_earning = earnings
        .iter()
        .find(|e| e["earner_id"].as_str().unwrap() == ROOT)
        .expect("root should have earned");
    assert_eq!(root_earning["level"].as_u64().unwrap(), 1);
    assert_eq!(root_earning["source_id"].as_str().unwrap(), CHILD);
    let root_dollar = root_earning["dollar_amount"].as_f64().unwrap();
    assert!(
        (root_dollar - 2.0).abs() < 1e-10,
        "root dollar_amount should be 2.0, got {}",
        root_dollar
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_matrix_wrong_tree_type_returns_invalid_params() {
    let mut worker = common::spawn_worker();
    load_matrix_test_plan(&mut worker);
    // A unilevel tree under the matrix structure's name.
    create_tree(&mut worker, TREE_NAME);

    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-m-wrong","op":"calculate_matrix","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("INVALID_PARAMS"),
        "expected INVALID_PARAMS, got: {}",
        resp
    );
    assert!(
        resp.contains("is a unilevel tree, not a matrix tree"),
        "expected expected-vs-actual message, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_matrix_topology_mismatch_returns_invalid_params() {
    let mut worker = common::spawn_worker();
    load_matrix_test_plan(&mut worker);
    // Plan's "Test" matrix is width 3; build the tree 2-wide to force a mismatch.
    create_matrix_tree_with_width(&mut worker, TREE_NAME, 2);

    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-m-mismatch","op":"calculate_matrix","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(
        resp.contains(r#""ok":false"#),
        "expected ok:false, got: {}",
        resp
    );
    assert!(
        resp.contains("INVALID_PARAMS"),
        "expected INVALID_PARAMS, got: {}",
        resp
    );
    assert!(
        resp.contains("does not match config"),
        "expected topology-mismatch message, got: {}",
        resp
    );
    assert!(
        resp.contains("width 2 vs expected 3"),
        "expected the expected-vs-actual width in the message (BR3), got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_matrix_without_plan_returns_no_plan() {
    let mut worker = common::spawn_worker();
    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-m-np","op":"calculate_matrix","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("NO_PLAN"), "expected NO_PLAN, got: {}", resp);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_matrix_structure_not_found_when_no_tree() {
    let mut worker = common::spawn_worker();
    load_matrix_test_plan(&mut worker);
    // Plan loaded, but no tree created under "Test" -> require_matrix_tree misses.
    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-m-nf","op":"calculate_matrix","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
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
fn calculate_matrix_unknown_structure_returns_not_found() {
    let mut worker = common::spawn_worker();
    load_matrix_test_plan(&mut worker);
    // A matrix tree exists under "Ghost", but the plan's only matrix structure
    // is named "Test". require_matrix_tree hits; find_matrix_structure misses.
    create_matrix_tree(&mut worker, "Ghost");

    let params = r#"{"structure":"Ghost","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-m-us","op":"calculate_matrix","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("STRUCTURE_NOT_FOUND"),
        "expected STRUCTURE_NOT_FOUND, got: {}",
        resp
    );
    // Distinct from the no-tree case: this is the find_matrix_structure miss.
    assert!(
        resp.contains("no matrix structure named 'Ghost'"),
        "expected the find_matrix_structure miss message, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Stairstep test plan. structures[0] is a stairstep structure with the same
/// level_commission block as the unilevel fixture. Walk 1 (level commissions)
/// pays regardless of breakaway, so breakaway: null still pays.
const STAIRSTEP_TEST_PLAN_JSON: &str = r#"{
    "name": "Integration Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "stairstep",
            "config": {
                "name": "Test",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": { "member": { "1": 0.05, "2": 0.05, "3": 0.05 } }
                },
                "compression": null,
                "breakaway": null
            }
        }
    ],
    "period": { "length": "month", "start_date": "2026-03-01", "payout_lag_days": 14 },
    "volume": { "inhibit_signup_volume": false, "base_currency": "USD", "volume_to_dollar_multiplier": 1.0, "deduct_qualifying_volume": false },
    "ranks": [
        { "name": "member", "ordinal": 1, "qualification": { "structures": [], "required_products": [] }, "qualified_structures": ["Test"], "demotion_policy": "promotion_only" }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": { "min_personal_volume": 0.0, "require_order_in_period": false, "eligible_statuses": [], "active_leg_tiers": [] },
    "bonuses": { "matching": null, "sponsor": null, "fast_start": null, "rank_advancement": null, "leadership_development": null, "infinity": null, "lifestyle": null, "pool": null, "matrix_completion": null, "position": null, "board_cycling": null },
    "payout": { "base_currency": "USD", "minimum_amount": 50.0, "split_payouts_enabled": true, "methods": [ { "type": "bank_transfer", "fee": 2.50 } ] },
    "caps": { "per_distributor_per_period": null, "company_payout_cap_percent": 0.42, "cap_enforcement": "pro_rata", "clawback_on_refund": false },
    "placement": { "donated_placement": null, "holding_tank": null, "binary_placement": null }
}"#;

fn load_stairstep_test_plan(worker: &mut std::process::Child) {
    let minified: String = STAIRSTEP_TEST_PLAN_JSON
        .lines()
        .map(|l| l.trim())
        .collect::<Vec<_>>()
        .join("");
    let request = format!(
        r#"{{"id":"load-plan","op":"load_plan","params":{}}}"#,
        minified
    );
    let resp = common::send_receive(worker, &request);
    assert!(
        resp.contains(r#""ok":true"#),
        "load_plan (stairstep) failed: {}",
        resp
    );
}

#[test]
fn calculate_stairstep_pays_upline() {
    let mut worker = common::spawn_worker();
    load_stairstep_test_plan(&mut worker);
    // Stairstep operates on a unilevel tree. build_three_node_chain creates a
    // unilevel tree named "Test" (root -> child -> grandchild).
    build_three_node_chain(&mut worker);

    // Volume at grandchild -> child (L1) and root (L2) earn.
    let snap =
        r#"{"rank":"member","personal_volume":100.0,"status":"active","has_order_in_period":true}"#;
    let params = format!(
        r#"{{"structure":"Test","snapshots":{{"{root}":{snap},"{child}":{snap},"{gc}":{snap}}},"volume":[{{"source_id":"{gc}","cv_amount":100.0}}]}}"#,
        root = ROOT,
        child = CHILD,
        gc = GRANDCHILD,
        snap = snap,
    );
    let request = format!(
        r#"{{"id":"calc-s","op":"calculate_stairstep","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);

    let parsed: serde_json::Value = serde_json::from_str(&resp).unwrap();
    assert!(
        parsed["ok"].as_bool().unwrap(),
        "calculate_stairstep failed: {}",
        resp
    );
    assert_eq!(parsed["id"], "calc-s");
    let earnings = parsed["result"].as_array().unwrap();
    // Volume at grandchild (100 CV) pays up the chain: child at level 1 and
    // root at level 2, each 100 * 0.40 * 1.0 * 0.05 = 2.0.
    assert_eq!(
        earnings.len(),
        2,
        "expected exactly 2 stairstep earnings, got: {}",
        resp
    );
    let child_earning = earnings
        .iter()
        .find(|e| e["earner_id"].as_str().unwrap() == CHILD)
        .expect("child should have earned");
    assert_eq!(child_earning["level"].as_u64().unwrap(), 1);
    assert_eq!(child_earning["source_id"].as_str().unwrap(), GRANDCHILD);
    let child_dollar = child_earning["dollar_amount"].as_f64().unwrap();
    assert!(
        (child_dollar - 2.0).abs() < 1e-10,
        "child dollar_amount should be 2.0, got {}",
        child_dollar
    );
    let root_earning = earnings
        .iter()
        .find(|e| e["earner_id"].as_str().unwrap() == ROOT)
        .expect("root should have earned");
    assert_eq!(root_earning["level"].as_u64().unwrap(), 2);
    assert_eq!(root_earning["source_id"].as_str().unwrap(), GRANDCHILD);
    let root_dollar = root_earning["dollar_amount"].as_f64().unwrap();
    assert!(
        (root_dollar - 2.0).abs() < 1e-10,
        "root dollar_amount should be 2.0, got {}",
        root_dollar
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_stairstep_wrong_tree_type_returns_invalid_params() {
    let mut worker = common::spawn_worker();
    load_stairstep_test_plan(&mut worker);
    // A binary tree under the stairstep structure's name.
    create_binary_tree(&mut worker, TREE_NAME);

    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-s-wrong","op":"calculate_stairstep","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("INVALID_PARAMS"),
        "expected INVALID_PARAMS, got: {}",
        resp
    );
    assert!(
        resp.contains("is a binary tree, not a unilevel tree"),
        "expected expected-vs-actual message, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_stairstep_without_plan_returns_no_plan() {
    let mut worker = common::spawn_worker();
    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-s-np","op":"calculate_stairstep","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(resp.contains("NO_PLAN"), "expected NO_PLAN, got: {}", resp);

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

#[test]
fn calculate_stairstep_structure_not_found_when_no_tree() {
    let mut worker = common::spawn_worker();
    load_stairstep_test_plan(&mut worker);
    // Plan loaded, but no tree created under "Test" -> require_unilevel_tree misses.
    let params = r#"{"structure":"Test","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-s-nf","op":"calculate_stairstep","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
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
fn calculate_stairstep_unknown_structure_returns_not_found() {
    let mut worker = common::spawn_worker();
    load_stairstep_test_plan(&mut worker);
    // A unilevel tree exists under "Ghost", but the plan's only stairstep
    // structure is named "Test". require_unilevel_tree hits;
    // find_stairstep_structure misses.
    create_tree(&mut worker, "Ghost");

    let params = r#"{"structure":"Ghost","snapshots":{},"volume":[]}"#;
    let request = format!(
        r#"{{"id":"calc-s-us","op":"calculate_stairstep","params":{}}}"#,
        params
    );
    let resp = common::send_receive(&mut worker, &request);
    assert!(resp.contains(r#""ok":false"#));
    assert!(
        resp.contains("STRUCTURE_NOT_FOUND"),
        "expected STRUCTURE_NOT_FOUND, got: {}",
        resp
    );
    // Distinct from the no-tree case: this is the find_stairstep_structure miss.
    assert!(
        resp.contains("no stairstep structure named 'Ghost'"),
        "expected the find_stairstep_structure miss message, got: {}",
        resp
    );

    drop(worker.stdin.take());
    worker.wait().unwrap();
}

/// Compile-time completeness gate. Every StructureConfig variant must map to a
/// commission op here. Adding a variant without an arm fails to compile the
/// test crate (this fn is test-only), which forces the new op to be named (and
/// therefore wired). This holds only while StructureConfig stays exhaustive: if
/// it ever becomes `#[non_exhaustive]`, Rust would require a `_` arm here and
/// quietly defeat the gate. `allow(dead_code)`: the function exists for its
/// exhaustive match, not to be called.
#[allow(dead_code)]
fn commission_op(structure: &StructureConfig) -> &'static str {
    match structure {
        StructureConfig::Unilevel(_) => "calculate_unilevel",
        StructureConfig::Binary(_) => "calculate_binary_pairing",
        StructureConfig::Matrix(_) => "calculate_matrix",
        StructureConfig::Stairstep(_) => "calculate_stairstep",
        StructureConfig::Generation(_) => "calculate_generation",
        StructureConfig::Streamline(_) => "calculate_streamline",
        StructureConfig::BoardPlan(_) => "board_calculate_commissions",
    }
}

#[test]
fn every_structure_type_has_a_dispatchable_commission_op() {
    // Runtime gate: each op below dispatches to a handler. A dispatched op
    // returns a typed error (NO_PLAN, etc.) on empty params, never UNKNOWN_OP.
    //
    // Primary gate is commission_op above: its exhaustive match is compile-time,
    // so adding a StructureConfig variant without an arm fails to compile the
    // test crate and forces you to name the op. This list is the runtime half:
    // it confirms the named ops actually dispatch. The two are not auto-synced.
    // When commission_op stops compiling on a new variant, name the op there,
    // then add the same string here and bump EXPECTED_OPS. The count assert only
    // flags a mismatch between EXPECTED_OPS and this list; it cannot see an op
    // named in commission_op but never added here. The backstop for that gap is
    // the per-op integration test every real calculator gets, where an unwired
    // op returns UNKNOWN_OP. Fully single-sourcing the list would need enum
    // reflection over StructureConfig, which is not worth it for this gate.
    const EXPECTED_OPS: usize = 7;
    let ops = [
        "calculate_unilevel",
        "calculate_binary_pairing",
        "calculate_matrix",
        "calculate_stairstep",
        "calculate_generation",
        "calculate_streamline",
        "board_calculate_commissions",
    ];
    assert_eq!(
        ops.len(),
        EXPECTED_OPS,
        "ops list is out of sync with commission_op's StructureConfig arms"
    );

    let mut worker = common::spawn_worker();
    for op in ops {
        let resp = common::send_receive(
            &mut worker,
            &format!(r#"{{"id":"gate","op":"{}","params":{{}}}}"#, op),
        );
        assert!(
            !resp.contains("UNKNOWN_OP"),
            "op '{}' is not dispatched (orphaned calculator?): {}",
            op,
            resp
        );
    }
    drop(worker.stdin.take());
    worker.wait().unwrap();
}
