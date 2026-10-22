package networkengine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A text scan cannot see an untyped float constant such as `x := 0.07`.
var floatType = regexp.MustCompile(`(?i)\bfloat(32|64)\b|Float(32|64)\(|NewFromFloat|InexactFloat|ParseFloat|FormatFloat|NullFloat64|::float[48]\b|double precision`)

// allowedFloatLines holds, per file, the one trimmed line that may match.
var allowedFloatLines = map[string]string{
	filepath.Join("..", "config", "rules.go"): "size := math.Pow(float64(s.Structure.Width), float64(s.Structure.Height))",
}

func TestNoFloatOnTheDecimalBoundary(t *testing.T) {
	files := []string{
		filepath.Join("..", "config", "types.go"),
		filepath.Join("..", "config", "translate.go"),
		filepath.Join("..", "config", "rules.go"),
		filepath.Join("..", "dec", "dec.go"),
		"wire_types.go",
		"types.go",
		"commission_detail.go",
		"config_types.go",
		"commission_run_store.go",
		"commission_run_store_memory.go",
		"commission_run_store_postgres.go",
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for n, line := range strings.Split(string(data), "\n") {
			code, _, _ := strings.Cut(line, "//")
			trimmed := strings.TrimSpace(line)
			if floatType.MatchString(code) && allowedFloatLines[f] != trimmed {
				t.Errorf("%s:%d: %s", f, n+1, trimmed)
			}
		}
	}
}
