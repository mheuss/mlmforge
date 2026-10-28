package commerce

import (
	"encoding/json"
	"testing"

	"github.com/mlmforge/mlmforge/internal/dec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrderItemCVPointsSumExactlyToTheThreshold(t *testing.T) {
	a := OrderItem{ProductID: "p1", Quantity: 1, CVPoints: dec.MustParse("10.10")}
	b := OrderItem{ProductID: "p2", Quantity: 1, CVPoints: dec.MustParse("20.20")}
	threshold := dec.MustParse("30.30")

	sum := a.CVPoints.Add(b.CVPoints.Decimal)

	assert.True(t, sum.Equal(dec.MustParse("30.30").Decimal), "sum is %s", sum)
	assert.False(t, sum.LessThan(threshold.Decimal), "sum %s is below threshold %s", sum, threshold)
	x, y := 10.10, 20.20
	assert.Less(t, x+y, 30.30)
}

func TestOrderItemCVPointsRoundTripThroughJSON(t *testing.T) {
	in := OrderItem{ProductID: "p1", Quantity: 2, Price: dec.MustParse("19.99"), Currency: "USD", CVPoints: dec.MustParse("10.10")}

	data, err := json.Marshal(in)
	require.NoError(t, err)
	var out OrderItem
	require.NoError(t, json.Unmarshal(data, &out))

	assert.True(t, out.CVPoints.Equal(dec.MustParse("10.10").Decimal), "decoded CVPoints is %s", out.CVPoints)
}

func TestOrderItemNullCVPointsFailsToDecode(t *testing.T) {
	var out OrderItem

	err := json.Unmarshal([]byte(`{"ProductID":"p1","Quantity":1,"CVPoints":null}`), &out)

	assert.ErrorContains(t, err, "got null")
}
