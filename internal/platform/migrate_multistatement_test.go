package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefuseMultiStatement_RefusesEachValueTheDriverReadsAsOn(t *testing.T) {
	for _, value := range []string{"true", "1", "t", "TRUE"} {
		err := refuseMultiStatement("up", "postgres://u:p@h/db?sslmode=disable&x-multi-statement="+value)

		var refused *MultiStatementError
		require.ErrorAs(t, err, &refused, "value %q", value)
		assert.Equal(t, &MultiStatementError{Command: "up", Value: value}, refused)
	}
}

func TestRefuseMultiStatement_PassesAURLWithTheOptionOffOrAbsent(t *testing.T) {
	for _, dbURL := range []string{
		"postgres://u:p@h/db?sslmode=disable",
		"postgres://u:p@h/db?sslmode=disable&x-multi-statement=false",
		"postgres://u:p@h/db?sslmode=disable&x-multi-statement=0",
	} {
		assert.NoError(t, refuseMultiStatement("up", dbURL), dbURL)
	}
}

func TestRefuseMultiStatement_LeavesWhatItCannotParse(t *testing.T) {
	assert.NoError(t, refuseMultiStatement("up", "postgres://u:p@h/db?sslmode=disable&x-multi-statement=yes"))
	assert.NoError(t, refuseMultiStatement("up", "postgres://u:p@h:notaport/db?x-multi-statement=true"))
}
