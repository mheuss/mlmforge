package dec

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type amounts struct {
	A Amount            `json:"a" yaml:"a"`
	P *Amount           `json:"p" yaml:"p"`
	Z Amount            `json:"z,omitzero" yaml:"z"`
	M map[string]Amount `json:"m,omitempty" yaml:"m"`
}

func TestAmountMarshalsTheCanonicalBareNumber(t *testing.T) {
	cases := map[string]string{
		"30.30": "30.3",
		"2.000": "2",
		"-0.0":  "0",
		"1e-7":  "0.0000001",
		"100":   "100",
		"0.05":  "0.05",
	}
	for in, want := range cases {
		out, err := json.Marshal(MustParse(in))
		require.NoError(t, err)
		assert.Equal(t, want, string(out), "input %s", in)
	}
}

func TestAmountReadsABareJSONNumberExactly(t *testing.T) {
	var a amounts
	require.NoError(t, json.Unmarshal([]byte(`{"a":123456789012345678.123456789,"p":0.1}`), &a))
	assert.Equal(t, "123456789012345678.123456789", a.A.String())
	assert.Equal(t, "0.1", a.P.String())
}

func TestAmountRefusesAQuotedNumber(t *testing.T) {
	var a amounts
	err := json.Unmarshal([]byte(`{"a":"2"}`), &a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `got the string "2"`)
}

func TestAmountRefusesNullForAValue(t *testing.T) {
	var a amounts
	require.NoError(t, json.Unmarshal([]byte(`{"p":null}`), &a))
	assert.Nil(t, a.P)
	err := json.Unmarshal([]byte(`{"a":null}`), &a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "got null")
}

func TestParseRefusesAnExponentPastSixty(t *testing.T) {
	for _, text := range []string{"0e-2147483648", "1e61", "1e-61", "1e9223372036854775807", "1e99999999999999999999"} {
		_, err := Parse(text)
		require.Error(t, err, text)
		assert.Contains(t, err.Error(), "exponent", text)
	}
	for _, text := range []string{"1e60", "1e-60"} {
		_, err := Parse(text)
		assert.NoError(t, err, text)
	}
}

func TestScanAcceptsOnlyDecimalText(t *testing.T) {
	var a Amount
	require.NoError(t, a.Scan("12.50"))
	assert.Equal(t, "12.5", a.String())
	require.NoError(t, a.Scan([]byte("0.07")))
	assert.Equal(t, "0.07", a.String())
	assert.Error(t, a.Scan(0.1))
	assert.Error(t, a.Scan("1e99"))
	assert.Error(t, a.Scan("NaN"))
}

func TestAmountOmitsAZeroFieldTaggedOmitzero(t *testing.T) {
	out, err := json.Marshal(amounts{A: MustParse("1")})
	require.NoError(t, err)
	assert.Equal(t, `{"a":1,"p":null}`, string(out))
}

func TestAmountReadsYAMLScalarText(t *testing.T) {
	var a amounts
	require.NoError(t, yaml.Unmarshal([]byte("a: 30.30\np: 1e-7\nm:\n  1: 0.07\n"), &a))
	assert.Equal(t, "30.3", a.A.String())
	assert.Equal(t, "0.0000001", a.P.String())
	assert.Equal(t, "0.07", a.M["1"].String())
}

func TestAmountRefusesYAMLFormsThatAreNotDecimals(t *testing.T) {
	for _, src := range []string{"a: .inf\n", "a: .nan\n", "a: 0x10\n", "a: 1_000\n"} {
		var a amounts
		assert.Error(t, yaml.Unmarshal([]byte(src), &a), src)
	}
}

func TestAmountScaleCountsWrittenDecimalPlaces(t *testing.T) {
	assert.Equal(t, int32(2), MustParse("30.30").Scale())
	assert.Equal(t, int32(0), MustParse("100").Scale())
	assert.Equal(t, int32(10), MustParse("0.0000000001").Scale())
	assert.Equal(t, int32(0), MustParse("1e3").Scale())
}

func TestParseRefusesNonFiniteText(t *testing.T) {
	for _, text := range []string{"NaN", "Infinity", "-Infinity", "", "+1", ".5", "01"} {
		_, err := Parse(text)
		assert.Error(t, err, text)
	}
}

func TestGobRoundTripsThroughDecimalText(t *testing.T) {
	type box struct{ A Amount }
	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(box{A: MustParse("123456789012345678.123456789")}))
	var out box
	require.NoError(t, gob.NewDecoder(&buf).Decode(&out))
	assert.Equal(t, "123456789012345678.123456789", out.A.String())
}

func TestBinaryDecodersReadOnlyDecimalText(t *testing.T) {
	var a Amount
	assert.Error(t, a.UnmarshalBinary([]byte("1e99")))
	assert.Error(t, a.GobDecode([]byte("NaN")))
	require.NoError(t, a.UnmarshalBinary([]byte("0.07")))
	assert.Equal(t, "0.07", a.String())
	require.NoError(t, a.GobDecode([]byte("2.50")))
	assert.Equal(t, "2.5", a.String())
}

func TestUnusedDecodersRefuse(t *testing.T) {
	var a Amount
	assert.Error(t, a.DecodeSpanner("1.5"))
	assert.Error(t, a.UnmarshalBSONValue(0x01, []byte{0, 0, 0, 0, 0, 0, 0xf8, 0x3f}))
}

func TestAmountReadsOnlyANumericYAMLScalar(t *testing.T) {
	type box struct {
		A Amount `yaml:"a"`
	}
	for _, src := range []string{"a: {decimal: 1e999}\n", "a: {decimal: 1.5}\n", "a: [1]\n", "a: \"2\"\n", "a: !!str 3\n"} {
		var b box
		assert.Error(t, yaml.Unmarshal([]byte(src), &b), src)
	}
	var b box
	require.NoError(t, yaml.Unmarshal([]byte("a: 1.25\n"), &b))
	assert.Equal(t, "1.25", b.A.String())
	require.NoError(t, yaml.Unmarshal([]byte("a: 7\n"), &b))
	assert.Equal(t, "7", b.A.String())
}

func TestParseRefusesTextLongerThanTheLimit(t *testing.T) {
	_, err := Parse("1." + strings.Repeat("3", 63))
	require.EqualError(t, err, "decimal text is 65 bytes long; the limit is 64")
	_, err = Parse("1." + strings.Repeat("3", 62))
	assert.NoError(t, err)
}

func TestParseAcceptsOnlyValuesWhoseTextReadsBack(t *testing.T) {
	_, err := Parse("1.2345e-60")
	require.EqualError(t, err, `decimal "1.2345e-60": its canonical text is 66 bytes long; the limit is 64`)
	for _, text := range []string{"1e60", "-1e-60", "-1.5e-60", "9.99e59"} {
		a, err := Parse(text)
		require.NoError(t, err, text)
		raw, err := json.Marshal(a)
		require.NoError(t, err, text)
		var back Amount
		require.NoError(t, json.Unmarshal(raw, &back), text)
		assert.True(t, a.Equal(back.Decimal), text)
	}
}
