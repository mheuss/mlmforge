package observability

import (
	"context"
	"os"
	"path/filepath"
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
