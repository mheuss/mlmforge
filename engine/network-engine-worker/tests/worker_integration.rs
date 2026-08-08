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

/// Minimal plan carrying a single streamline structure named `TestStreamline`.
/// The name matches `SL_STRUCTURE` so the plan and `create_streamline` agree
/// when a test needs both a loaded plan and a live engine (HEU-583). Its level-1
/// `percent` of 0.10 is the value mutated to exercise the load-time gate.
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
        }
    ],
    "period": { "length": "month", "start_date": "2026-03-01", "payout_lag_days": 14 },
    "volume": { "inhibit_signup_volume": false, "base_currency": "USD", "volume_to_dollar_multiplier": 1.0, "deduct_qualifying_volume": false },
    "ranks": [
        { "name": "member", "ordinal": 1, "qualification": { "structures": [], "required_products": [] }, "qualified_structures": ["TestStreamline"], "demotion_policy": "promotion_only" }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": { "min_personal_volume": 0.0, "require_order_in_period": false, "eligible_statuses": [], "active_leg_tiers": [] },
    "bonuses": { "matching": null, "sponsor": null, "fast_start": null, "rank_advancement": null, "leadership_development": null, "infinity": null, "lifestyle": null, "pool": null, "matrix_completion": null, "position": null, "board_cycling": null },
    "payout": { "base_currency": "USD", "minimum_amount": 50.0, "split_payouts_enabled": true, "methods": [ { "type": "bank_transfer", "fee": 2.50 } ] },
    "caps": { "per_distributor_per_period": null, "company_payout_cap_percent": 0.42, "cap_enforcement": "pro_rata", "clawback_on_refund": false },
    "placement": { "donated_placement": null, "holding_tank": null, "binary_placement": null }
}"#;

fn create_streamline(worker: &mut std::process::Child) {
    let resp = common::send_receive(
        worker,
        &format!(
            r#"{{"id":"sl-create","op":"create_streamline","params":{{"structure":"{}","assignment_mode":"sponsor_stream","freeze_on_demotion":true,"timestamp":1000}}}}"#,
            SL_STRUCTURE
        ),
    );
    assert!(
        resp.contains(r#""ok":true"#),
        "create_streamline failed: {}",
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
