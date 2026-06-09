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

// advance returns the start of the period n steps from start (n may be < 0).
// start MUST be a period start (as returned by periodStart).
func (s *Sequence) advance(start time.Time, n int) time.Time {
	switch s.length {
	case Month:
		return start.AddDate(0, n, 0)
	case Quarter:
		return start.AddDate(0, 3*n, 0)
	case SemiMonth:
		half := 0
		if start.Day() == 16 {
			half = 1
		}
		total := half + n
		monthDelta := total / 2
		newHalf := total % 2
		if newHalf < 0 { // normalize Go's truncated modulo for negative n
			newHalf += 2
			monthDelta--
		}
		base := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, monthDelta, 0)
		if newHalf == 1 {
			return time.Date(base.Year(), base.Month(), 16, 0, 0, 0, 0, time.UTC)
		}
		return base
	case Week:
		return start.AddDate(0, 0, 7*n)
	}
	return start // unreachable
}

// Label returns the period_id for the period containing t. Length-specific,
// zero-padded, lexicographically sortable. Output-only: never parsed back.
func (s *Sequence) Label(t time.Time) string {
	start := s.periodStart(t)
	switch s.length {
	case Month:
		return start.Format("2006-01")
	case Quarter:
		q := (int(start.Month())-1)/3 + 1
		return fmt.Sprintf("%04d-Q%d", start.Year(), q)
	case SemiMonth:
		half := 1
		if start.Day() == 16 {
			half = 2
		}
		return fmt.Sprintf("%s-H%d", start.Format("2006-01"), half)
	case Week:
		isoYear, isoWeek := start.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", isoYear, isoWeek)
	}
	return "" // unreachable
}

// PriorLabels returns the labels of the n periods immediately before the period
// containing t, most-recent-first (DESC). Returns nil when n <= 0. Pure date math:
// labels for periods before the anchor are produced normally.
func (s *Sequence) PriorLabels(t time.Time, n int) []string {
	if n <= 0 {
		return nil
	}
	start := s.periodStart(t)
	labels := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		labels = append(labels, s.Label(s.advance(start, -i)))
	}
	return labels
}

// PeriodStartsInRange returns the start of each period from the one containing
// from to the one containing to, inclusive, ascending. nil if from is after to.
func (s *Sequence) PeriodStartsInRange(from, to time.Time) []time.Time {
	start := s.periodStart(from)
	end := s.periodStart(to)
	if start.After(end) {
		return nil
	}
	var starts []time.Time
	for cur := start; !cur.After(end); cur = s.advance(cur, 1) {
		starts = append(starts, cur)
	}
	return starts
}

// IsBeforeStart reports whether t's period falls before the plan's start period.
func (s *Sequence) IsBeforeStart(t time.Time) bool {
	return s.periodStart(t).Before(s.periodStart(s.anchor))
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
