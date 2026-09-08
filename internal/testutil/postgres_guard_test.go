package testutil

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// go test counts a skipped test as a success, so a CI run that could not start
// Postgres would report green having asserted nothing about any Postgres seam
// (HEU-678). An unset CI keeps the old behavior, so the per-test container
// checks still skip.
func TestPostgresStartupIsFatal(t *testing.T) {
	failed := errors.New("start postgres container: cannot connect to the docker daemon")

	tests := []struct {
		name string
		err  error
		ci   string
		want bool
	}{
		{name: "startup failed with CI set", err: failed, ci: "true", want: true},
		{name: "startup failed with CI unset", err: failed, ci: "", want: false},
		{name: "startup succeeded with CI set", err: nil, ci: "true", want: false},
		{name: "startup succeeded with CI unset", err: nil, ci: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, postgresStartupIsFatal(tt.err, tt.ci))
		})
	}
}

// A panic reaches no guard. RequirePostgresInCI takes an error, so a container
// library that panics instead of returning one leaves CI with no policy applied
// (HEU-682).
func TestPanicToError(t *testing.T) {
	inner := errors.New("check host \"unix:///var/run/docker.sock\": docker info: Cannot connect to the Docker daemon")

	t.Run("no panic", func(t *testing.T) {
		assert.NoError(t, panicToError(nil))
	})

	t.Run("panic with an error keeps it wrapped", func(t *testing.T) {
		got := panicToError(inner)

		require.Error(t, got)
		assert.ErrorIs(t, got, inner)
		assert.Contains(t, got.Error(), "panicked")
	})

	t.Run("panic with a non-error keeps its text", func(t *testing.T) {
		got := panicToError("docker host not set")

		require.Error(t, got)
		assert.Contains(t, got.Error(), "docker host not set")
		assert.Contains(t, got.Error(), "panicked")
	})
}
