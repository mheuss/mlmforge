package platform

import (
	"encoding/hex"
	"strings"

	"github.com/google/uuid"
)

// CanonicalUUID returns id in lowercase hyphenated form, or false when
// Postgres's uuid input would refuse it.
func CanonicalUUID(id string) (string, bool) {
	rest, braced := strings.CutPrefix(id, "{")
	var u uuid.UUID
	for i := range u {
		if len(rest) < 2 {
			return "", false
		}
		if _, err := hex.Decode(u[i:i+1], []byte(rest[:2])); err != nil {
			return "", false
		}
		rest = rest[2:]
		if i%2 == 1 && i < len(u)-1 {
			rest, _ = strings.CutPrefix(rest, "-")
		}
	}
	if braced {
		var closed bool
		if rest, closed = strings.CutPrefix(rest, "}"); !closed {
			return "", false
		}
	}
	return u.String(), rest == ""
}
