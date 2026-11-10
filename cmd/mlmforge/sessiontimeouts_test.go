package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func timeoutRecordWith(t *testing.T) *timeoutRecord {
	t.Helper()
	r := &timeoutRecord{}
	r.add("statement_timeout", "30s", "mlmforge default")
	r.add("lock_timeout", "5s", lockTimeoutEnv)
	return r
}

var statementCancel = &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}

// channelCases is one synthetic cancellation per timeout, with the text its annotation carries.
var channelCases = map[string]struct {
	pg   *pgconn.PgError
	want string
}{
	"statement_timeout": {statementCancel, "statement_timeout is 30s (set by: mlmforge default)"},
	"lock_timeout": {&pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"},
		"lock_timeout is 5s (set by: " + lockTimeoutEnv + ")"},
}

func TestAnnotate_AppendsTheMatchingTimeout(t *testing.T) {
	for name, tc := range channelCases {
		t.Run(name, func(t *testing.T) {
			base := fmt.Errorf("store placed node: %w", tc.pg)

			got := timeoutRecordWith(t).annotate(base)

			assert.Contains(t, got.Error(), "the server canceled the statement: "+tc.want)
			assert.ErrorIs(t, got, base)
		})
	}
}

func TestAnnotate_LeavesEverythingElseAlone(t *testing.T) {
	r := timeoutRecordWith(t)
	cases := map[string]error{
		"nil":           nil,
		"not a PgError": errors.New("plain"),
		"user cancel":   &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"},
		"NOWAIT":        &pgconn.PgError{Code: "55P03", Message: `could not obtain lock on row in relation "tree_projections"`},
		"another locale": &pgconn.PgError{Code: "57014",
			Message: "Anweisung wird abgebrochen wegen Zeitüberschreitung der Anweisung"},
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, err, r.annotate(err))
		})
	}
	var nilRecord *timeoutRecord
	assert.Equal(t, error(statementCancel), nilRecord.annotate(statementCancel), "a nil record annotated an error")
}

func TestAnnotate_AVariedSettingNamesNoSource(t *testing.T) {
	r := &timeoutRecord{}
	r.add("statement_timeout", "30s", "mlmforge default")
	r.add("statement_timeout", "9s", statementTimeoutEnv)

	got := r.annotate(statementCancel)

	assert.Contains(t, got.Error(), "statement_timeout varied across this command's connections")
	assert.NotContains(t, got.Error(), "set by")
}

func TestAnnotateWrite_AnnotatesTheProjectionError(t *testing.T) {
	for name, tc := range channelCases {
		t.Run(name, func(t *testing.T) {
			res := networkengine.WriteResult{EventID: "e", Version: 2, Stream: "s",
				ProjectionErr: fmt.Errorf("store placed node: %w", tc.pg)}
			var out, warn bytes.Buffer

			_ = reportWrite(context.Background(), &out, &warn, annotateWrite(res, timeoutRecordWith(t)), nil)

			assert.Contains(t, warn.String(), tc.want)
		})
	}
}

func TestAnnotateReject_AnnotatesAPrintedRetryError(t *testing.T) {
	for name, tc := range channelCases {
		t.Run(name, func(t *testing.T) {
			res := networkengine.RejectResult{RejectedEventID: "e", RejectedVersion: 2,
				RetryErr: fmt.Errorf("retry: %w", tc.pg), Outcome: networkengine.RejectOutcomeRejected}
			var out, warn bytes.Buffer

			_ = reportReject(context.Background(), &out, &warn, annotateReject(res, timeoutRecordWith(t), nil), nil)

			assert.Contains(t, warn.String(), tc.want)
		})
	}
}

func TestAnnotateReject_DoesNotPrintARetryErrorTheReturnedErrorCarries(t *testing.T) {
	retry := fmt.Errorf("retry: %w", statementCancel)
	res := networkengine.RejectResult{RejectedEventID: "e", RejectedVersion: 2, RetryErr: retry}
	returned := fmt.Errorf("reject: %w", retry)
	var out, warn bytes.Buffer

	_ = reportReject(context.Background(), &out, &warn, annotateReject(res, timeoutRecordWith(t), returned), returned)

	assert.NotContains(t, warn.String(), "retrying event", "the retry error was printed beside a returned error that carries it")
}

func TestWithTreeDeps_AnnotatesTheReturnedError(t *testing.T) {
	for name, tc := range channelCases {
		t.Run(name, func(t *testing.T) {
			r := timeoutRecordWith(t)
			open := func(context.Context, string, string) (*treeDeps, error) {
				return &treeDeps{timeouts: r, release: func() error { return nil }}, nil
			}

			err := withTreeDeps(context.Background(), io.Discard, open, "", "", func(context.Context, *treeDeps) error {
				return fmt.Errorf("read rows: %w", tc.pg)
			})

			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestTimeoutRecord_DescribesTheFirstResult(t *testing.T) {
	r := &timeoutRecord{}
	r.add("statement_timeout", "30s", "mlmforge default")

	got, ok := r.describe("statement_timeout")

	require.True(t, ok)
	assert.Equal(t, "statement_timeout is 30s (set by: mlmforge default)", got)
}

func TestTimeoutRecord_ASameResultChangesNothing(t *testing.T) {
	r := &timeoutRecord{}
	r.add("statement_timeout", "30s", "mlmforge default")
	r.add("statement_timeout", "30s", "mlmforge default")

	got, ok := r.describe("statement_timeout")

	require.True(t, ok)
	assert.Equal(t, "statement_timeout is 30s (set by: mlmforge default)", got)
}

func TestTimeoutRecord_AVariedSettingStaysVaried(t *testing.T) {
	r := &timeoutRecord{}
	r.add("statement_timeout", "30s", "mlmforge default")
	r.add("statement_timeout", "10s", "mlmforge default")
	r.add("statement_timeout", "30s", "mlmforge default")

	got, ok := r.describe("statement_timeout")

	require.True(t, ok)
	assert.Equal(t, "statement_timeout varied across this command's connections", got)
}

func TestTimeoutRecord_ADifferentResultMarksItVaried(t *testing.T) {
	for name, second := range map[string][2]string{
		"value":  {"10s", "mlmforge default"},
		"source": {"30s", "MLMFORGE_TREE_STATEMENT_TIMEOUT"},
	} {
		t.Run(name, func(t *testing.T) {
			r := &timeoutRecord{}
			r.add("statement_timeout", "30s", "mlmforge default")
			r.add("statement_timeout", second[0], second[1])

			got, ok := r.describe("statement_timeout")

			require.True(t, ok)
			assert.Equal(t, "statement_timeout varied across this command's connections", got)
		})
	}
}

func TestTimeoutRecord_ConcurrentWritersAreSafe(t *testing.T) {
	r := &timeoutRecord{}
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.add("lock_timeout", fmt.Sprintf("%ds", i%2+5), "mlmforge default")
		}()
		go func() {
			defer wg.Done()
			_, _ = r.describe("lock_timeout")
		}()
	}
	wg.Wait()

	got, ok := r.describe("lock_timeout")

	require.True(t, ok)
	assert.Equal(t, "lock_timeout varied across this command's connections", got)
}

func TestTimeoutRecord_NilAndUnknownDescribeNothing(t *testing.T) {
	var r *timeoutRecord
	r.add("statement_timeout", "30s", "mlmforge default")
	_, ok := r.describe("statement_timeout")
	assert.False(t, ok, "a nil record described a setting")
	_, ok = (&timeoutRecord{}).describe("statement_timeout")
	assert.False(t, ok, "an empty record described a setting")
}

func TestSessionTimeoutDefaults_ArePinned(t *testing.T) {
	assert.Equal(t, "30s", defaultStatementTimeout)
	assert.Equal(t, "9s", defaultLockTimeout)
}

func TestResolveTimeout_FollowsThePrecedenceRule(t *testing.T) {
	const env = "MLMFORGE_TREE_STATEMENT_TIMEOUT"
	cases := []struct {
		name, source, envValue string
		want                   timeoutPlan
	}{
		{"client wins over the env var", "client", "9s",
			timeoutPlan{source: "the client's connection settings (URL, PGOPTIONS or a service file)"}},
		{"env var beats a role", "user", "9s", timeoutPlan{apply: true, value: "9s", source: env}},
		{"env var beats the default", "default", "9s", timeoutPlan{apply: true, value: "9s", source: env}},
		{"env var beats the server configuration", "configuration file", "9s",
			timeoutPlan{apply: true, value: "9s", source: env}},
		{"env var beats an unknown source", "override", "9s", timeoutPlan{apply: true, value: "9s", source: env}},
		{"env var zero is applied", "default", "0", timeoutPlan{apply: true, value: "0", source: env}},
		{"role kept without the env var", "user", "",
			timeoutPlan{source: "the database role or database settings"}},
		{"database kept", "database", "", timeoutPlan{source: "the database role or database settings"}},
		{"database user kept", "database user", "", timeoutPlan{source: "the database role or database settings"}},
		{"configuration file kept", "configuration file", "", timeoutPlan{source: "the server configuration"}},
		{"default gets the mlmforge default", "default", "",
			timeoutPlan{apply: true, value: "30s", source: "mlmforge default"}},
		{"unknown source kept and named", "override", "",
			timeoutPlan{source: `the server (pg_settings source "override")`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveTimeout(tc.source, env, tc.envValue, "30s"))
		})
	}
}
