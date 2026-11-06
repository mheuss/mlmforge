package platform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalUUID(t *testing.T) {
	const want = "aaaaaaaa-aaaa-aaaa-aaaa-000000000001"
	for _, tc := range []struct {
		name string
		in   string
		ok   bool
	}{
		{"canonical", want, true},
		{"uppercase", strings.ToUpper(want), true},
		{"bare 32 hex", "aaaaaaaaaaaaaaaaaaaa000000000001", true},
		{"hyphen after every group of four", "aaaa-aaaa-aaaa-aaaa-aaaa-0000-0000-0001", true},
		{"braced 32 hex", "{aaaaaaaaaaaaaaaaaaaa000000000001}", true},
		{"braced hyphenated", "{" + want + "}", true},
		{"urn:uuid: prefix", "urn:uuid:" + want, false},
		{"38 characters wrapped in characters that are not braces", "x" + want + "x", false},
		{"opening brace with no closing brace", "{" + want, false},
		{"closing brace with no opening brace", want + "}", false},
		{"hyphen after two hex digits", "aa-aaaaaa-aaaa-aaaa-aaaa-000000000001", false},
		{"trailing hyphen", want + "-", false},
		{"31 hex digits", "aaaaaaaaaaaaaaaaaaaa00000000001", false},
		{"33 hex digits", "aaaaaaaaaaaaaaaaaaaa0000000000001", false},
		{"not a UUID", "tree1", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalUUID(tc.in)
			assert.Equal(t, tc.ok, ok, "ok for %q", tc.in)
			if tc.ok {
				assert.Equal(t, want, got)
			}
		})
	}
}
