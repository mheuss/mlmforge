package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingWriter captures the request each write command sends.
type recordingWriter struct {
	addRoot networkengine.AddRootRequest
	place   networkengine.PlaceRequest
	remove  networkengine.RemoveRequest
	result  networkengine.WriteResult
	err     error
}

func (w *recordingWriter) AddRoot(_ context.Context, r networkengine.AddRootRequest) (networkengine.WriteResult, error) {
	w.addRoot = r
	return w.result, w.err
}

func (w *recordingWriter) Place(_ context.Context, r networkengine.PlaceRequest) (networkengine.WriteResult, error) {
	w.place = r
	return w.result, w.err
}

func (w *recordingWriter) Remove(_ context.Context, r networkengine.RemoveRequest) (networkengine.WriteResult, error) {
	w.remove = r
	return w.result, w.err
}

// runWriteCmd executes one write subcommand against w.
func runWriteCmd(t *testing.T, w treeWriter, args ...string) (*cmdOutput, error) {
	t.Helper()
	return runWriteCmdContext(t, context.Background(), w, args...)
}

// runWriteCmdContext executes one write subcommand against w under ctx.
func runWriteCmdContext(t *testing.T, ctx context.Context, w treeWriter, args ...string) (*cmdOutput, error) {
	t.Helper()
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { return nil }}, nil
		},
		nil,
		func(*treeDeps) treeWriter { return w },
	)
	out := &cmdOutput{}
	cmd.SetOut(&out.stdout)
	cmd.SetErr(&out.stderr)
	cmd.SetArgs(append(args, "--db-url", "postgres://x", "--worker", workerStub(t)))
	return out, cmd.ExecuteContext(ctx)
}

func TestNewTreeCmd_RegistersTheWriteSubcommands(t *testing.T) {
	names := map[string]bool{}
	for _, c := range newTreeCmd().Commands() {
		names[c.Name()] = true
	}

	for _, want := range []string{"add-root", "place", "remove"} {
		assert.True(t, names[want], "missing subcommand %s", want)
	}
}

func TestTreeAddRootCmd_PassesItsFlagsToTheWriter(t *testing.T) {
	w := &recordingWriter{}

	_, err := runWriteCmd(t, w, "add-root", "--tree-id", "t", "--user-id", "u", "--sponsor-id", "s",
		"--tree-type", "matrix", "--matrix-width", "3", "--matrix-spillover", "breadth_first",
		"--enrolled-at", "2026-09-23T08:00:00-04:00")

	require.NoError(t, err)
	width, spillover := 3, "breadth_first"
	assert.Equal(t, networkengine.AddRootRequest{
		TreeID: "t", UserID: "u", SponsorID: "s", TreeType: "matrix",
		MatrixWidth: &width, MatrixSpillover: &spillover,
		EnrolledAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	}, w.addRoot)
}

func TestTreeAddRootCmd_LeavesMatrixParametersUnsetWhenAbsent(t *testing.T) {
	w := &recordingWriter{}

	_, err := runWriteCmd(t, w, "add-root", "--tree-id", "t", "--user-id", "u", "--sponsor-id", "s",
		"--tree-type", "unilevel")

	require.NoError(t, err)
	assert.Nil(t, w.addRoot.MatrixWidth)
	assert.Nil(t, w.addRoot.MatrixSpillover)
}

func TestTreePlaceCmd_PassesItsFlagsToTheWriter(t *testing.T) {
	w := &recordingWriter{}

	_, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p",
		"--sponsor-id", "s", "--position", "0", "--enrolled-at", "2026-09-23T12:00:00Z")

	require.NoError(t, err)
	zero := 0
	assert.Equal(t, networkengine.PlaceRequest{
		TreeID: "t", UserID: "u", ParentID: "p", SponsorID: "s", Position: &zero,
		EnrolledAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	}, w.place)
}

func TestTreePlaceCmd_SendsNoPositionWhenTheFlagIsAbsent(t *testing.T) {
	w := &recordingWriter{}

	_, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "s")

	require.NoError(t, err)
	assert.Nil(t, w.place.Position)
}

func TestTreeRemoveCmd_PassesItsFlagsToTheWriter(t *testing.T) {
	w := &recordingWriter{}

	_, err := runWriteCmd(t, w, "remove", "--tree-id", "t", "--user-id", "u", "--removed-at", "2026-09-23T12:00:00Z")

	require.NoError(t, err)
	assert.Equal(t, networkengine.RemoveRequest{
		TreeID: "t", UserID: "u", RemovedAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	}, w.remove)
}

func TestTreeWriteCmds_DefaultTheirTimeToNowInUTC(t *testing.T) {
	w := &recordingWriter{}
	before := time.Now()

	_, err := runWriteCmd(t, w, "remove", "--tree-id", "t", "--user-id", "u")

	after := time.Now()
	require.NoError(t, err)
	assert.Equal(t, time.UTC, w.remove.RemovedAt.Location())
	assert.False(t, w.remove.RemovedAt.Before(before.Truncate(time.Second)), "%s is before %s", w.remove.RemovedAt, before)
	assert.False(t, w.remove.RemovedAt.After(after), "%s is after %s", w.remove.RemovedAt, after)
}

func TestTreeWriteCmds_RefuseATimeThatIsNotRFC3339(t *testing.T) {
	_, err := runWriteCmd(t, &recordingWriter{}, "remove", "--tree-id", "t", "--user-id", "u", "--removed-at", "yesterday")

	require.ErrorContains(t, err, `--removed-at "yesterday" is not an RFC 3339 time`)
}

func TestTreePlaceCmd_ReportsTheAppendOnStdout(t *testing.T) {
	w := &recordingWriter{result: networkengine.WriteResult{
		Stream: "tree-t", EventID: "e2", Version: 2,
		CaughtUp: &networkengine.CaughtUpEvent{EventID: "e1", Version: 1, Type: networkengine.EventTypeRootAdded},
	}}

	out, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	require.NoError(t, err)
	assert.Equal(t, "redelivered event e1 at version 1\nappended event e2 at version 2 to stream tree-t; projected\n",
		out.stdout.String())
	assert.Empty(t, out.stderr.String())
}

func TestTreePlaceCmd_ReportsARedeliveryAheadOfARefusal(t *testing.T) {
	refusal := errors.New("check_mutation for add_node in tree t returned: engine error [USER_NOT_FOUND]; nothing was appended")
	w := &recordingWriter{
		result: networkengine.WriteResult{
			Stream:   "tree-t",
			CaughtUp: &networkengine.CaughtUpEvent{EventID: "e1", Version: 1, Type: networkengine.EventTypeRootAdded},
		},
		err: refusal,
	}

	out, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	require.ErrorIs(t, err, refusal)
	assert.Equal(t, "redelivered event e1 at version 1\n", out.stdout.String())
}

// projectionFailure is a confirmed append at version 2 whose projection
// failed, with the version the writer read afterwards.
func projectionFailure(observed *networkengine.ProjectionObservation) networkengine.WriteResult {
	return networkengine.WriteResult{
		Stream: "tree-t", EventID: "e2", Version: 2,
		ProjectionErr: errors.New("engine add_node failed after 2 retries"),
		Observed:      observed,
	}
}

const projectionWarning = "warning: event e2 at version 2 was appended and its projection returned an error: " +
	"engine add_node failed after 2 retries."

func TestTreePlaceCmd_ReportsAProjectionFailureByWhatItObserved(t *testing.T) {
	notCurrent := "Error: event e2 at version 2 was appended and the store was not observed current\n"
	cases := []struct {
		name     string
		observed *networkengine.ProjectionObservation
		stderr   string
		exit     int
	}{
		{
			name:     "one behind",
			observed: &networkengine.ProjectionObservation{Version: 1, Found: true},
			stderr:   projectionWarning + " The tree's projected version is 1. The next write or tree load of this tree redelivers it.\n" + notCurrent,
			exit:     3,
		},
		{
			name:     "current",
			observed: &networkengine.ProjectionObservation{Version: 2, Found: true},
			stderr:   projectionWarning + " The tree's projected version is 2.\n",
			exit:     0,
		},
		{
			name:     "two behind",
			observed: &networkengine.ProjectionObservation{Version: 0, Found: true},
			stderr:   projectionWarning + " The tree's projected version is 0.\n" + notCurrent,
			exit:     3,
		},
		{
			name:     "no projection row",
			observed: &networkengine.ProjectionObservation{},
			stderr:   projectionWarning + " The tree has no projection row.\n" + notCurrent,
			exit:     3,
		},
		{
			name:     "the read failed",
			observed: &networkengine.ProjectionObservation{Err: errors.New("connection reset")},
			stderr:   projectionWarning + " The tree's projected version could not be read: connection reset.\n" + notCurrent,
			exit:     3,
		},
		{
			name:   "not read",
			stderr: projectionWarning + " The tree's projected version was not read.\n" + notCurrent,
			exit:   3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &recordingWriter{result: projectionFailure(tc.observed)}

			out, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

			assert.Equal(t, tc.exit, exitCode(err))
			assert.Equal(t, "appended event e2 at version 2 to stream tree-t; projection returned an error\n", out.stdout.String())
			assert.Equal(t, tc.stderr, out.stderr.String())
		})
	}
}

// The appended root is at version 1 and the tree was observed with no
// projection row.
func TestTreeAddRootCmd_PromisesARedeliveryWhenTheRootLeftNoProjectionRow(t *testing.T) {
	w := &recordingWriter{result: networkengine.WriteResult{
		Stream: "tree-t", EventID: "e1", Version: 1,
		ProjectionErr: errors.New("store root node: connection reset"),
		Observed:      &networkengine.ProjectionObservation{},
	}}

	out, err := runWriteCmd(t, w, "add-root", "--tree-id", "t", "--user-id", "u", "--sponsor-id", "u", "--tree-type", "unilevel")

	assert.Equal(t, 3, exitCode(err))
	assert.Equal(t, "warning: event e1 at version 1 was appended and its projection returned an error: store root node: connection reset. "+
		"The tree has no projection row. The next write or tree load of this tree redelivers it.\n"+
		"Error: event e1 at version 1 was appended and the store was not observed current\n", out.stderr.String())
}

func TestTreePlaceCmd_ExitsZeroForAStoreObservedPastARefusedEvent(t *testing.T) {
	refused := &networkengine.ProjectionRefusedError{TreeID: "t", EventVersion: 2, ProjectedVersion: 3}
	w := &recordingWriter{result: networkengine.WriteResult{
		Stream: "tree-t", EventID: "e2", Version: 2,
		ProjectionErr: fmt.Errorf("project event e2 at version 2 in stream tree-t: %w", refused),
		Observed:      &networkengine.ProjectionObservation{Version: 3, Found: true},
	}}

	out, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	require.NoError(t, err, "a store observed past the event exits 0")
	assert.Equal(t, "appended event e2 at version 2 to stream tree-t; projection returned an error\n", out.stdout.String())
	assert.Equal(t, "warning: event e2 at version 2 was appended and its projection returned an error: "+
		"project event e2 at version 2 in stream tree-t: tree t has projected version 3; "+
		"the event at version 2 was not projected. The tree's projected version is 3.\n", out.stderr.String())
}

func TestTreeWriteCmds_NameTheirExitCodesInTheirHelp(t *testing.T) {
	var named []string
	for _, c := range newTreeCmd().Commands() {
		switch c.Name() {
		case "add-root", "place", "remove":
			named = append(named, c.Name())
			assert.Contains(t, c.Long, "Exits 0 when the event was appended and the store was observed current, "+
				"with any warnings on stderr.", "%s help", c.Name())
			assert.Contains(t, c.Long, "Exits 3 when the event was appended and the store was not observed current.",
				"%s help", c.Name())
			assert.Contains(t, c.Long, "Exits 1 when no append was confirmed.", "%s help", c.Name())
		case "load":
			named = append(named, c.Name())
			assert.NotContains(t, c.Long, "Exits 3", "load never appends")
			assert.Contains(t, c.Long, "Exits 1 when", "load help")
		}
	}
	assert.ElementsMatch(t, []string{"add-root", "place", "remove", "load"}, named)
}

func TestTreePlaceCmd_WarnsOnAReleaseFailureAfterAProjectionFailure(t *testing.T) {
	res := projectionFailure(&networkengine.ProjectionObservation{Version: 1, Found: true})
	res.ReleaseErr = errors.New("pg_advisory_unlock for tree t returned false")
	w := &recordingWriter{result: res}

	out, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	assert.Equal(t, 3, exitCode(err))
	assert.Equal(t, projectionWarning+" The tree's projected version is 1. The next write or tree load of this tree redelivers it.\n"+
		"warning: releasing the tree lock reported: pg_advisory_unlock for tree t returned false\n"+
		"Error: event e2 at version 2 was appended and the store was not observed current\n", out.stderr.String())
}

func TestTreePlaceCmd_ExitsNonZeroWhenTheOutcomeIsUnknown(t *testing.T) {
	w := &recordingWriter{
		result: networkengine.WriteResult{Stream: "tree-t"},
		err: &networkengine.AppendOutcomeUnknownError{
			Stream: "tree-t", Version: 2, EventID: "e2",
			AppendErr: errors.New("connection reset"), ReadErr: errors.New("read timed out"),
		},
	}

	out, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	var unknown *networkengine.AppendOutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Empty(t, out.stdout.String())
	assert.Contains(t, out.stderr.String(), "reading version 2 to confirm it returned: read timed out")
	assert.NotContains(t, out.stderr.String(), "Usage:")
}

func TestTreeRemoveCmd_WarnsWhenTheLockReleaseFails(t *testing.T) {
	w := &recordingWriter{result: networkengine.WriteResult{
		Stream: "tree-t", EventID: "e3", Version: 3,
		ReleaseErr: errors.New("pg_advisory_unlock for tree t returned false"),
	}}

	out, err := runWriteCmd(t, w, "remove", "--tree-id", "t", "--user-id", "u")

	require.NoError(t, err)
	assert.Equal(t, "warning: releasing the tree lock reported: pg_advisory_unlock for tree t returned false\n",
		out.stderr.String())
}

// sigWriter raises one signal at this process and reports whether the
// context it was handed saw the cancellation.
type sigWriter struct {
	recordingWriter
	sig     syscall.Signal
	sawDone bool
}

func (w *sigWriter) Place(ctx context.Context, _ networkengine.PlaceRequest) (networkengine.WriteResult, error) {
	if err := syscall.Kill(syscall.Getpid(), w.sig); err != nil {
		return networkengine.WriteResult{}, err
	}
	select {
	case <-ctx.Done():
		w.sawDone = true
	case <-time.After(2 * time.Second):
	}
	return networkengine.WriteResult{}, ctx.Err()
}

func TestTreePlaceCmd_SignalsCancelTheWrite(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			keepAlive := make(chan os.Signal, 1)
			signal.Notify(keepAlive, sig)
			defer signal.Stop(keepAlive)
			w := &sigWriter{sig: sig}

			_, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

			require.True(t, w.sawDone, "%s must reach the writer's context", sig)
			require.ErrorContains(t, err, "signal received) and no append was confirmed")
		})
	}
}

func TestTreePlaceCmd_ReportsAnUnknownOutcomeAheadOfItsCancellation(t *testing.T) {
	w := &recordingWriter{
		result: networkengine.WriteResult{Stream: "tree-t"},
		err: &networkengine.AppendOutcomeUnknownError{
			Stream: "tree-t", Version: 2, EventID: "e2",
			AppendErr: context.Canceled, ReadErr: errors.New("read timed out"),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := runWriteCmdContext(t, ctx, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	var unknown *networkengine.AppendOutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Contains(t, out.stderr.String(), "whether the event was appended is unknown")
	assert.NotContains(t, out.stderr.String(), "context ended")
}

func TestTreePlaceCmd_ReportsAWriteWhoseContextEnded(t *testing.T) {
	w := &recordingWriter{result: networkengine.WriteResult{Stream: "tree-t"}, err: context.Canceled}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := runWriteCmdContext(t, ctx, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, out.stderr.String(),
		"the command's context ended (context canceled) and no append was confirmed: context canceled")
}

func TestTreePlaceCmd_ReportsAnInternalDeadlineUnchanged(t *testing.T) {
	inner := fmt.Errorf("store write: %w", context.DeadlineExceeded)
	w := &recordingWriter{result: networkengine.WriteResult{Stream: "tree-t"}, err: inner}

	out, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	require.Equal(t, inner, err)
	assert.NotContains(t, out.stderr.String(), "context ended")
}

func TestTreePlaceCmd_WarnsOnAReleaseFailureAlongsideAnError(t *testing.T) {
	unknown := &networkengine.AppendOutcomeUnknownError{
		Stream: "tree-t", Version: 2, EventID: "e2",
		AppendErr: errors.New("connection reset"), ReadErr: errors.New("read timed out"),
	}
	w := &recordingWriter{
		result: networkengine.WriteResult{Stream: "tree-t", ReleaseErr: errors.New("pg_advisory_unlock for tree t returned false")},
		err:    unknown,
	}

	out, err := runWriteCmd(t, w, "place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p")

	require.ErrorIs(t, err, unknown)
	assert.Contains(t, out.stderr.String(), "warning: releasing the tree lock reported: pg_advisory_unlock for tree t returned false\n")
}

func TestTreeWriteCmds_RequireTheirFlags(t *testing.T) {
	cases := map[string][]string{
		"add-root": {"--tree-id", "t", "--user-id", "u", "--sponsor-id", "s", "--tree-type", "unilevel"},
		"place":    {"--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "s"},
		"remove":   {"--tree-id", "t", "--user-id", "u"},
	}
	for command, flags := range cases {
		for i := 0; i < len(flags); i += 2 {
			omitted := flags[i]
			t.Run(command+" without "+omitted, func(t *testing.T) {
				args := append([]string{command}, flags[:i]...)
				args = append(args, flags[i+2:]...)
				w := &recordingWriter{}

				_, err := runWriteCmd(t, w, args...)

				require.ErrorContains(t, err, `required flag(s) "`+omitted[2:]+`" not set`)
			})
		}
	}
}
