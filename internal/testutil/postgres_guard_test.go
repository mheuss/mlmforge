package testutil

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// go test counts a skipped test as a success, so a CI run that could not start
// Postgres would report green having asserted nothing about any Postgres seam
// (HEU-678). Locally the skip is what lets the suite run without Docker, so an
// unset CI keeps the old behavior.
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
