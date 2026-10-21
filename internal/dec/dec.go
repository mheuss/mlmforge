// Package dec carries exact decimal values for volume, money and rates.
package dec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// Amount is an exact decimal that crosses JSON as a bare number.
type Amount struct {
	decimal.Decimal
}

var numberText = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// maxExponent bounds the written exponent. Beyond it, shopspring's text and
// comparison methods allocate in proportion to the exponent.
const maxExponent = 60

// Parse reads plain decimal text such as "12.50" or "1e-7".
func Parse(text string) (Amount, error) {
	if !numberText.MatchString(text) {
		return Amount{}, fmt.Errorf("decimal %q: not a plain decimal number", text)
	}
	if at := strings.IndexAny(text, "eE"); at >= 0 {
		e, err := strconv.ParseInt(text[at+1:], 10, 64)
		if err != nil || e > maxExponent || e < -maxExponent {
			return Amount{}, fmt.Errorf("decimal %q: the exponent is outside ±%d", text, maxExponent)
		}
	}
	d, err := decimal.NewFromString(text)
	if err != nil {
		return Amount{}, fmt.Errorf("decimal %q: %w", text, err)
	}
	return Amount{d}, nil
}

// MustParse is Parse for literals known to be valid.
func MustParse(text string) Amount {
	a, err := Parse(text)
	if err != nil {
		panic(err)
	}
	return a
}

// MarshalJSON writes the canonical decimal text as a bare JSON number.
func (a Amount) MarshalJSON() ([]byte, error) {
	return []byte(a.String()), nil
}

// UnmarshalJSON reads a bare JSON number and refuses a quoted one or null.
func (a *Amount) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if string(trimmed) == "null" {
		return fmt.Errorf("decimal: got null; send a bare JSON number")
	}
	if len(trimmed) > 0 && trimmed[0] == '"' {
		return fmt.Errorf("decimal: got the string %s; send a bare JSON number", trimmed)
	}
	var n json.Number
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return fmt.Errorf("decimal: %s is not a JSON number: %w", trimmed, err)
	}
	parsed, err := Parse(n.String())
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// UnmarshalText reads a YAML scalar's text.
func (a *Amount) UnmarshalText(text []byte) error {
	parsed, err := Parse(string(text))
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// Scale returns the number of decimal places the value was written with.
func (a Amount) Scale() int32 {
	if e := a.Exponent(); e < 0 {
		return -e
	}
	return 0
}
