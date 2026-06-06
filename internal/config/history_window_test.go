package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMaxHistoryDepth_NoWindowGate returns 0 when no rank carries a window
// gate. minimalPlan() ships without windows, so the depth is zero.
func TestMaxHistoryDepth_NoWindowGate(t *testing.T) {
	plan := minimalPlan()
	assert.Equal(t, 0, MaxHistoryDepth(plan))
}

// TestMaxHistoryDepth_ReturnsMaxWindowPeriods returns the deepest
// window_periods across all ranks so the periodic driver can size the axis.
func TestMaxHistoryDepth_ReturnsMaxWindowPeriods(t *testing.T) {
	plan := minimalPlan()
	// Two ranks carry windows of differing depth; the larger wins.
	plan.Ranks[0].Qualification.Window = &RankQualificationWindow{
		ThresholdRank:     "Associate",
		QualifyingPeriods: 2,
		WindowPeriods:     3,
	}
	plan.Ranks[1].Qualification.Window = &RankQualificationWindow{
		ThresholdRank:     "Silver",
		QualifyingPeriods: 4,
		WindowPeriods:     6,
	}

	assert.Equal(t, 6, MaxHistoryDepth(plan))
}

// TestMaxHistoryDepth_SingleWindow returns that rank's window_periods when only
// one rank has a gate.
func TestMaxHistoryDepth_SingleWindow(t *testing.T) {
	plan := minimalPlan()
	plan.Ranks[1].Qualification.Window = &RankQualificationWindow{
		ThresholdRank:     "Silver",
		QualifyingPeriods: 3,
		WindowPeriods:     5,
	}

	assert.Equal(t, 5, MaxHistoryDepth(plan))
}
