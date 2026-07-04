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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
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
