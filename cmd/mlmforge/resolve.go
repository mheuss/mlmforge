package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mlmforge/mlmforge/internal/platform"
)

// workerPathEnv is the environment variable naming the compiled Rust worker.
const workerPathEnv = "MLMFORGE_WORKER_BIN"

// defaultConnectTimeout is the connect_timeout, in seconds, added to a URL that sets none.
var defaultConnectTimeout = 10

// timeoutSource says which connect timeout applies to a database URL.
type timeoutSource int

const (
	timeoutUnknown timeoutSource = iota
	timeoutAdded
	timeoutFromURL
	timeoutFromEnv
	timeoutFromService
	timeoutFromEnvOrService
	timeoutNone
)

// String names the timeout's source for the operator.
func (s timeoutSource) String() string {
	switch s {
	case timeoutAdded:
		return fmt.Sprintf("connect_timeout=%d, added by mlmforge because the URL set none", defaultConnectTimeout)
	case timeoutFromURL:
		return "connect_timeout from the URL"
	case timeoutFromEnv:
		return "PGCONNECT_TIMEOUT"
	case timeoutFromService:
		return "no connect_timeout added; a service is named"
	case timeoutFromEnvOrService:
		return "PGCONNECT_TIMEOUT or the named service"
	case timeoutNone:
		return "no connect timeout applies"
	}
	return "no connect_timeout added; the URL was not changed"
}

// dbTarget is a resolved database URL and the source of its connect timeout.
type dbTarget struct {
	url     string
	timeout timeoutSource
}

// resolveDBURL returns the database URL from the flag or DATABASE_URL, with a
// connect timeout added when nothing else sets one.
func resolveDBURL(flagValue string) (dbTarget, error) {
	raw := flagValue
	if raw == "" {
		raw = os.Getenv("DATABASE_URL")
	}
	if raw == "" {
		return dbTarget{}, errors.New("--db-url flag or DATABASE_URL env var is required")
	}
	return withConnectTimeout(raw), nil
}

// withConnectTimeout adds the default connect_timeout to raw when nothing else sets one.
func withConnectTimeout(raw string) dbTarget {
	unchanged := func(s timeoutSource) dbTarget { return dbTarget{url: raw, timeout: s} }
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		return unchanged(timeoutUnknown)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return unchanged(timeoutUnknown)
	}
	q := u.Query()
	// Cases run in precedence order. Moving one changes which source is reported.
	switch {
	case q.Get("connect_timeout") != "":
		return unchanged(timeoutFromURL)
	case os.Getenv("PGCONNECT_TIMEOUT") != "" && (q.Has("service") || os.Getenv("PGSERVICE") != ""):
		return unchanged(timeoutFromEnvOrService)
	case os.Getenv("PGCONNECT_TIMEOUT") != "":
		return unchanged(timeoutFromEnv)
	case q.Has("connect_timeout"):
		return unchanged(timeoutNone)
	case q.Has("service") || os.Getenv("PGSERVICE") != "":
		return unchanged(timeoutFromService)
	}
	param := "connect_timeout=" + strconv.Itoa(defaultConnectTimeout)
	return dbTarget{url: appendQueryParam(raw, param), timeout: timeoutAdded}
}

// appendQueryParam adds param to the query of raw without re-encoding anything else.
func appendQueryParam(raw, param string) string {
	head, fragment, hasFragment := strings.Cut(raw, "#")
	_, query, hasQuery := strings.Cut(head, "?")
	switch {
	case !hasQuery:
		head += "?" + param
	case query == "" || strings.HasSuffix(query, "&"):
		head += param
	default:
		head += "&" + param
	}
	if hasFragment {
		head += "#" + fragment
	}
	return head
}

// connectError names the timeout's source right after a ConnectTimeoutError's text, keeping the rest of the message.
func connectError(err error, target dbTarget) error {
	var cte *platform.ConnectTimeoutError
	if !errors.As(err, &cte) {
		return err
	}
	source := " (" + target.timeout.String() + ")"
	text, timeout := err.Error(), cte.Error()
	i := strings.Index(text, timeout)
	if i < 0 {
		return &operatorError{text: text + source, err: err}
	}
	end := i + len(timeout)
	return &operatorError{text: text[:end] + source + text[end:], err: err}
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
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("worker binary not found at %s, read from %s: %w", abs, source, err)
	}
	// Checked here so the failure names the path and the source.
	if info.IsDir() {
		return "", fmt.Errorf("worker path %s, read from %s, is a directory", abs, source)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("worker binary at %s, read from %s, is not executable (mode %s)", abs, source, info.Mode().Perm())
	}
	return abs, nil
}
