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
    /// Raw NDJSON lines to send as setup verbatim, bypassing the
    /// `serde_json::Value` round-trip. Use this when the fixture needs to
    /// control its own bytes: duplicate keys, or a specific key order the
    /// assertion depends on. Mirrors `request_raw`. Mutually exclusive with
    /// `setup`.
    ///
    /// Not for malformed input. Every setup response is asserted to contain
    /// `"ok":true` in `contract_fixtures_match_worker_behavior`, so a payload
    /// the worker rejects fails the harness rather than exercising anything.
    /// Malformed-input coverage belongs on `request_raw`, where the rejection
    /// is the assertion.
    ///
    /// It is no longer needed just because a fixture loads a plan. The
    /// round-trip reorders keys, which used to break adjacent-tagged enum
    /// payloads; HEU-648 made that order parse fine.
    #[serde(default)]
    setup_raw: Vec<String>,
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
        if path.extension().is_none_or(|ext| ext != "json") {
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

/// Compares two JSON values, reading numbers as exact decimals. Returns the
/// first difference, led by its JSON Pointer.
fn compare_json(
    path: &str,
    expected: &serde_json::Value,
    actual: &serde_json::Value,
) -> Result<(), String> {
    use serde_json::Value;
    let at = if path.is_empty() { "(root)" } else { path };
    match (expected, actual) {
        (Value::Number(e), Value::Number(a)) => {
            let (e_text, a_text) = (e.to_string(), a.to_string());
            let same = match (canonical_number(&e_text), canonical_number(&a_text)) {
                (Some(x), Some(y)) => x == y,
                _ => e_text == a_text,
            };
            if same {
                Ok(())
            } else {
                Err(format!("{at}: expected {e}, got {a}"))
            }
        }
        (Value::Object(e), Value::Object(a)) => {
            for (key, ev) in e {
                match a.get(key) {
                    Some(av) => compare_json(&format!("{path}/{key}"), ev, av)?,
                    None => return Err(format!("{path}/{key}: expected key is absent")),
                }
            }
            match a.keys().find(|key| !e.contains_key(*key)) {
                Some(key) => Err(format!("{path}/{key}: unexpected key")),
                None => Ok(()),
            }
        }
        (Value::Array(e), Value::Array(a)) if e.len() == a.len() => {
            for (i, (ev, av)) in e.iter().zip(a).enumerate() {
                compare_json(&format!("{path}/{i}"), ev, av)?;
            }
            Ok(())
        }
        (Value::Array(e), Value::Array(a)) => Err(format!(
            "{at}: expected an array of {}, got an array of {}",
            e.len(),
            a.len()
        )),
        _ if expected == actual => Ok(()),
        _ => Err(format!("{at}: expected {expected}, got {actual}")),
    }
}

/// A JSON number's sign, significant digits and power of ten, with leading and
/// trailing zeros removed. Zero has no sign. `None` when the exponent does not
/// fit an `i64`.
fn canonical_number(text: &str) -> Option<(bool, String, i64)> {
    let (negative, body) = match text.strip_prefix('-') {
        Some(rest) => (true, rest),
        None => (false, text),
    };
    let (mantissa, exponent_text) = match body.find(['e', 'E']) {
        Some(at) => (&body[..at], Some(&body[at + 1..])),
        None => (body, None),
    };
    let (whole, fraction) = mantissa.split_once('.').unwrap_or((mantissa, ""));
    let digits = format!("{whole}{fraction}");
    let significant = digits.trim_start_matches('0');
    let trimmed = significant.trim_end_matches('0');
    if trimmed.is_empty() {
        return Some((false, String::new(), 0));
    }
    let exponent = match exponent_text {
        Some(e) => e.trim_start_matches('+').parse::<i64>().ok()?,
        None => 0,
    };
    let shift = (significant.len() - trimmed.len()) as i64 - fraction.len() as i64;
    Some((negative, trimmed.to_string(), exponent.checked_add(shift)?))
}

/// Asserts that the actual response matches the expected response from the fixture.
///
/// Comparison rules:
/// - `ok` must match exactly.
/// - `id` must match exactly.
/// - If `expected.result` is present, `actual.result` must match, with numbers
///   compared as exact decimals.
/// - If `expected.error.code` is present, `actual.error.code` must match.
/// - If `expected.error.message_contains` is present, `actual.error.message`
///   must contain it. When the key is absent the message is not compared.
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
        if let Err(difference) = compare_json("", &expected["result"], &actual["result"]) {
            panic!("[{}] 'result' mismatch at {}", fixture_name, difference);
        }
    }

    // If expected has an error, compare the error object.
    if let Some(expected_error) = expected.get("error") {
        let actual_error = actual
            .get("error")
            .unwrap_or_else(|| panic!("[{}] expected error but got none", fixture_name));

        if let Err(e) = check_expected_error(fixture_name, expected_error, actual_error) {
            panic!("{}", e);
        }
    }
}

/// Every key a fixture may put under `expected_response.error`. A key outside
/// this set fails the fixture rather than being ignored, so a misspelled
/// assertion cannot pass by asserting nothing.
const EXPECTED_ERROR_KEYS: [&str; 2] = ["code", "message_contains"];

/// Compares a fixture's expected error object against the worker's actual one.
///
/// `code` is compared exactly when present. `message_contains` is compared as a
/// substring of the actual message when present, and the message is not read at
/// all when it is absent.
fn check_expected_error(
    fixture_name: &str,
    expected_error: &serde_json::Value,
    actual_error: &serde_json::Value,
) -> Result<(), String> {
    let expected_obj = expected_error.as_object().ok_or_else(|| {
        format!(
            "[{}] expected_response.error is not an object: {}",
            fixture_name, expected_error
        )
    })?;

    if expected_obj.is_empty() {
        return Err(format!(
            "[{}] expected_response.error is empty, so it asserts nothing",
            fixture_name
        ));
    }

    for key in expected_obj.keys() {
        if !EXPECTED_ERROR_KEYS.contains(&key.as_str()) {
            return Err(format!(
                "[{}] unrecognized key \"{}\" under expected_response.error",
                fixture_name, key
            ));
        }
    }

    if let Some(expected_code) = expected_obj.get("code") {
        let want = expected_code.as_str().ok_or_else(|| {
            format!(
                "[{}] expected error code is not a JSON string: {}",
                fixture_name, expected_code
            )
        })?;

        let Some(actual_code) = actual_error.get("code") else {
            return Err(format!(
                "[{}] expected error code \"{}\" but the response carried no code",
                fixture_name, want
            ));
        };

        let got = actual_code.as_str().ok_or_else(|| {
            format!(
                "[{}] actual error code is not a JSON string: {}",
                fixture_name, actual_code
            )
        })?;

        if got != want {
            return Err(format!(
                "[{}] error code mismatch: want \"{}\", got \"{}\"",
                fixture_name, want, got
            ));
        }
    }

    let Some(want) = expected_obj.get("message_contains") else {
        return Ok(());
    };

    let want = want.as_str().ok_or_else(|| {
        format!(
            "[{}] message_contains is not a JSON string: {}",
            fixture_name, want
        )
    })?;

    if want.is_empty() {
        return Err(format!(
            "[{}] message_contains is empty, which every message contains",
            fixture_name
        ));
    }

    let Some(actual_message) = actual_error.get("message") else {
        return Err(format!(
            "[{}] message_contains wants \"{}\" but the response carried no error message",
            fixture_name, want
        ));
    };

    let actual_message = actual_message.as_str().ok_or_else(|| {
        format!(
            "[{}] actual error message is not a JSON string: {}",
            fixture_name, actual_message
        )
    })?;

    if !actual_message.contains(want) {
        return Err(format!(
            "[{}] error message does not contain \"{}\"; message was \"{}\"",
            fixture_name, want, actual_message
        ));
    }

    Ok(())
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

        // Fixtures pick exactly one setup mode. Mixing them is ambiguous
        // because ordering across the two lists is undefined.
        assert!(
            fixture.setup.is_empty() || fixture.setup_raw.is_empty(),
            "[{}] fixture has both 'setup' and 'setup_raw'; pick one",
            name
        );

        // Send setup requests to initialize worker state.
        // Each setup response must succeed; a silent failure here would
        // cause the main request to pass vacuously against wrong state.
        // `setup_raw` is sent verbatim (no Value round-trip), mirroring
        // `request_raw`. See the `setup_raw` field's doc comment for when a
        // fixture needs it.
        let setup_lines: Vec<String> = if !fixture.setup_raw.is_empty() {
            fixture.setup_raw.clone()
        } else {
            fixture
                .setup
                .iter()
                .map(|req| {
                    serde_json::to_string(req).expect("setup request serialization is infallible")
                })
                .collect()
        };

        for (i, setup_line) in setup_lines.iter().enumerate() {
            let setup_resp = common::send_receive(&mut worker, setup_line);
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

#[test]
fn check_expected_error_skips_the_message_when_message_contains_is_absent() {
    let expected = serde_json::json!({"code": "INVALID_PLAN"});
    let actual = serde_json::json!({"code": "INVALID_PLAN", "message": "anything at all"});
    assert!(check_expected_error("fx", &expected, &actual).is_ok());
}

#[test]
fn check_expected_error_accepts_a_message_containing_the_substring() {
    let expected =
        serde_json::json!({"code": "INVALID_PLAN", "message_contains": "failed validation"});
    let actual =
        serde_json::json!({"code": "INVALID_PLAN", "message": "plan failed validation: level 3"});
    assert!(check_expected_error("fx", &expected, &actual).is_ok());
}

#[test]
fn check_expected_error_rejects_a_message_missing_the_substring() {
    let expected =
        serde_json::json!({"code": "INVALID_PLAN", "message_contains": "failed validation"});
    let actual =
        serde_json::json!({"code": "INVALID_PLAN", "message": "failed to deserialize plan: eof"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("failed validation"), "{}", err);
    assert!(err.contains("failed to deserialize plan: eof"), "{}", err);
}

#[test]
fn check_expected_error_rejects_message_contains_when_there_is_no_message() {
    let expected =
        serde_json::json!({"code": "INVALID_PLAN", "message_contains": "failed validation"});
    let actual = serde_json::json!({"code": "INVALID_PLAN"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("no error message"), "{}", err);
}

#[test]
fn check_expected_error_rejects_a_code_mismatch() {
    let expected = serde_json::json!({"code": "INVALID_PLAN"});
    let actual = serde_json::json!({"code": "INVALID_PARAMS"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("INVALID_PLAN"), "{}", err);
    assert!(err.contains("INVALID_PARAMS"), "{}", err);
}

#[test]
fn check_expected_error_rejects_a_misspelled_key() {
    let expected =
        serde_json::json!({"code": "INVALID_PLAN", "message_contain": "failed validation"});
    let actual = serde_json::json!({"code": "INVALID_PLAN", "message": "unrelated"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("message_contain"), "{}", err);
}

#[test]
fn check_expected_error_rejects_a_non_object_error() {
    let expected = serde_json::Value::Null;
    let actual = serde_json::json!({"code": "UNKNOWN_OP", "message": "unknown operation: bogus"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("not an object"), "{}", err);
}

#[test]
fn check_expected_error_rejects_a_null_message_contains() {
    let expected = serde_json::json!({"code": "UNKNOWN_OP", "message_contains": null});
    let actual = serde_json::json!({"code": "UNKNOWN_OP", "message": "unknown operation: bogus"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("not a JSON string"), "{}", err);
}

#[test]
fn check_expected_error_rejects_a_non_string_message_contains() {
    let expected = serde_json::json!({"code": "UNKNOWN_OP", "message_contains": 5});
    let actual = serde_json::json!({"code": "UNKNOWN_OP", "message": "unknown operation: bogus"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("not a JSON string"), "{}", err);
}

#[test]
fn check_expected_error_rejects_an_empty_message_contains() {
    let expected = serde_json::json!({"code": "UNKNOWN_OP", "message_contains": ""});
    let actual = serde_json::json!({"code": "UNKNOWN_OP", "message": "unknown operation: bogus"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("empty"), "{}", err);
}

#[test]
fn check_expected_error_rejects_an_empty_error_object() {
    let expected = serde_json::json!({});
    let actual = serde_json::json!({"code": "UNKNOWN_OP", "message": "unknown operation: bogus"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("asserts nothing"), "{}", err);
}

#[test]
fn check_expected_error_rejects_a_null_code() {
    let expected = serde_json::json!({"code": null});
    let actual = serde_json::json!({"message": "unknown operation: bogus"});
    let err = check_expected_error("fx", &expected, &actual).unwrap_err();
    assert!(err.contains("not a JSON string"), "{}", err);
}

/// A fixture may assert the message alone. A message that is diagnostic across
/// two codes is worth pinning without also pinning which code carried it.
#[test]
fn check_expected_error_treats_code_as_optional() {
    let actual = serde_json::json!({"code": "UNKNOWN_OP", "message": "unknown operation: bogus"});

    let expected = serde_json::json!({"message_contains": "bogus"});
    assert!(check_expected_error("fx", &expected, &actual).is_ok());

    let expected = serde_json::json!({"message_contains": "NOTPRESENT"});
    assert!(
        check_expected_error("fx", &expected, &actual).is_err(),
        "the message check must still bite when no code is asserted"
    );
}

#[test]
fn compare_json_treats_equal_decimals_as_equal() {
    let expected: serde_json::Value =
        serde_json::from_str(r#"{"a":2.0,"b":[1,0.50],"c":1e3}"#).unwrap();
    let actual: serde_json::Value =
        serde_json::from_str(r#"{"c":1000,"b":[1.00,0.5],"a":2}"#).unwrap();
    assert_eq!(compare_json("", &expected, &actual), Ok(()));
}

#[test]
fn compare_json_names_the_first_difference() {
    let expected: serde_json::Value = serde_json::from_str(r#"{"a":{"b":[1,2.5]}}"#).unwrap();
    let actual: serde_json::Value = serde_json::from_str(r#"{"a":{"b":[1,2.50001]}}"#).unwrap();
    assert_eq!(
        compare_json("", &expected, &actual),
        Err("/a/b/1: expected 2.5, got 2.50001".to_string())
    );
}

#[test]
fn compare_json_keeps_strings_and_numbers_apart() {
    let expected: serde_json::Value = serde_json::from_str(r#"{"a":"2"}"#).unwrap();
    let actual: serde_json::Value = serde_json::from_str(r#"{"a":2}"#).unwrap();
    assert!(compare_json("", &expected, &actual).is_err());
}

#[test]
fn canonical_number_refuses_an_exponent_past_i64() {
    assert_eq!(canonical_number("1e99999999999999999999"), None);
    assert_eq!(canonical_number("1e-99999999999999999999"), None);
    assert_eq!(canonical_number("10e9223372036854775807"), None);
}

#[test]
fn compare_json_treats_negative_zero_as_zero() {
    let expected: serde_json::Value = serde_json::from_str(r#"[0]"#).unwrap();
    let actual: serde_json::Value = serde_json::from_str(r#"[-0.0]"#).unwrap();
    assert_eq!(compare_json("", &expected, &actual), Ok(()));
}

#[test]
fn compare_json_labels_a_root_difference() {
    assert_eq!(
        compare_json("", &serde_json::json!("x"), &serde_json::json!("y")),
        Err("(root): expected \"x\", got \"y\"".to_string())
    );
}

#[test]
fn compare_json_names_array_lengths_that_differ() {
    assert_eq!(
        compare_json(
            "",
            &serde_json::json!({"a": [1, 2]}),
            &serde_json::json!({"a": [1]})
        ),
        Err("/a: expected an array of 2, got an array of 1".to_string())
    );
}

#[test]
fn canonical_number_reads_zero_with_any_exponent_as_zero() {
    assert_eq!(
        canonical_number("0e99999999999999999999"),
        canonical_number("0")
    );
}
