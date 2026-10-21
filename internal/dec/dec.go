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
	"gopkg.in/yaml.v3"
)

// Amount is an exact decimal that crosses JSON as a bare number.
type Amount struct {
	decimal.Decimal
}

var numberText = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// maxTextLength bounds the text Parse reads, so the work per value stays small.
const maxTextLength = 64

// maxExponent bounds the written exponent, so a short input cannot stand for a
// value with a vast number of digits.
const maxExponent = 60

// LooksLikeNumber reports whether text has the decimal form Parse reads,
// before Parse applies its length and exponent limits.
func LooksLikeNumber(text string) bool {
	return numberText.MatchString(text)
}

// Parse reads plain decimal text such as "12.50" or "1e-7".
func Parse(text string) (Amount, error) {
	if len(text) > maxTextLength {
		return Amount{}, fmt.Errorf("decimal text is %d bytes long; the limit is %d", len(text), maxTextLength)
	}
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
	if canonical := d.String(); len(canonical) > maxTextLength {
		return Amount{}, fmt.Errorf("decimal %q: its canonical text is %d bytes long; the limit is %d", text, len(canonical), maxTextLength)
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

// UnmarshalText reads decimal text.
func (a *Amount) UnmarshalText(text []byte) error {
	parsed, err := Parse(string(text))
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// UnmarshalYAML reads a YAML int or float scalar and refuses any other node.
func (a *Amount) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode || (n.ShortTag() != "!!int" && n.ShortTag() != "!!float") {
		return fmt.Errorf("decimal: line %d: want a YAML number, got a %s node tagged %s", n.Line, kindName(n.Kind), n.ShortTag())
	}
	return a.UnmarshalText([]byte(n.Value))
}

func kindName(k yaml.Kind) string {
	switch k {
	case yaml.ScalarNode:
		return "scalar"
	case yaml.MappingNode:
		return "mapping"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.AliasNode:
		return "alias"
	case yaml.DocumentNode:
		return "document"
	default:
		return fmt.Sprintf("kind %d", k)
	}
}

// Scan reads decimal text from a database and refuses any other type.
func (a *Amount) Scan(value any) error {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("decimal: cannot scan %T; want decimal text", value)
	}
	parsed, err := Parse(text)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// MarshalBinary writes the canonical decimal text.
func (a Amount) MarshalBinary() ([]byte, error) {
	return []byte(a.String()), nil
}

// UnmarshalBinary reads decimal text.
func (a *Amount) UnmarshalBinary(data []byte) error {
	return a.UnmarshalText(data)
}

// GobEncode writes the canonical decimal text.
func (a Amount) GobEncode() ([]byte, error) {
	return a.MarshalBinary()
}

// GobDecode reads decimal text.
func (a *Amount) GobDecode(data []byte) error {
	return a.UnmarshalText(data)
}

// DecodeSpanner refuses every value.
func (a *Amount) DecodeSpanner(value any) error {
	return fmt.Errorf("decimal: Amount does not decode from Spanner, got %T", value)
}

// UnmarshalBSONValue refuses every value.
func (a *Amount) UnmarshalBSONValue(typ byte, _ []byte) error {
	return fmt.Errorf("decimal: Amount does not decode from BSON, got type 0x%02x", typ)
}

// Scale returns the number of decimal places the value was written with.
func (a Amount) Scale() int32 {
	if e := a.Exponent(); e < 0 {
		return -e
	}
	return 0
}
