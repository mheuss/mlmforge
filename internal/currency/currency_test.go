package currency

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMinorUnitsOfKnownCodes(t *testing.T) {
	for code, want := range map[string]int32{"USD": 2, "JPY": 0, "KWD": 3} {
		got, ok := MinorUnits(code)
		assert.True(t, ok, code)
		assert.Equal(t, want, got, code)
	}
}

func TestMinorUnitsRefusesCodesTheTableDoesNotHold(t *testing.T) {
	for _, code := range []string{"ZZZ", "XAU", "CLF", "XTS", "usd", "", "code"} {
		_, ok := MinorUnits(code)
		assert.False(t, ok, code)
	}
}

func TestTableBytesArePinned(t *testing.T) {
	sum := sha256.Sum256([]byte(table))
	assert.Equal(t, "4952a4e0fbf6b2072c08838c4845d540a210f5d1d2cd4e539b97675744d5b116", hex.EncodeToString(sum[:]))
}

func TestEveryTableRowIsACodeAndADigit(t *testing.T) {
	row := regexp.MustCompile(`^[A-Z]{3},[0-9]$`)
	lines := strings.Split(strings.TrimSuffix(table, "\n"), "\n")
	var rows int
	for _, line := range lines {
		if strings.HasPrefix(line, "#") || line == "code,minor_units" {
			continue
		}
		assert.Regexp(t, row, line)
		rows++
	}
	assert.Equal(t, 155, rows)
}
