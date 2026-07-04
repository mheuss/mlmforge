// Package observability wires the OpenTelemetry pipeline for MLMForge and
// forwards NDJSON signals emitted by the Rust engine into it.
//
// The design (design-rationale 019, D1/D2) registers all three signal providers
// — logs, metrics, traces — globally at startup, but only the logs pipeline has
// an exporter today. Metrics and traces are wired but dormant: attaching a real
// exporter later is a one-line change, not a second wiring pass, and no call
// site changes.
package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// defaultLogFile is used when OTEL_LOGS_EXPORTER=file but OTEL_LOGS_FILE is unset.
const defaultLogFile = "mlmforge.otel.log"

// Init constructs the OpenTelemetry pipeline from the environment and registers
// all three signal providers globally. The logs pipeline is active when
// OTEL_LOGS_EXPORTER=file (records written to OTEL_LOGS_FILE, default
// mlmforge.otel.log); any other value leaves the logger provider with no
// processor, so records are dropped. Metrics and traces are always wired but
// dormant (no exporter) so they add negligible overhead until one is configured.
//
// The returned shutdown flushes and releases every provider and closes the log
// file; call it once at process exit.
func Init(_ context.Context) (shutdown func(context.Context) error, err error) {
	logFile, logProcessor, err := newLogProcessor()
	if err != nil {
		return nil, err
	}

	var lpOpts []sdklog.LoggerProviderOption
	if logProcessor != nil {
		lpOpts = append(lpOpts, sdklog.WithProcessor(logProcessor))
	}
	loggerProvider := sdklog.NewLoggerProvider(lpOpts...)
	global.SetLoggerProvider(loggerProvider)

	// Dormant: no reader/exporter attached, so these produce no output but keep
	// the global providers real (not no-ops), ready for a future exporter (D2).
	meterProvider := sdkmetric.NewMeterProvider()
	otel.SetMeterProvider(meterProvider)

	tracerProvider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tracerProvider)

	shutdown = func(ctx context.Context) error {
		errs := []error{
			loggerProvider.Shutdown(ctx),
			meterProvider.Shutdown(ctx),
			tracerProvider.Shutdown(ctx),
		}
		if logFile != nil {
			errs = append(errs, logFile.Close())
		}
		return errors.Join(errs...)
	}
	return shutdown, nil
}

// signalLoggerName is the instrumentation scope for records bridged from the
// engine. Signals originate in the Rust network engine, so the scope points
// there rather than at this package.
const signalLoggerName = "github.com/mlmforge/mlmforge/internal/networkengine"

// signal is the wire shape of an NDJSON signal frame from the worker. It is
// defined here (not in networkengine) so networkengine stays OTel-agnostic. The
// fields are decoded raw so each value's JSON type drives its attribute type.
type signal struct {
	Level     string                     `json:"level"`
	Target    string                     `json:"target"`
	Message   string                     `json:"message"`
	Fields    map[string]json.RawMessage `json:"fields"`
	TraceID   string                     `json:"trace_id"`
	SpanID    string                     `json:"span_id"`
	Timestamp string                     `json:"timestamp"`
}

// Observer forwards NDJSON signal frames from the engine into the OTel logs
// pipeline as log records.
type Observer struct {
	logger otellog.Logger
}

// NewObserver returns an Observer that emits through lp. When lp is nil it falls
// back to the global logger provider, so after Init a plain NewObserver(nil)
// writes to the configured pipeline.
func NewObserver(lp otellog.LoggerProvider) *Observer {
	if lp == nil {
		lp = global.GetLoggerProvider()
	}
	return &Observer{logger: lp.Logger(signalLoggerName)}
}

// HandleSignal parses one signal frame and emits it as a log record: level maps
// to severity, message to the body, timestamp to the record time, and target /
// each field / trace context become attributes. It is fire-and-forget — a
// malformed frame is dropped, not reported — so it can be passed directly to
// networkengine.WithSignalHandler.
func (o *Observer) HandleSignal(raw json.RawMessage) {
	var sig signal
	if err := json.Unmarshal(raw, &sig); err != nil {
		return
	}

	var rec otellog.Record
	rec.SetSeverity(severityForLevel(sig.Level))
	if sig.Level != "" {
		rec.SetSeverityText(sig.Level)
	}
	rec.SetBody(otellog.StringValue(sig.Message))
	if ts, err := time.Parse(time.RFC3339Nano, sig.Timestamp); err == nil {
		rec.SetTimestamp(ts)
	}

	attrs := make([]otellog.KeyValue, 0, len(sig.Fields)+3)
	if sig.Target != "" {
		attrs = append(attrs, otellog.String("target", sig.Target))
	}
	// Sort field keys so attribute order is deterministic across runs.
	keys := make([]string, 0, len(sig.Fields))
	for k := range sig.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		attrs = append(attrs, fieldToKeyValue(k, sig.Fields[k]))
	}
	if sig.TraceID != "" {
		attrs = append(attrs, otellog.String("trace_id", sig.TraceID))
	}
	if sig.SpanID != "" {
		attrs = append(attrs, otellog.String("span_id", sig.SpanID))
	}
	rec.AddAttributes(attrs...)

	o.logger.Emit(context.Background(), rec)
}

// severityForLevel maps a tracing level string to an OTel severity. Unknown
// levels map to Undefined rather than guessing.
func severityForLevel(level string) otellog.Severity {
	switch strings.ToLower(level) {
	case "trace":
		return otellog.SeverityTrace
	case "debug":
		return otellog.SeverityDebug
	case "info":
		return otellog.SeverityInfo
	case "warn", "warning":
		return otellog.SeverityWarn
	case "error":
		return otellog.SeverityError
	default:
		return otellog.SeverityUndefined
	}
}

// fieldToKeyValue maps a raw JSON field value to a typed log attribute:
// string -> String, number -> Float64, bool -> Bool, and null/object/array ->
// a compact JSON string. This mirrors the Rust visitor's primitive-or-debug
// handling so both sides render fields the same way.
func fieldToKeyValue(key string, raw json.RawMessage) otellog.KeyValue {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return otellog.String(key, "")
	}

	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return otellog.String(key, s)
		}
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(trimmed, &b); err == nil {
			return otellog.Bool(key, b)
		}
	case '{', '[', 'n':
		// object / array / null fall through to the compact-JSON string below.
	default:
		var f float64
		if err := json.Unmarshal(trimmed, &f); err == nil {
			return otellog.Float64(key, f)
		}
	}

	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err == nil {
		return otellog.String(key, buf.String())
	}
	return otellog.String(key, string(trimmed))
}

// newLogProcessor builds the log processor from the environment. It returns a
// nil processor (records dropped) when logs are not enabled, and the open file
// handle so the caller can close it on shutdown.
func newLogProcessor() (*os.File, sdklog.Processor, error) {
	if os.Getenv("OTEL_LOGS_EXPORTER") != "file" {
		return nil, nil, nil
	}

	path := os.Getenv("OTEL_LOGS_FILE")
	if path == "" {
		path = defaultLogFile
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("create otel log dir %q: %w", dir, err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open otel log file %q: %w", path, err)
	}

	exporter, err := stdoutlog.New(stdoutlog.WithWriter(f))
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("create stdoutlog exporter: %w", err)
	}
	return f, sdklog.NewSimpleProcessor(exporter), nil
}
