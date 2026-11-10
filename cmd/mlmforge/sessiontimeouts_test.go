package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionTimeoutDefaults_ArePinned(t *testing.T) {
	assert.Equal(t, "30s", defaultStatementTimeout)
	assert.Equal(t, "11s", defaultLockTimeout)
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
