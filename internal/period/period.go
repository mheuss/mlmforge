// Package period turns a compensation plan's PeriodConfig into ordered,
// lexicographically sortable period_id labels. It is pure: no clock, no I/O.
package period

import (
	"fmt"
	"time"
)

// Length is a commission period cadence. Mirrors the config "length" strings.
type Length int

const (
	Week Length = iota
	SemiMonth
	Month
	Quarter
)

// Sequence derives period_ids for one plan. anchor is the plan start_date,
// reduced to a UTC calendar date.
type Sequence struct {
	length Length
	anchor time.Time
}

// NewSequence parses a plan's raw period config. startDate must be "2006-01-02".
// A missing or invalid start date is an error (design BR6).
func NewSequence(length, startDate string) (*Sequence, error) {
	l, err := ParseLength(length)
	if err != nil {
		return nil, err
	}
	if startDate == "" {
		return nil, fmt.Errorf("period: start_date is required")
	}
	anchor, err := time.ParseInLocation("2006-01-02", startDate, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("period: invalid start_date %q: %w", startDate, err)
	}
	return &Sequence{length: l, anchor: anchor}, nil
}

// dateOnly reduces t to its own calendar date at UTC midnight. The caller's
// Y/M/D is authoritative (not the UTC-shifted date), so labels are stable
// regardless of clock time, location, or DST.
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// periodStart returns the start date (UTC midnight) of the period containing t.
func (s *Sequence) periodStart(t time.Time) time.Time {
	d := dateOnly(t)
	switch s.length {
	case Month:
		return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
	case Quarter:
		startMonth := time.Month((int(d.Month())-1)/3*3 + 1) // 1, 4, 7, or 10
		return time.Date(d.Year(), startMonth, 1, 0, 0, 0, 0, time.UTC)
	case SemiMonth:
		if d.Day() <= 15 {
			return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(d.Year(), d.Month(), 16, 0, 0, 0, 0, time.UTC)
	case Week:
		days := int(d.Sub(s.anchor) / (24 * time.Hour)) // exact whole days in UTC
		bucket := days / 7
		if days < 0 && days%7 != 0 {
			bucket-- // floor toward negative infinity for pre-anchor dates
		}
		return s.anchor.AddDate(0, 0, bucket*7)
	}
	return d // unreachable: Length is validated in NewSequence
}

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
