use serde::{Deserialize, Serialize};

/// The NDJSON wire semantics this worker speaks.
///
/// Version 1 is the first *self-reporting* version. A worker that answers
/// `ping` with a bare `"pong"` predates this constant and is versionless --
/// not version 0, and not version 1.
///
/// This moves on any change to wire semantics, not only on shape changes. Two
/// workers can share a schema and still disagree about what a field means.
///
/// There is deliberately no compatibility arm here: the worker has exactly one
/// answer.
pub const PROTOCOL_VERSION: u32 = 1;

/// An NDJSON request from the Go platform layer.
#[derive(Debug, Deserialize)]
pub struct Request {
    pub id: String,
    pub op: String,
    /// Optional trace context from the Go caller, used to correlate the worker's
    /// signals with the caller's trace. These fields are optional, so requests
    /// without them still deserialize. `serde(default)` makes that explicit and
    /// keeps existing contract fixtures unaffected.
    #[serde(default)]
    pub trace_id: Option<String>,
    #[serde(default)]
    pub span_id: Option<String>,
    /// Raw JSON params preserved as-is, so handlers deserialize straight into
    /// their own types instead of through a `serde_json::Value` intermediate.
    /// That skips a full reparse of every request body and keeps the caller's
    /// bytes intact.
    ///
    /// It used to be load-bearing for correctness too: `Value` sorts keys, and
    /// the sorted order made serde buffer an adjacently-tagged enum's content,
    /// which stripped the string-to-integer coercion that `BTreeMap<u8, f64>`
    /// rate tables need. HEU-648 fixed that in the config types, so a `Value`
    /// round-trip no longer breaks a plan. `RawValue` stays for the reasons
    /// above.
    #[serde(default = "default_raw_params")]
    pub params: Box<serde_json::value::RawValue>,
}

fn default_raw_params() -> Box<serde_json::value::RawValue> {
    serde_json::value::RawValue::from_string("{}".to_string()).expect("valid JSON object literal")
}

/// An NDJSON response sent back to the Go platform layer.
#[derive(Debug, Serialize)]
pub struct Response {
    pub id: String,
    pub ok: bool,
    /// Boxed to keep `Response` under clippy's `large-error-threshold`.
    /// `Response` is the `Err` type of the parse and lookup helpers in
    /// `handlers/`, and this is the field that carried the weight: held inline,
    /// a `serde_json::Value` made `Response`'s size depend on a feature flag.
    /// See `response_stays_small_enough_for_clippy`.
    ///
    /// `serde` serializes `Box<T>` as `T`, so the NDJSON shape is unchanged.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<Box<serde_json::Value>>,
    /// Also boxed, for headroom. Boxing `result` alone already clears the
    /// threshold at 88 bytes. Boxing this one too brings `Response` to 48, and
    /// error responses are the cold path.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<Box<ErrorPayload>>,
}

/// Error details included in a failed response.
#[derive(Debug, Serialize)]
pub struct ErrorPayload {
    pub code: String,
    pub message: String,
}

impl Response {
    pub fn success(id: String, result: serde_json::Value) -> Self {
        Self {
            id,
            ok: true,
            result: Some(Box::new(result)),
            error: None,
        }
    }

    pub fn error(id: String, code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            id,
            ok: false,
            result: None,
            error: Some(Box::new(ErrorPayload {
                code: code.into(),
                message: message.into(),
            })),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Clippy's `result_large_err` rejects an `Err` variant over its
    /// `large-error-threshold`, which defaults to 128 bytes and is not
    /// overridden here. `Response` is the error type of the parse and lookup
    /// helpers in `handlers/`.
    ///
    /// It measures 48 bytes, in every build. Before `result` was boxed,
    /// `Response` held a `serde_json::Value` inline and so changed size with
    /// `serde_json`'s `preserve_order` feature, which `network-engine` declared
    /// in `[dev-dependencies]` at the time. It came out at 112 bytes or 152
    /// depending on whether that crate's dev targets were in the build graph.
    /// That is why `clippy --all-targets --workspace` failed while
    /// `clippy --all-targets -p network-engine-worker` passed. Holding
    /// `--all-targets` fixed is what makes the comparison mean anything: plain
    /// `clippy --workspace` was green throughout, which is why CI never caught
    /// this. Boxing removed the coupling.
    ///
    /// HEU-648 later removed the feature entirely, so all three build
    /// configurations now link the same `serde_json`.
    ///
    /// `docs/development/network-engine.md`, "Rust Tests: Package Scope Used
    /// To Lie", covers that build-scope split in full, including the third
    /// configuration the shipped binary used to be.
    ///
    /// Prefer boxing a new field over raising this bound.
    #[test]
    fn response_stays_small_enough_for_clippy() {
        const CLIPPY_LARGE_ERROR_THRESHOLD: usize = 128;

        let size = std::mem::size_of::<Response>();
        assert!(
            size <= CLIPPY_LARGE_ERROR_THRESHOLD,
            "Response is {size} bytes, over clippy's \
             {CLIPPY_LARGE_ERROR_THRESHOLD}-byte large-error-threshold; box the \
             largest field rather than raising this bound"
        );
    }

    #[test]
    fn deserialize_request() {
        let json = r#"{"id":"req-1","op":"ping"}"#;
        let req: Request = serde_json::from_str(json).unwrap();
        assert_eq!(req.id, "req-1");
        assert_eq!(req.op, "ping");
    }

    #[test]
    fn deserialize_request_with_params() {
        let json = r#"{"id":"req-2","op":"add_node","params":{"user_id":"abc"}}"#;
        let req: Request = serde_json::from_str(json).unwrap();
        assert_eq!(req.op, "add_node");
        assert!(req.params.get().contains("user_id"));
    }

    #[test]
    fn deserialize_request_with_trace() {
        let json = r#"{"id":"req-1","op":"ping","trace_id":"abc","span_id":"def"}"#;
        let req: Request = serde_json::from_str(json).unwrap();
        assert_eq!(req.trace_id.as_deref(), Some("abc"));
        assert_eq!(req.span_id.as_deref(), Some("def"));
    }

    #[test]
    fn deserialize_request_without_trace() {
        // No trace fields: they default to None. Protects existing contract
        // fixtures that send requests without trace context.
        let json = r#"{"id":"req-1","op":"ping"}"#;
        let req: Request = serde_json::from_str(json).unwrap();
        assert!(req.trace_id.is_none());
        assert!(req.span_id.is_none());
    }

    /// Exact-string, not `contains`. ADR-019 makes the NDJSON shape a contract
    /// with the Go side, and a substring check passes through an added field, a
    /// renamed one, or a lost `skip_serializing_if`. Field order is not part of
    /// that contract: the Go side decodes with `encoding/json` struct tags,
    /// which ignore order. So a reorder tripping this test is a heads-up, not a
    /// compatibility break.
    ///
    /// This test owns the envelope shape only. The build-graph detector lives
    /// in `value_payload_emits_sorted_keys`, so tidying this fixture cannot
    /// silently delete it.
    #[test]
    fn serialize_success_response() {
        let resp = Response::success("req-1".into(), serde_json::json!("pong"));
        let json = serde_json::to_string(&resp).unwrap();
        assert_eq!(json, r#"{"id":"req-1","ok":true,"result":"pong"}"#);
    }

    /// Detects `serde_json/preserve_order` re-entering the build graph.
    ///
    /// The payload's keys are authored out of alphabetical order on purpose.
    /// `serde_json::Value` is backed by a `BTreeMap`, so it emits them sorted.
    /// With `preserve_order` linked, `Value` becomes an `IndexMap` and emits
    /// insertion order instead, and this fails.
    ///
    /// **Scope matters.** This only fires under `cargo test --workspace`, which
    /// is what CI runs (`.github/workflows/ci.yml`). Under
    /// `cargo test -p network-engine-worker` the feature would not reach this
    /// crate even if it were restored, so the test passes and proves nothing.
    /// That asymmetry is the exact bug HEU-648 removed, and it applies to the
    /// detector as much as to anything else.
    ///
    /// It detects a regression. It does not promise the Go side anything about
    /// key order — see `serialize_success_response` above.
    ///
    /// Before HEU-648 this could not be tested at all. A guard here forced the
    /// payload to stay scalar, because key order depended on whether
    /// `network-engine`'s dev-dependencies were in the build graph.
    #[test]
    fn value_payload_emits_sorted_keys() {
        let payload = serde_json::json!({"zebra": 1, "alpha": 2});
        let resp = Response::success("req-1".into(), payload);
        let json = serde_json::to_string(&resp).unwrap();
        assert_eq!(
            json,
            r#"{"id":"req-1","ok":true,"result":{"alpha":2,"zebra":1}}"#
        );
    }

    /// Exact-string for the same reason as `serialize_success_response`.
    #[test]
    fn serialize_error_response() {
        let resp = Response::error("req-1".into(), "NOT_FOUND", "thing not found");
        let json = serde_json::to_string(&resp).unwrap();
        assert_eq!(
            json,
            r#"{"id":"req-1","ok":false,"error":{"code":"NOT_FOUND","message":"thing not found"}}"#
        );
    }
}
