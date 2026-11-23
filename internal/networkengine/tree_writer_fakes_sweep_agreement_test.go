package networkengine

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	sweepTestName            = "TestFakeWriterEngine_SweepsMatrixRemovalsAsTheWorkerDoes"
	sweepSeedsEnv            = "FAKE_SWEEP_SEEDS"
	sweepDefaultFirst        = 1
	sweepDefaultLast         = 200
	sweepNonLeafPromoteFloor = 600
	sweepNonLeafPct          = 90
	sweepMostChildrenPct     = 75
	sweepPromotePct          = 90
	sweepRemovalAttempts     = 6
)

// parseSweepSeeds returns the inclusive seed range a FAKE_SWEEP_SEEDS value names.
func parseSweepSeeds(value string) (int, int, error) {
	if value == "" {
		return 0, 0, fmt.Errorf("%s is set but empty", sweepSeedsEnv)
	}
	a, b, ok := strings.Cut(value, "-")
	first, errFirst := strconv.Atoi(a)
	last, errLast := strconv.Atoi(b)
	if !ok || errFirst != nil || errLast != nil {
		return 0, 0, fmt.Errorf("%s=%q is not two integers joined by -", sweepSeedsEnv, value)
	}
	if first < 1 || last < 1 {
		return 0, 0, fmt.Errorf("%s=%q holds a number below 1", sweepSeedsEnv, value)
	}
	if first > last {
		return 0, 0, fmt.Errorf("%s=%q has %d above %d", sweepSeedsEnv, value, first, last)
	}
	return first, last, nil
}

func TestParseSweepSeeds(t *testing.T) {
	for _, row := range []struct {
		value       string
		first, last int
	}{
		{"1-200", 1, 200},
		{"203-203", 203, 203},
		{"1-5000", 1, 5000},
	} {
		first, last, err := parseSweepSeeds(row.value)
		require.NoError(t, err, row.value)
		require.Equal(t, [2]int{row.first, row.last}, [2]int{first, last}, row.value)
	}
	for _, row := range []struct{ value, message string }{
		{"", "FAKE_SWEEP_SEEDS is set but empty"},
		{"203", `FAKE_SWEEP_SEEDS="203" is not two integers joined by -`},
		{"a-3", `FAKE_SWEEP_SEEDS="a-3" is not two integers joined by -`},
		{"1-2-3", `FAKE_SWEEP_SEEDS="1-2-3" is not two integers joined by -`},
		{"0-5", `FAKE_SWEEP_SEEDS="0-5" holds a number below 1`},
		{"5-1", `FAKE_SWEEP_SEEDS="5-1" has 5 above 1`},
	} {
		_, _, err := parseSweepSeeds(row.value)
		require.EqualError(t, err, row.message, "value %q", row.value)
	}
}
