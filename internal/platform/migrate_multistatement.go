package platform

import (
	"net/url"
	"strconv"
)

// refuseMultiStatement returns a MultiStatementError when dbURL turns on multi-statement mode.
func refuseMultiStatement(command, dbURL string) error {
	parsed, err := url.Parse(dbURL)
	if err != nil {
		return nil
	}
	value := parsed.Query().Get("x-multi-statement")
	if value == "" {
		return nil
	}
	// ParseBool rather than a compare with "true": "1", "t" and "TRUE" also turn the option on.
	on, err := strconv.ParseBool(value)
	if err != nil || !on {
		return nil
	}
	return &MultiStatementError{Command: command, Value: value}
}
