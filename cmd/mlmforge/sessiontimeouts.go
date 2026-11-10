package main

import "fmt"

// Default session timeouts for the tree commands, in SHOW format.
const (
	defaultStatementTimeout = "30s"
	defaultLockTimeout      = "11s"
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
