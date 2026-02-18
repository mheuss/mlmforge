use serde::{Deserialize, Serialize};

/// An NDJSON request from the Go platform layer.
#[derive(Debug, Deserialize)]
pub struct Request {
    pub id: String,
    pub op: String,
    /// Raw JSON params preserved as-is to avoid the serde_json::Value
    /// intermediate representation. Value's BTreeMap key ordering and
    /// buffered content deserialization breaks non-string map keys
    /// (like BTreeMap<u8, f64> in rate tables) and adjacently-tagged
    /// enums when field order differs from tag order.
    #[serde(default = "default_raw_params")]
    pub params: Box<serde_json::value::RawValue>,
}

fn default_raw_params() -> Box<serde_json::value::RawValue> {
    serde_json::value::RawValue::from_string("{}".to_string()).unwrap()
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

    pub fn with_id(mut self, id: &str) -> Self {
        self.id = id.to_string();
        self
    }
}

#[cfg(test)]
mod tests {
    use super::*;

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
