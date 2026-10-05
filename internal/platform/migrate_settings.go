package platform

import (
	"net/url"
	"strconv"
)

// unusedSettings lists the golang-migrate URL settings mlmforge migrate refuses.
var unusedSettings = []string{"x-migrations-table", "x-migrations-table-quoted", "x-statement-timeout", "x-multi-statement-max-size"}

// refuseUnusedSettings refuses an unused golang-migrate setting, and an x-multi-statement value that is neither true nor false.
func refuseUnusedSettings(command, dbURL string) error {
	parsed, err := url.Parse(dbURL)
	if err != nil {
		return nil
	}
	q := parsed.Query()
	for _, name := range unusedSettings {
		if q.Has(name) {
			return &UnusedSettingError{Command: command, Name: name}
		}
	}
	if value := q.Get("x-multi-statement"); value != "" {
		if _, err := strconv.ParseBool(value); err != nil {
			return &InvalidSettingError{Command: command, Name: "x-multi-statement", Value: value}
		}
	}
	return nil
}
