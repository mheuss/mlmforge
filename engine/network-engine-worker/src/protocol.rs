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
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<serde_json::Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<ErrorPayload>,
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
            result: Some(result),
            error: None,
        }
    }

    pub fn error(id: String, code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            id,
            ok: false,
            result: None,
            error: Some(ErrorPayload {
                code: code.into(),
                message: message.into(),
            }),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// `Response` is the error type of 15 `pub(crate)` helpers in `handlers/`.
    /// Clippy's `result_large_err` rejects an `Err` variant over its
    /// `large-error-threshold`, which defaults to 128 bytes.
    ///
    /// Why the lint fires only on the test target: `network-engine` puts
    /// `serde_json`'s `preserve_order` in its `[dev-dependencies]`. Resolver 2
    /// withholds dev-dependency features from non-dev builds, so a `--bins`
    /// build gets a `BTreeMap`-backed `serde_json::Value` at 32 bytes, and a
    /// `--tests` build gets the `IndexMap`-backed one at 72. That moved
    /// `Response` between 112 and 152 bytes — one side of the threshold each.
    ///
    /// Boxing both large fields is what makes this size independent of that
    /// feature, so the bound below means the same thing in either build.
    ///
    /// This bound is what stops a future field on `Response` from silently
    /// pushing it back over. Prefer boxing the new field over raising the
    /// number.
    #[test]
    fn response_stays_small_enough_for_clippy() {
        let size = std::mem::size_of::<Response>();
        assert!(
            size <= 128,
            "Response is {size} bytes, over clippy's 128-byte \
             large-error-threshold; box the largest field rather than \
             raising this bound"
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
