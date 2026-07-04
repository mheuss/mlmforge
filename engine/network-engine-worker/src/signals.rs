use std::cell::RefCell;
use std::collections::BTreeMap;
use std::io::Write;

use serde::Serialize;
use tracing::field::{Field, Visit};
use tracing::{Event, Subscriber};
use tracing_log::NormalizeEvent;
use tracing_subscriber::fmt::MakeWriter;
use tracing_subscriber::layer::{Context, Layer};

/// One serialized NDJSON signal message. Mirrors the schema in design-rationale 019.
#[derive(Serialize)]
struct SignalMessage {
    #[serde(rename = "type")]
    kind: &'static str,
    level: String,
    target: String,
    message: String,
    fields: BTreeMap<String, serde_json::Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    trace_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    span_id: Option<String>,
    timestamp: String,
}

/// Collects event fields into a JSON map and extracts the special `message` field.
#[derive(Default)]
struct FieldVisitor {
    fields: BTreeMap<String, serde_json::Value>,
    message: Option<String>,
}

impl Visit for FieldVisitor {
    fn record_f64(&mut self, f: &Field, v: f64) {
        // serde_json maps non-finite floats (NaN, +/-Inf) to null.
        self.fields.insert(f.name().into(), v.into());
    }
    fn record_i64(&mut self, f: &Field, v: i64) {
        self.fields.insert(f.name().into(), v.into());
    }
    fn record_u64(&mut self, f: &Field, v: u64) {
        self.fields.insert(f.name().into(), v.into());
    }
    fn record_bool(&mut self, f: &Field, v: bool) {
        self.fields.insert(f.name().into(), v.into());
    }
    fn record_str(&mut self, f: &Field, v: &str) {
        self.fields.insert(f.name().into(), v.into());
    }
    fn record_debug(&mut self, f: &Field, v: &dyn std::fmt::Debug) {
        let rendered = format!("{v:?}");
        if f.name() == "message" {
            // The macro records the message as `fmt::Arguments`, whose Debug impl
            // renders through Display. So this is the plain message text, with no
            // surrounding quotes to strip.
            self.message = Some(rendered);
        } else {
            // Non-primitive fields, or values written with `?field`, land here and
            // are Debug-stringified: strings gain quotes and numbers become strings.
            // Prefer typed values at call sites so they hit record_str/record_f64
            // and serialize as native JSON.
            self.fields
                .insert(f.name().into(), serde_json::Value::String(rendered));
        }
    }
}

/// A tracing Layer that serializes each event as one NDJSON signal line.
pub struct SignalLayer<W> {
    make_writer: W,
}

impl<W> SignalLayer<W> {
    pub fn new(make_writer: W) -> Self {
        Self { make_writer }
    }
}

impl<S, W> Layer<S> for SignalLayer<W>
where
    S: Subscriber,
    W: for<'a> MakeWriter<'a> + 'static,
{
    fn on_event(&self, event: &Event<'_>, _ctx: Context<'_, S>) {
        // Events bridged from the `log` crate report a static "log" target and
        // push their real module path, file, and line into `log.*` fields.
        // Normalize the metadata so `target` is the emitting module (per 019),
        // then drop the `log.*` fields so `fields` holds only call-site data.
        // Native `tracing` events return None here and use their own metadata.
        let normalized = event.normalized_metadata();
        let meta = normalized.as_ref().unwrap_or_else(|| event.metadata());

        let mut visitor = FieldVisitor::default();
        event.record(&mut visitor);
        visitor.fields.retain(|name, _| !name.starts_with("log."));

        let (trace_id, span_id) = current_trace_context();
        let signal = SignalMessage {
            kind: "signal",
            level: meta.level().as_str().to_ascii_lowercase(),
            target: meta.target().to_string(),
            message: visitor.message.unwrap_or_default(),
            fields: visitor.fields,
            trace_id,
            span_id,
            timestamp: chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::AutoSi, true),
        };

        if let Ok(line) = serde_json::to_string(&signal) {
            let mut w = self.make_writer.make_writer();
            let _ = writeln!(w, "{line}");
            let _ = w.flush();
        }
    }
}

thread_local! {
    // Read by `current_trace_context` inside `SignalLayer::on_event`, i.e. inside
    // arbitrary tracing call sites. Never emit a tracing event while a borrow of
    // this cell is held: `on_event` would re-enter and the nested borrow would
    // panic. The functions below only move tuples, so they are safe.
    static CURRENT_TRACE: RefCell<(Option<String>, Option<String>)> =
        const { RefCell::new((None, None)) };
}

/// Set the trace context for signals emitted on this thread. The worker calls
/// this per request from the incoming trace_id/span_id.
///
/// The matching `clear_trace_context` must run even if a request handler panics.
/// The worker catches handler panics, so wire this pair OUTSIDE that recovery (or
/// behind a drop guard), never inside the panicking closure. Otherwise a stale
/// trace context leaks into the next request on this long-lived thread.
pub(crate) fn set_trace_context(trace_id: Option<String>, span_id: Option<String>) {
    CURRENT_TRACE.with(|c| *c.borrow_mut() = (trace_id, span_id));
}

/// Clear the trace context. See `set_trace_context` for the panic-safety rule.
pub(crate) fn clear_trace_context() {
    CURRENT_TRACE.with(|c| *c.borrow_mut() = (None, None));
}

/// Read the current thread's trace context. The Layer calls this per event.
pub(crate) fn current_trace_context() -> (Option<String>, Option<String>) {
    CURRENT_TRACE.with(|c| c.borrow().clone())
}

#[cfg(test)]
mod tests {
    use super::SignalLayer;
    use std::io::Write;
    use std::sync::{Arc, Mutex};
    use tracing_subscriber::Registry;
    use tracing_subscriber::fmt::MakeWriter;
    use tracing_subscriber::layer::SubscriberExt;

    /// A `MakeWriter` that appends every write into a shared buffer, so the test
    /// can inspect exactly what the layer emitted.
    #[derive(Clone)]
    struct BufWriter(Arc<Mutex<Vec<u8>>>);

    impl<'a> MakeWriter<'a> for BufWriter {
        type Writer = BufGuard;
        fn make_writer(&'a self) -> Self::Writer {
            BufGuard(self.0.clone())
        }
    }

    struct BufGuard(Arc<Mutex<Vec<u8>>>);

    impl Write for BufGuard {
        fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(buf);
            Ok(buf.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }

    #[test]
    fn warn_event_serializes_as_signal() {
        let buf = Arc::new(Mutex::new(Vec::new()));
        let subscriber = Registry::default().with(SignalLayer::new(BufWriter(buf.clone())));

        tracing::subscriber::with_default(subscriber, || {
            tracing::warn!(percent = 0.5, "pairing percent outside [0.0, 1.0]");
        });

        let captured = String::from_utf8(buf.lock().unwrap().clone()).unwrap();
        let line = captured
            .lines()
            .next()
            .expect("one signal line was written");
        let value: serde_json::Value =
            serde_json::from_str(line).expect("signal line is valid JSON");

        assert_eq!(value["type"], "signal");
        assert_eq!(value["level"], "warn");
        assert!(
            value["target"]
                .as_str()
                .unwrap()
                .starts_with("network_engine_worker"),
            "unexpected target: {}",
            value["target"]
        );
        assert_eq!(value["message"], "pairing percent outside [0.0, 1.0]");
        assert_eq!(value["fields"]["percent"], 0.5);
        // 019: trace_id/span_id are omitted keys (not null) when absent.
        assert!(
            value.get("trace_id").is_none(),
            "trace_id should be omitted"
        );
        assert!(value.get("span_id").is_none(), "span_id should be omitted");
        assert!(
            value["timestamp"].as_str().unwrap().ends_with('Z'),
            "timestamp should be UTC Z form: {}",
            value["timestamp"]
        );
    }

    #[test]
    fn string_field_serializes_as_json_string() {
        let buf = Arc::new(Mutex::new(Vec::new()));
        let subscriber = Registry::default().with(SignalLayer::new(BufWriter(buf.clone())));

        tracing::subscriber::with_default(subscriber, || {
            tracing::warn!(boundary_rank = "gold", "rank below boundary");
        });

        let captured = String::from_utf8(buf.lock().unwrap().clone()).unwrap();
        let line = captured
            .lines()
            .next()
            .expect("one signal line was written");
        let value: serde_json::Value =
            serde_json::from_str(line).expect("signal line is valid JSON");

        // A &str field must serialize as a JSON string, not a Debug-quoted string.
        assert_eq!(value["fields"]["boundary_rank"], "gold");
    }

    #[test]
    fn trace_context_attaches_to_signal_and_clears() {
        use super::{clear_trace_context, set_trace_context};

        let buf = Arc::new(Mutex::new(Vec::new()));
        let subscriber = Registry::default().with(SignalLayer::new(BufWriter(buf.clone())));

        tracing::subscriber::with_default(subscriber, || {
            set_trace_context(Some("abc".into()), Some("def".into()));
            tracing::warn!("with trace");
            clear_trace_context();
            tracing::warn!("without trace");
        });

        let captured = String::from_utf8(buf.lock().unwrap().clone()).unwrap();
        let mut lines = captured.lines();

        let first: serde_json::Value =
            serde_json::from_str(lines.next().expect("first signal line")).expect("valid JSON");
        assert_eq!(first["trace_id"], "abc");
        assert_eq!(first["span_id"], "def");

        let second: serde_json::Value =
            serde_json::from_str(lines.next().expect("second signal line")).expect("valid JSON");
        assert!(
            second.get("trace_id").is_none(),
            "trace_id should be cleared"
        );
        assert!(second.get("span_id").is_none(), "span_id should be cleared");
    }
}
