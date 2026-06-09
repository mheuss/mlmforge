// Package period turns a compensation plan's PeriodConfig into ordered,
// lexicographically sortable period_id labels. It is pure: no clock, no I/O.
package period

import "fmt"

// Length is a commission period cadence. Mirrors the config "length" strings.
type Length int

const (
	Week Length = iota
	SemiMonth
	Month
	Quarter
)

// ParseLength maps a config length string to a Length.
func ParseLength(s string) (Length, error) {
	switch s {
	case "week":
		return Week, nil
	case "semi_month":
		return SemiMonth, nil
	case "month":
		return Month, nil
	case "quarter":
		return Quarter, nil
	default:
		return 0, fmt.Errorf("period: unknown length %q", s)
	}
}
