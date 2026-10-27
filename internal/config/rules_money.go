package config

import (
	"fmt"

	"github.com/mlmforge/mlmforge/internal/currency"
)

// validateCurrency refuses a base currency the minor-unit table does not hold.
func validateCurrency(plan *CompensationPlan) []ValidationError {
	code := plan.Volume.BaseCurrency
	if _, ok := currency.MinorUnits(code); ok {
		return nil
	}
	return []ValidationError{{
		Path:     "/volume/base_currency",
		Code:     "unknown_currency",
		Message:  fmt.Sprintf("volume base_currency %q is not in the minor-unit table", code),
		Severity: SeverityError,
	}}
}
