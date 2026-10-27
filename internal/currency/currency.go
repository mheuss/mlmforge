// Package currency holds the ISO 4217 minor unit of each currency code.
package currency

import (
	_ "embed"
	"strconv"
	"strings"
)

//go:embed minor_units.csv
var table string

// MinorUnits returns the decimal places of code's minor unit, and false when the table does not hold code.
func MinorUnits(code string) (int32, bool) {
	header := true
	for _, line := range strings.Split(table, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if header {
			header = false
			continue
		}
		c, units, ok := strings.Cut(line, ",")
		if !ok || c != code {
			continue
		}
		n, err := strconv.ParseInt(units, 10, 32)
		if err != nil {
			return 0, false
		}
		return int32(n), true
	}
	return 0, false
}
