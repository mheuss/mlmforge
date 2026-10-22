package networkengine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var floatType = regexp.MustCompile(`\bfloat(32|64)\b|ParseFloat|FormatFloat`)

func TestNoFloatOnTheDecimalBoundary(t *testing.T) {
	files := []string{
		filepath.Join("..", "config", "types.go"),
		"wire_types.go",
		"types.go",
		"commission_detail.go",
		"config_types.go",
		"commission_run_store.go",
		"commission_run_store_postgres.go",
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for n, line := range strings.Split(string(data), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if floatType.MatchString(code) {
				t.Errorf("%s:%d: %s", f, n+1, strings.TrimSpace(line))
			}
		}
	}
}
