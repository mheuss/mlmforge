mod common;

use std::path::PathBuf;

use serde::Deserialize;

/// A contract fixture loaded from a JSON file in engine/testdata/contracts/.
///
/// Each fixture describes a single request/response pair. Both Rust and Go
/// contract tests read these same files so serialization drift is caught.
#[derive(Debug, Deserialize)]
struct ContractFixture {
    description: String,
    /// Requests to send before the main request to set up worker state.
    /// Responses are read but not asserted. Used to create trees before
    /// testing operations that require an existing tree.
    #[serde(default)]
    setup: Vec<serde_json::Value>,
    /// Structured request object. Present for well-formed requests.
    #[serde(default)]
    request: Option<serde_json::Value>,
    /// Raw string to send instead of a JSON object. Used for malformed input tests.
    #[serde(default)]
    request_raw: Option<String>,
    expected_response: serde_json::Value,
}

fn fixtures_dir() -> PathBuf {
    // CARGO_MANIFEST_DIR is engine/network-engine-worker/
    // Fixtures live at engine/testdata/contracts/
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("testdata")
        .join("contracts")
}

fn load_fixtures() -> Vec<(String, ContractFixture)> {
    let dir = fixtures_dir();
    let mut fixtures = Vec::new();

    for entry in std::fs::read_dir(&dir)
        .unwrap_or_else(|e| panic!("failed to read fixtures dir {}: {}", dir.display(), e))
    {
        let entry = entry.unwrap();
        let path = entry.path();
        if path.extension().map_or(true, |ext| ext != "json") {
            continue;
        }
        let content = std::fs::read_to_string(&path)
            .unwrap_or_else(|e| panic!("failed to read {}: {}", path.display(), e));
        let fixture: ContractFixture = serde_json::from_str(&content)
            .unwrap_or_else(|e| panic!("failed to parse {}: {}", path.display(), e));

        let name = path.file_stem().unwrap().to_string_lossy().to_string();
        fixtures.push((name, fixture));
    }

    fixtures.sort_by(|a, b| a.0.cmp(&b.0));
    fixtures
}

/// Asserts that the actual response matches the expected response from the fixture.
///
/// Comparison rules:
/// - `ok` must match exactly.
/// - `id` must match exactly.
/// - If `expected.result` is present, `actual.result` must match exactly.
/// - If `expected.error.code` is present, `actual.error.code` must match.
///   The error message is not compared because it may contain implementation details.
fn assert_response_matches(fixture_name: &str, expected: &serde_json::Value, actual: &str) {
    let actual: serde_json::Value = serde_json::from_str(actual)
        .unwrap_or_else(|e| panic!("[{}] failed to parse actual response: {}", fixture_name, e));

    // ok field must match.
    assert_eq!(
        expected["ok"], actual["ok"],
        "[{}] 'ok' mismatch: expected {}, got {}",
        fixture_name, expected["ok"], actual["ok"]
    );

    // id field must match.
    assert_eq!(
        expected["id"], actual["id"],
        "[{}] 'id' mismatch: expected {}, got {}",
        fixture_name, expected["id"], actual["id"]
    );

    // If expected has a result, compare it (including explicit null).
    if expected.get("result").is_some() {
        assert_eq!(
            expected["result"], actual["result"],
            "[{}] 'result' mismatch",
            fixture_name
        );
    }

    // If expected has an error with a code, compare the code.
    if let Some(expected_error) = expected.get("error") {
        let actual_error = actual
            .get("error")
            .unwrap_or_else(|| panic!("[{}] expected error but got none", fixture_name));

        if let Some(expected_code) = expected_error.get("code") {
            assert_eq!(
                expected_code,
                actual_error.get("code").unwrap_or(&serde_json::Value::Null),
                "[{}] error 'code' mismatch",
                fixture_name
            );
        }
    }
}

#[test]
fn contract_fixtures_match_worker_behavior() {
    let fixtures = load_fixtures();
    assert!(
        !fixtures.is_empty(),
        "no contract fixtures found in {}",
        fixtures_dir().display()
    );

    for (name, fixture) in &fixtures {
        // Each fixture gets a fresh worker to avoid state leaking between tests.
        let mut worker = common::spawn_worker();

        // Send setup requests to initialize worker state.
        // Each setup response must succeed; a silent failure here would
        // cause the main request to pass vacuously against wrong state.
        for (i, setup_req) in fixture.setup.iter().enumerate() {
            let setup_line = serde_json::to_string(setup_req)
                .expect("setup request serialization is infallible");
            let setup_resp = common::send_receive(&mut worker, &setup_line);
            assert!(
                setup_resp.contains(r#""ok":true"#),
                "[{}] setup request {} failed: {}",
                name,
                i,
                setup_resp
            );
        }

        let request_line = if let Some(raw) = &fixture.request_raw {
            raw.clone()
        } else if let Some(req) = &fixture.request {
            serde_json::to_string(req).unwrap()
        } else {
            panic!("[{}] fixture has neither 'request' nor 'request_raw'", name);
        };

        let actual = common::send_receive(&mut worker, &request_line);
        assert_response_matches(name, &fixture.expected_response, &actual);

        drop(worker.stdin.take());
        worker.wait().unwrap();

        eprintln!("  contract: {} -- {}", name, fixture.description);
    }
}
