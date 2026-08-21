use serde::{Deserialize, Serialize};

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
    /// Raw JSON params preserved as-is to avoid the serde_json::Value
    /// intermediate representation. Value's BTreeMap key ordering and
    /// buffered content deserialization breaks non-string map keys
    /// (like BTreeMap<u8, f64> in rate tables) and adjacently-tagged
    /// enums when field order differs from tag order.
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
    /// Boxed to keep `Response` under clippy's `large-error-threshold`. It is
    /// the `Err` type of 15 helpers in `handlers/`, and this is the field that
    /// carried the weight. See `response_stays_small_enough_for_clippy` for the
    /// sizes and why they differ between builds.
    ///
    /// `serde` serializes `Box<T>` as `T`, so the NDJSON shape is unchanged.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<Box<serde_json::Value>>,
    /// Boxed for the same reason as `result`.
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
    /// overridden here. `Response` is the error type of 15 helpers in
    /// `handlers/`.
    ///
    /// Run this under `--workspace`. `network-engine` declares
    /// `serde_json/preserve_order` in `[dev-dependencies]`, so a build graph
    /// carrying that crate's dev targets unifies the feature onto our
    /// `serde_json` and `Value` grows from 32 bytes to 72. `Response` is 112
    /// bytes without it and 152 with it. `cargo test -p network-engine-worker`
    /// sees the small one and passes even while clippy is failing.
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

    #[test]
    fn serialize_success_response() {
        let resp = Response::success("req-1".into(), serde_json::json!("pong"));
        let json = serde_json::to_string(&resp).unwrap();
        assert!(json.contains(r#""ok":true"#));
        assert!(json.contains(r#""result":"pong""#));
        assert!(!json.contains("error"));
    }

    #[test]
    fn serialize_error_response() {
        let resp = Response::error("req-1".into(), "NOT_FOUND", "thing not found");
        let json = serde_json::to_string(&resp).unwrap();
        assert!(json.contains(r#""ok":false"#));
        assert!(json.contains(r#""code":"NOT_FOUND""#));
        assert!(!json.contains("result"));
    }
}
