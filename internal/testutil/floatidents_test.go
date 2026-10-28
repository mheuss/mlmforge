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
	write("extra.go", "package p\n"+
		"\n"+
		"type Price float64\n"+
		"\n"+
		"var Fee float64\n"+
		"\n"+
		"var Low, High float32\n"+
		"\n"+
		"type Wrapped struct {\n"+
		"\tfloat64\n"+
		"}\n"+
		"\n"+
		"type Box[T float64 | int] struct {\n"+
		"\tV T\n"+
		"}\n"+
		"\n"+
		"func (b *Box[T]) Scale() float64 { return 0 }\n"+
		"\n"+
		"type Pair[K comparable, V float32] struct{}\n"+
		"\n"+
		"func (p Pair[K, V]) Sum() float64 { return 0 }\n")
	write("types_test.go", "package p\n\nvar f float64\n")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "dir.go"), 0o755))
	write("notes.txt", "float64\n")

	found, err := FindFloatIdents(dir, map[string]bool{"Mix.Ratio": true, "Low": true})

	require.NoError(t, err)
	assert.Equal(t, []string{
		"extra.go:3: Price float64",
		"extra.go:5: Fee float64",
		"extra.go:7: High float32",
		"extra.go:10: Wrapped float64",
		"extra.go:13: Box float64",
		"extra.go:17: Box.Scale float64",
		"extra.go:19: Pair float32",
		"extra.go:21: Pair.Sum float64",
		"types.go:4: Order.Total float64",
		"types.go:9: Calc.Scale float32",
		"types.go:12: half float64",
		"types.go:12: half float64",
		"types.go:18: Order.Tax float64",
	}, found)
}
