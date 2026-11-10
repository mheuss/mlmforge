package main

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
