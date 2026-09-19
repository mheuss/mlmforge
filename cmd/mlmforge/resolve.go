package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// workerPathEnv is the environment variable naming the compiled Rust worker.
const workerPathEnv = "MLMFORGE_WORKER_BIN"

// resolveDBURL returns the database URL from the flag or DATABASE_URL.
func resolveDBURL(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if env := os.Getenv("DATABASE_URL"); env != "" {
		return env, nil
	}
	return "", errors.New("--db-url flag or DATABASE_URL env var is required")
}

// resolveWorkerPath returns an absolute path to the worker binary.
func resolveWorkerPath(flagValue string) (string, error) {
	path, source := flagValue, "--worker"
	if path == "" {
		path, source = os.Getenv(workerPathEnv), workerPathEnv
	}
	if path == "" {
		return "", fmt.Errorf("--worker flag or %s env var is required", workerPathEnv)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve worker path %q read from %s: %w", path, source, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("worker binary not found at %s, read from %s: %w", abs, source, err)
	}
	return abs, nil
}
