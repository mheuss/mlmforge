package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
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

// runTimedWriteCmd executes one write subcommand against w with a timeouts record on the deps.
func runTimedWriteCmd(t *testing.T, w treeWriter, args ...string) (*cmdOutput, error) {
	t.Helper()
	rec := timeoutRecordWith(t)
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{timeouts: rec, release: func() error { return nil }}, nil
		},
		nil,
		func(*treeDeps) treeWriter { return w },
	)
	out := &cmdOutput{}
	cmd.SetOut(&out.stdout)
	cmd.SetErr(&out.stderr)
	cmd.SetArgs(append(args, "--db-url", "postgres://x", "--worker", workerStub(t)))
	return out, cmd.ExecuteContext(context.Background())
}

func TestWriteCommands_AnnotateATimedOutProjection(t *testing.T) {
	commands := map[string][]string{
		"add-root": {"add-root", "--tree-id", "t", "--user-id", "u", "--sponsor-id", "u", "--tree-type", "unilevel"},
		"place":    {"place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p"},
		"remove":   {"remove", "--tree-id", "t", "--user-id", "u"},
	}
	for name, args := range commands {
		t.Run(name, func(t *testing.T) {
			w := &recordingWriter{result: networkengine.WriteResult{EventID: "e", Version: 2, Stream: "s",
				ProjectionErr: fmt.Errorf("store placed node: %w", statementCancel)}}

			out, _ := runTimedWriteCmd(t, w, args...)

			assert.Contains(t, out.stderr.String(), "statement_timeout is 30s (set by: mlmforge default)")
		})
	}
}

func TestWriteCommands_AnnotateATimedOutProjectedVersionRead(t *testing.T) {
	w := &recordingWriter{result: networkengine.WriteResult{EventID: "e", Version: 2, Stream: "s",
		ProjectionErr: errors.New("projection failed"),
		Observed:      &networkengine.ProjectionObservation{Err: fmt.Errorf("read: %w", statementCancel)}}}

	out, _ := runTimedWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	assert.Contains(t, out.stderr.String(), "could not be read")
	assert.Contains(t, out.stderr.String(), "statement_timeout is 30s (set by: mlmforge default)")
}

func TestRejectCommand_AnnotatesATimedOutProjection(t *testing.T) {
	cases := map[string]networkengine.RejectResult{
		"projection error": {EventID: "r", Version: 3, Stream: "s", RejectedEventID: "e", RejectedVersion: 2,
			Outcome:       networkengine.RejectOutcomeRejected,
			ProjectionErr: fmt.Errorf("project: %w", statementCancel),
			Observed:      &networkengine.ProjectionObservation{Version: 2, Found: true}},
		"projected-version read": {EventID: "r", Version: 3, Stream: "s", RejectedEventID: "e", RejectedVersion: 2,
			Outcome:       networkengine.RejectOutcomeRejected,
			ProjectionErr: errors.New("projection failed"),
			Observed:      &networkengine.ProjectionObservation{Err: fmt.Errorf("read: %w", statementCancel)}},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			w := &recordingWriter{rejectResult: res}

			out, _ := runTimedWriteCmd(t, w, "reject-event", "--tree-id", "t", "--event-id", "e", "--reason", "r")

			assert.Contains(t, out.stderr.String(), "statement_timeout is 30s (set by: mlmforge default)")
		})
	}
}

func TestRejectCommand_AnnotatesAPrintedRetryError(t *testing.T) {
	w := &recordingWriter{
		rejectResult: networkengine.RejectResult{RejectedEventID: "e", RejectedVersion: 2,
			RetryErr: fmt.Errorf("retry: %w", statementCancel)},
		err: errors.New("the rejection was refused"),
	}

	out, _ := runTimedWriteCmd(t, w, "reject-event", "--tree-id", "t", "--event-id", "e", "--reason", "r")

	assert.Contains(t, out.stderr.String(), "statement_timeout is 30s (set by: mlmforge default)")
}

func TestWriteCommands_AnnotateATimedOutReturnedError(t *testing.T) {
	w := &recordingWriter{err: fmt.Errorf("append: %w", statementCancel)}

	_, err := runTimedWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "statement_timeout is 30s (set by: mlmforge default)")
	assert.Equal(t, 1, exitCode(err))
}

func TestAnnotate_DoesNotAnnotateTwice(t *testing.T) {
	r := timeoutRecordWith(t)
	once := r.annotate(statementCancel)

	twice := r.annotate(fmt.Errorf("outer: %w", once))

	assert.Equal(t, 1, strings.Count(twice.Error(), "the server canceled the statement"))
}

func TestAnnotate_LeavesAnUnrecordedSessionsTimeoutAlone(t *testing.T) {
	for name, tc := range channelCases {
		t.Run(name, func(t *testing.T) {
			err := fmt.Errorf("lock tree: %w", &unrecordedError{err: fmt.Errorf("query: %w", tc.pg)})

			got := timeoutRecordWith(t).annotate(err)

			assert.Same(t, err, got)
			assert.NotContains(t, got.Error(), "the server canceled the statement")
		})
	}
}

func TestAnnotate_AnUnrecordedSiblingDoesNotHideAPoolTimeout(t *testing.T) {
	lockCancel := &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}
	err := &networkengine.AppendOutcomeUnknownError{
		AppendErr: fmt.Errorf("append: %w", statementCancel),
		ReadErr:   &unrecordedError{err: fmt.Errorf("read: %w", lockCancel)},
	}

	got := timeoutRecordWith(t).annotate(err)

	assert.Contains(t, got.Error(), "(the server canceled the statement: statement_timeout is 30s (set by: mlmforge default))")
	assert.NotContains(t, got.Error(), "lock_timeout is")
}

func TestAnnotate_AMarkedBranchOfAJoinedErrorIsLeftAlone(t *testing.T) {
	lockCancel := &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}
	cases := map[string]*networkengine.AppendOutcomeUnknownError{
		"marked append": {
			AppendErr: &unrecordedError{err: fmt.Errorf("append: %w", lockCancel)},
			ReadErr:   errors.New("read: connection refused"),
		},
		"marked read, nil append": {
			ReadErr: &unrecordedError{err: fmt.Errorf("read: %w", lockCancel)},
		},
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			got := timeoutRecordWith(t).annotate(err)

			assert.Same(t, error(err), got)
		})
	}
}

// fakeLocker returns its fields from Lock and from the unlock it hands back.
type fakeLocker struct{ lockErr, unlockErr error }

func (f fakeLocker) Lock(context.Context, uuid.UUID) (func() error, error) {
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	return func() error { return f.unlockErr }, nil
}

func TestUnrecordedLocker_MarksLockAndUnlockErrors(t *testing.T) {
	var unrecorded *unrecordedError

	_, err := unrecordedLocker{fakeLocker{lockErr: statementCancel}}.Lock(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorAs(t, err, &unrecorded)
	assert.ErrorIs(t, err, statementCancel)

	unlock, err := unrecordedLocker{fakeLocker{unlockErr: statementCancel}}.Lock(context.Background(), uuid.New())
	require.NoError(t, err)
	err = unlock()
	require.Error(t, err)
	assert.ErrorAs(t, err, &unrecorded)

	unlock, err = unrecordedLocker{fakeLocker{}}.Lock(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.NoError(t, unlock())
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
	assert.Equal(t, "20s", defaultLockTimeout)
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
			timeoutPlan{apply: true, value: "11s", source: "mlmforge default"}},
		{"unknown source kept and named", "override", "",
			timeoutPlan{source: `the server (pg_settings source "override")`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveTimeout(tc.source, env, tc.envValue, "11s"))
		})
	}
}
