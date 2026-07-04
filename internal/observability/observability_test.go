package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// restoreProviders resets the global providers to no-ops after the test, so
// Init's mutation of global state does not leak between tests. Tests that call
// it must not use t.Parallel().
func restoreProviders(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		global.SetLoggerProvider(lognoop.NewLoggerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
	})
}

// assertProvidersWired checks D2: after Init all three global providers are the
// SDK providers (not the built-in no-ops), and exercising the dormant metric and
// trace providers neither panics nor errors.
func assertProvidersWired(t *testing.T) {
	t.Helper()

	assert.IsType(t, &sdklog.LoggerProvider{}, global.GetLoggerProvider(),
		"global logger provider should be the SDK provider")
	assert.IsType(t, &sdkmetric.MeterProvider{}, otel.GetMeterProvider(),
		"global meter provider should be the SDK provider")
	assert.IsType(t, &sdktrace.TracerProvider{}, otel.GetTracerProvider(),
		"global tracer provider should be the SDK provider")

	// Dormant providers: creating a span/counter is a no-op, not a panic.
	_, span := otel.Tracer("test").Start(context.Background(), "dormant")
	span.End()
	counter, err := otel.Meter("test").Int64Counter("dormant")
	require.NoError(t, err)
	counter.Add(context.Background(), 1)
}

// captureExporter is an in-memory sdklog.Exporter that records everything the
// SimpleProcessor emits, so tests can inspect the resulting log records.
type captureExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *captureExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.records = append(e.records, records...)
	return nil
}

func (e *captureExporter) Shutdown(context.Context) error   { return nil }
func (e *captureExporter) ForceFlush(context.Context) error { return nil }

func collectAttrs(rec sdklog.Record) map[string]otellog.Value {
	attrs := make(map[string]otellog.Value, rec.AttributesLen())
	rec.WalkAttributes(func(kv otellog.KeyValue) bool {
		attrs[kv.Key] = kv.Value
		return true
	})
	return attrs
}

func TestObserver_HandleSignal(t *testing.T) {
	exporter := &captureExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	observer := NewObserver(lp)

	raw := json.RawMessage(`{"type":"signal","level":"warn","target":"network_engine::commission::binary","message":"pairing percent outside [0.0, 1.0]","fields":{"percent":1.5},"trace_id":"abc","span_id":"def","timestamp":"2026-07-03T00:00:00Z"}`)
	observer.HandleSignal(raw)

	require.NoError(t, lp.ForceFlush(context.Background()))
	require.Len(t, exporter.records, 1, "one signal should map to one record")
	rec := exporter.records[0]

	assert.Equal(t, otellog.SeverityWarn, rec.Severity())
	assert.Equal(t, "pairing percent outside [0.0, 1.0]", rec.Body().AsString())

	attrs := collectAttrs(rec)
	require.Contains(t, attrs, "target")
	assert.Equal(t, "network_engine::commission::binary", attrs["target"].AsString())
	require.Contains(t, attrs, "percent")
	assert.Equal(t, 1.5, attrs["percent"].AsFloat64())
	require.Contains(t, attrs, "trace_id")
	assert.Equal(t, "abc", attrs["trace_id"].AsString())
	require.Contains(t, attrs, "span_id")
	assert.Equal(t, "def", attrs["span_id"].AsString())
}

// TestObserver_HandleSignal_FieldTypes covers the deterministic value mapping:
// string/number/bool as typed attributes, and null/object/array compacted to a
// JSON string (mirrors the Rust visitor).
func TestObserver_HandleSignal_FieldTypes(t *testing.T) {
	exporter := &captureExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	observer := NewObserver(lp)
	observer.HandleSignal(json.RawMessage(`{"type":"signal","level":"info","message":"m","fields":{"s":"txt","n":42,"b":true,"nothing":null,"obj":{"k":1},"arr":[1,2]}}`))

	require.NoError(t, lp.ForceFlush(context.Background()))
	require.Len(t, exporter.records, 1)
	attrs := collectAttrs(exporter.records[0])

	assert.Equal(t, "txt", attrs["s"].AsString())
	assert.Equal(t, float64(42), attrs["n"].AsFloat64())
	assert.Equal(t, true, attrs["b"].AsBool())
	assert.Equal(t, "null", attrs["nothing"].AsString())
	assert.Equal(t, `{"k":1}`, attrs["obj"].AsString())
	assert.Equal(t, `[1,2]`, attrs["arr"].AsString())
}

// TestObserver_HandleSignal_NativeTraceCorrelation: valid hex IDs land in the
// record's first-class TraceID/SpanID (via the emit context), not as attributes.
func TestObserver_HandleSignal_NativeTraceCorrelation(t *testing.T) {
	exporter := &captureExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	observer := NewObserver(lp)
	const traceID = "0123456789abcdef0123456789abcdef"
	const spanID = "0123456789abcdef"
	observer.HandleSignal(json.RawMessage(
		`{"type":"signal","level":"warn","message":"m","trace_id":"` + traceID + `","span_id":"` + spanID + `"}`))

	require.NoError(t, lp.ForceFlush(context.Background()))
	require.Len(t, exporter.records, 1)
	rec := exporter.records[0]

	assert.Equal(t, traceID, rec.TraceID().String(), "trace id should be stamped natively")
	assert.Equal(t, spanID, rec.SpanID().String(), "span id should be stamped natively")

	attrs := collectAttrs(rec)
	assert.NotContains(t, attrs, "trace_id", "valid ids must not duplicate into attributes")
	assert.NotContains(t, attrs, "span_id")
}

// TestObserver_HandleSignal_Malformed: fire-and-forget drops bad input without
// panicking and emits nothing.
func TestObserver_HandleSignal_Malformed(t *testing.T) {
	exporter := &captureExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	observer := NewObserver(lp)
	observer.HandleSignal(json.RawMessage(`{"type":"signal",`)) // truncated

	require.NoError(t, lp.ForceFlush(context.Background()))
	assert.Empty(t, exporter.records, "malformed signal should emit no record")
}

func TestInit_LogsDisabled(t *testing.T) {
	restoreProviders(t)
	t.Setenv("OTEL_LOGS_EXPORTER", "") // unset -> logger provider drops records

	shutdown, err := Init(context.Background())
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	defer func() { require.NoError(t, shutdown(context.Background())) }()

	assertProvidersWired(t)

	// Emitting through the global logger must not panic even with no processor.
	var rec otellog.Record
	rec.SetBody(otellog.StringValue("dropped"))
	rec.SetSeverity(otellog.SeverityInfo)
	global.Logger("test").Emit(context.Background(), rec)
}

func TestInit_LogsToFile(t *testing.T) {
	restoreProviders(t)

	// Nested path exercises the MkdirAll of the parent directory.
	path := filepath.Join(t.TempDir(), "nested", "mlmforge.otel.log")
	t.Setenv("OTEL_LOGS_EXPORTER", "file")
	t.Setenv("OTEL_LOGS_FILE", path)

	shutdown, err := Init(context.Background())
	require.NoError(t, err)

	assertProvidersWired(t)

	var rec otellog.Record
	rec.SetBody(otellog.StringValue("hello from a record"))
	rec.SetSeverity(otellog.SeverityWarn)
	global.Logger("test").Emit(context.Background(), rec)

	// Shutdown flushes the simple processor and closes the file.
	require.NoError(t, shutdown(context.Background()))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotEmpty(t, data, "log file should contain the flushed record")
}
