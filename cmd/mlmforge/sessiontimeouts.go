package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/mlmforge/mlmforge/internal/platform"
)

// Env vars an operator sets to override the session timeouts.
const (
	statementTimeoutEnv = "MLMFORGE_TREE_STATEMENT_TIMEOUT"
	lockTimeoutEnv      = "MLMFORGE_TREE_LOCK_TIMEOUT"
)

// Default session timeouts, in SHOW format.
const (
	defaultStatementTimeout = "30s"
	defaultLockTimeout      = "9s"
)

// timeoutSetting names one session setting, its override env var and its default.
type timeoutSetting struct {
	name string
	env  string
	def  string
}

var sessionTimeoutSettings = []timeoutSetting{
	{name: "statement_timeout", env: statementTimeoutEnv, def: defaultStatementTimeout},
	{name: "lock_timeout", env: lockTimeoutEnv, def: defaultLockTimeout},
}

// readTimeoutsSQL reads each setting's source and its current value.
const readTimeoutsSQL = `SELECT name, source, current_setting(name) FROM pg_settings
WHERE name IN ('statement_timeout', 'lock_timeout')`

// afterConnect resolves both settings on one new connection and records the result.
func (r *timeoutRecord) afterConnect(ctx context.Context, conn *pgx.Conn) error {
	if err := r.resolve(ctx, conn); err != nil {
		return &unrecordedError{err: err}
	}
	return nil
}

// resolve reads both settings, applies the precedence rule, and records the result.
func (r *timeoutRecord) resolve(ctx context.Context, conn *pgx.Conn) error {
	rows, err := conn.Query(ctx, readTimeoutsSQL)
	if err != nil {
		return fmt.Errorf("read session timeouts on a new tree connection: %w", err)
	}
	type current struct{ source, value string }
	found := map[string]current{}
	for rows.Next() {
		var name string
		var c current
		if err := rows.Scan(&name, &c.source, &c.value); err != nil {
			rows.Close()
			return fmt.Errorf("read session timeouts on a new tree connection: %w", err)
		}
		found[name] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read session timeouts on a new tree connection: %w", err)
	}
	for _, s := range sessionTimeoutSettings {
		if _, ok := found[s.name]; !ok {
			return fmt.Errorf("pg_settings returned no row for %s on a new tree connection", s.name)
		}
	}

	var sets, envNotes []string
	var args []any
	plans := make([]timeoutPlan, len(sessionTimeoutSettings))
	for i, s := range sessionTimeoutSettings {
		envValue := os.Getenv(s.env)
		plans[i] = resolveTimeout(found[s.name].source, s.env, envValue, s.def)
		if plans[i].apply {
			args = append(args, plans[i].value)
			sets = append(sets, fmt.Sprintf("set_config('%s', $%d, false)", s.name, len(args)))
			if plans[i].source == s.env {
				envNotes = append(envNotes, fmt.Sprintf("%s=%q", s.env, envValue))
			}
		}
	}

	values := make([]string, len(sessionTimeoutSettings))
	reads := make([]string, len(sessionTimeoutSettings))
	for i, s := range sessionTimeoutSettings {
		values[i] = found[s.name].value
		reads[i] = fmt.Sprintf("current_setting('%s')", s.name)
	}
	if len(sets) > 0 {
		q := "SELECT " + strings.Join(sets, ", ") + ", " + strings.Join(reads, ", ")
		var dest []any
		for range sets {
			dest = append(dest, new(string))
		}
		for i := range values {
			dest = append(dest, &values[i])
		}
		if err := conn.QueryRow(ctx, q, args...).Scan(dest...); err != nil {
			if len(envNotes) == 0 {
				return fmt.Errorf("apply session timeouts on a new tree connection: %w", err)
			}
			return fmt.Errorf("apply session timeouts on a new tree connection, writing %s: %w", strings.Join(envNotes, ", "), err)
		}
	}
	for i, s := range sessionTimeoutSettings {
		r.add(s.name, values[i], plans[i].source)
	}
	return nil
}

// unrecordedError marks an error from a session whose settings the record does not describe.
type unrecordedError struct{ err error }

func (e *unrecordedError) Error() string { return e.err.Error() }
func (e *unrecordedError) Unwrap() error { return e.err }

// unrecordedLocker marks every error from a locker whose sessions do not get the recorded settings.
type unrecordedLocker struct{ networkengine.TreeLocker }

func (l unrecordedLocker) Lock(ctx context.Context, treeID uuid.UUID) (func() error, error) {
	unlock, err := l.TreeLocker.Lock(ctx, treeID)
	if err != nil {
		return nil, &unrecordedError{err: err}
	}
	return func() error {
		if err := unlock(); err != nil {
			return &unrecordedError{err: err}
		}
		return nil
	}, nil
}

// pgUnderUnrecorded reports whether pg sits inside an unrecordedError somewhere in err's tree.
func pgUnderUnrecorded(err error, pg *pgconn.PgError) bool {
	if u, ok := err.(*unrecordedError); ok {
		var inner *pgconn.PgError
		return errors.As(u.err, &inner) && inner == pg
	}
	switch w := err.(type) {
	case interface{ Unwrap() error }:
		if next := w.Unwrap(); next != nil {
			return pgUnderUnrecorded(next, pg)
		}
	case interface{ Unwrap() []error }:
		for _, next := range w.Unwrap() {
			if next != nil && pgUnderUnrecorded(next, pg) {
				return true
			}
		}
	}
	return false
}

// timeoutError adds a timeout's recorded value and source to a server cancellation.
type timeoutError struct {
	err  error
	text string
}

func (e *timeoutError) Error() string { return e.err.Error() + " (" + e.text + ")" }
func (e *timeoutError) Unwrap() error { return e.err }

// annotate adds the recorded value and source to a statement or lock timeout error.
func (r *timeoutRecord) annotate(err error) error {
	var pg *pgconn.PgError
	if err == nil || r == nil || !errors.As(err, &pg) {
		return err
	}
	var done *timeoutError
	if errors.As(err, &done) {
		return err
	}
	if pgUnderUnrecorded(err, pg) {
		return err
	}
	var name string
	switch {
	case pg.Code == "57014" && pg.Message == "canceling statement due to statement timeout":
		name = "statement_timeout"
	case pg.Code == "55P03" && pg.Message == "canceling statement due to lock timeout":
		name = "lock_timeout"
	default:
		return err
	}
	text, ok := r.describe(name)
	if !ok {
		return err
	}
	return &timeoutError{err: err, text: "the server canceled the statement: " + text}
}

// annotateObservation annotates the error a projected-version read returned, on a copy.
func annotateObservation(obs *networkengine.ProjectionObservation, r *timeoutRecord) *networkengine.ProjectionObservation {
	if obs == nil || obs.Err == nil {
		return obs
	}
	annotated := *obs
	annotated.Err = r.annotate(obs.Err)
	return &annotated
}

// annotateWrite annotates a write's projection error and its projected-version read error.
func annotateWrite(res networkengine.WriteResult, r *timeoutRecord) networkengine.WriteResult {
	res.ProjectionErr = r.annotate(res.ProjectionErr)
	res.Observed = annotateObservation(res.Observed, r)
	return res
}

// annotateReject annotates a rejection's projection error and projected-version read
// error, and its retry error unless err carries it.
func annotateReject(res networkengine.RejectResult, r *timeoutRecord, err error) networkengine.RejectResult {
	res.ProjectionErr = r.annotate(res.ProjectionErr)
	res.Observed = annotateObservation(res.Observed, r)
	if res.RetryErr != nil && (err == nil || !errors.Is(err, res.RetryErr)) {
		res.RetryErr = r.annotate(res.RetryErr)
	}
	return res
}

// openTreePool opens a pool whose connections resolve their session timeouts on connect.
func openTreePool(ctx context.Context, dbURL string, rec *timeoutRecord) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", platform.PgxConnStringError(err))
	}
	cfg.AfterConnect = rec.afterConnect
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", platform.PgxConnStringError(err))
	}
	return pool, nil
}

// timeoutPlan is the precedence rule's outcome for one setting.
type timeoutPlan struct {
	apply  bool
	value  string
	source string
}

// resolveTimeout applies the precedence rule to one setting.
func resolveTimeout(serverSource, envName, envValue, def string) timeoutPlan {
	switch {
	case serverSource == "client":
		return timeoutPlan{source: "the client's connection settings (URL, PGOPTIONS or a service file)"}
	case envValue != "":
		return timeoutPlan{apply: true, value: envValue, source: envName}
	case serverSource == "user", serverSource == "database", serverSource == "database user":
		return timeoutPlan{source: "the database role or database settings"}
	case serverSource == "configuration file":
		return timeoutPlan{source: "the server configuration"}
	case serverSource == "default":
		return timeoutPlan{apply: true, value: def, source: "mlmforge default"}
	}
	return timeoutPlan{source: fmt.Sprintf("the server (pg_settings source %q)", serverSource)}
}

// timeoutResult is what one or more connections resolved for one setting.
type timeoutResult struct {
	value  string
	source string
	varied bool
}

// timeoutRecord holds each setting's result across a command's connections.
type timeoutRecord struct {
	mu   sync.Mutex
	seen map[string]timeoutResult
}

// add records one connection's result for one setting.
func (r *timeoutRecord) add(name, value, source string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]timeoutResult{}
	}
	prev, ok := r.seen[name]
	switch {
	case !ok:
		r.seen[name] = timeoutResult{value: value, source: source}
	case prev.value != value || prev.source != source:
		r.seen[name] = timeoutResult{varied: true}
	}
}

// describe states the setting's recorded value and source, or that it varied.
func (r *timeoutRecord) describe(name string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.seen[name]
	if !ok {
		return "", false
	}
	if res.varied {
		return name + " varied across this command's connections", true
	}
	return fmt.Sprintf("%s is %s (set by: %s)", name, res.value, res.source), true
}
