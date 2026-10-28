package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindFloatIdents_NamesEachFloatOutsideTestsAndExceptions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	write("types.go", "package p\n"+
		"\n"+
		"type Order struct {\n"+
		"\tTotal float64\n"+
		"\tQty   int\n"+
		"}\n"+
		"\n"+
		"type Calc interface {\n"+
		"\tScale(r float32) error\n"+
		"}\n"+
		"\n"+
		"func half(x int) float64 { return float64(x) / 2 }\n"+
		"\n"+
		"type Mix struct {\n"+
		"\tRatio float64\n"+
		"}\n"+
		"\n"+
		"func (o *Order) Tax() float64 { return 0 }\n")
	write("types_test.go", "package p\n\nvar f float64\n")
	write("notes.txt", "float64\n")

	found, err := FindFloatIdents(dir, map[string]bool{"Mix.Ratio": true})

	require.NoError(t, err)
	assert.Equal(t, []string{
		"types.go:4: Order.Total float64",
		"types.go:9: Calc.Scale float32",
		"types.go:12: half float64",
		"types.go:12: half float64",
		"types.go:18: Order.Tax float64",
	}, found)
}
