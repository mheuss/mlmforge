package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRulesRefuseABaseCurrencyMissingFromTheTable(t *testing.T) {
	plan := minimalPlan()
	plan.Volume.BaseCurrency = "ZZZ"
	plan.Payout.BaseCurrency = "ZZZ"

	errs := validateBusinessRules(plan)

	require.Len(t, errs, 1)
	assert.Equal(t, "/volume/base_currency", errs[0].Path)
	assert.Equal(t, "unknown_currency", errs[0].Code)
	assert.Equal(t, `volume base_currency "ZZZ" is not in the minor-unit table`, errs[0].Message)
	assert.Equal(t, SeverityError, errs[0].Severity)
}

func TestRulesAcceptEveryTestCurrency(t *testing.T) {
	for _, code := range []string{"USD", "JPY", "KWD"} {
		plan := minimalPlan()
		plan.Volume.BaseCurrency = code
		plan.Payout.BaseCurrency = code
		assert.Empty(t, validateCurrency(plan), code)
	}
}
