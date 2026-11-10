package main

import (
	"fmt"
	"sync"
)

// Default session timeouts, in SHOW format.
const (
	defaultStatementTimeout = "30s"
	defaultLockTimeout      = "9s"
)

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
