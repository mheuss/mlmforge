package networkengine

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// goldenDetail reads a stored-shape fixture. Byte equality is the point: a
// renamed field or a reordered struct changes the bytes, and that is exactly
// the drift these fixtures exist to catch.
//
// These are the bytes the mapper hands to the driver, not the bytes in the
// table — jsonb re-emits keys sorted by length then bytewise. Do not compare
// a fixture against a SELECT.
//
// Surrounding whitespace is trimmed so an editor adding a trailing newline
// does not fail the comparison. Everything inside the JSON still has to match
// exactly.
func goldenDetail(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "commission_detail", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

func TestResultFromCommissionEarning(t *testing.T) {
	earner := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	in := CommissionEarningDTO{
		EarnerID:     earner.String(),
		SourceID:     "33333333-3333-3333-3333-333333333333",
		Level:        3,
		Rate:         0.05,
		CVAmount:     120,
		DollarAmount: 6,
	}

	got, err := ResultFromCommissionEarning(in)
	if err != nil {
		t.Fatalf("ResultFromCommissionEarning: %v", err)
	}
	if got.EarnerID != earner {
		t.Fatalf("EarnerID = %s, want %s", got.EarnerID, earner)
	}
	if got.DollarAmount != 6 {
		t.Fatalf("DollarAmount = %v, want 6", got.DollarAmount)
	}
	if want := goldenDetail(t, "commission_earning.json"); string(got.Detail) != want {
		t.Fatalf("detail =\n%s\nwant\n%s", got.Detail, want)
	}
}

func TestResultFromBinaryEarning(t *testing.T) {
	earner := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	in := BinaryCommissionEarningDTO{
		EarnerID:      earner.String(),
		PositionID:    "44444444-4444-4444-4444-444444444444",
		LeftVolume:    500,
		RightVolume:   300,
		MatchedVolume: 300,
		Ratio:         1,
		Percent:       0.1,
		DollarAmount:  30,
		Capped:        false,
	}

	got, err := ResultFromBinaryEarning(in)
	if err != nil {
		t.Fatalf("ResultFromBinaryEarning: %v", err)
	}
	if got.EarnerID != earner {
		t.Fatalf("EarnerID = %s, want %s", got.EarnerID, earner)
	}
	if got.DollarAmount != 30 {
		t.Fatalf("DollarAmount = %v, want 30", got.DollarAmount)
	}
	if want := goldenDetail(t, "binary.json"); string(got.Detail) != want {
		t.Fatalf("detail =\n%s\nwant\n%s", got.Detail, want)
	}
}

func TestResultFromBoardCycleEarning(t *testing.T) {
	earner := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	in := BoardCycleEarningDTO{
		EarnerID:     earner.String(),
		BoardID:      "55555555-5555-5555-5555-555555555555",
		DollarAmount: 25,
		CycleNumber:  2,
		Capped:       true,
	}

	got, err := ResultFromBoardCycleEarning(in)
	if err != nil {
		t.Fatalf("ResultFromBoardCycleEarning: %v", err)
	}
	if got.EarnerID != earner {
		t.Fatalf("EarnerID = %s, want %s", got.EarnerID, earner)
	}
	if got.DollarAmount != 25 {
		t.Fatalf("DollarAmount = %v, want 25", got.DollarAmount)
	}
	if want := goldenDetail(t, "board.json"); string(got.Detail) != want {
		t.Fatalf("detail =\n%s\nwant\n%s", got.Detail, want)
	}
}

var detailFixtures = []string{"commission_earning.json", "binary.json", "board.json"}

// TestDetailNeverRepeatsColumnFields guards the rule that makes the split
// worth having: earner_id and dollar_amount are columns, so repeating them
// inside detail would store the same fact twice and let the two disagree.
//
// It reads fixtures rather than structs on purpose, so it still fails after
// someone regenerates the goldens to match a bad change.
func TestDetailNeverRepeatsColumnFields(t *testing.T) {
	for _, name := range detailFixtures {
		t.Run(name, func(t *testing.T) {
			body := goldenDetail(t, name)
			for _, banned := range []string{`"earner_id"`, `"dollar_amount"`} {
				if strings.Contains(body, banned) {
					t.Fatalf("%s contains %s, which is a real column", name, banned)
				}
			}
		})
	}
}

// Every shape carries the version tag and a kind. The version alone cannot
// say which shape a row is: all three share one value, and the structure
// column holds a user-supplied name that maps to no shape reliably. Like the
// test above, this reads fixtures so a regenerated golden cannot hide a
// dropped key.
func TestDetailCarriesVersionAndKind(t *testing.T) {
	wantKinds := map[string]string{
		"commission_earning.json": kindCommissionEarning,
		"binary.json":             kindBinaryPairing,
		"board.json":              kindBoardCycle,
	}
	seen := map[string]bool{}
	for _, name := range detailFixtures {
		t.Run(name, func(t *testing.T) {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(goldenDetail(t, name)), &parsed); err != nil {
				t.Fatalf("fixture is not readable JSON: %v", err)
			}
			if parsed["v"] != float64(detailVersion) {
				t.Fatalf("v = %v, want %d", parsed["v"], detailVersion)
			}
			kind, ok := parsed["kind"].(string)
			if !ok {
				t.Fatalf("kind = %v, want a string", parsed["kind"])
			}
			if kind != wantKinds[name] {
				t.Fatalf("kind = %q, want %q", kind, wantKinds[name])
			}
			if seen[kind] {
				t.Fatalf("kind %q is used by more than one shape, so it cannot discriminate", kind)
			}
			seen[kind] = true
		})
	}
}

// A field added to a wire DTO but not to its detail struct is silently
// dropped: the golden fixtures do not change, the struct literals still
// compile, and nothing fails. It surfaces years later as a column that was
// never stored, and design-rationale 027 keeps these rows precisely because
// the source events are gone by then — so it is not backfillable.
//
// This pins every DTO field as either a column or a detail key. Adding one
// fails here and forces the decision.
func TestEveryDTOFieldReachesAColumnOrDetail(t *testing.T) {
	// earner_id and dollar_amount are real columns, so they must NOT appear
	// in detail. Everything else must.
	const (
		earnerTag = "earner_id"
		amountTag = "dollar_amount"
	)
	cases := []struct {
		name   string
		dto    any
		detail any
		// Keys detail adds that no DTO supplies.
		extra []string
	}{
		{"commission earning", CommissionEarningDTO{}, commissionEarningDetail{}, []string{"v", "kind"}},
		{"binary pairing", BinaryCommissionEarningDTO{}, binaryPairingDetail{}, []string{"v", "kind"}},
		{"board cycle", BoardCycleEarningDTO{}, boardCycleDetail{}, []string{"v", "kind"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dtoTags := jsonTags(t, tc.dto)
			detailTags := jsonTags(t, tc.detail)

			for _, col := range []string{earnerTag, amountTag} {
				if !slices.Contains(dtoTags, col) {
					t.Fatalf("DTO has no %q field; this test's premise is wrong", col)
				}
				if slices.Contains(detailTags, col) {
					t.Errorf("detail carries %q, which is a real column", col)
				}
			}

			var want []string
			for _, tag := range dtoTags {
				if tag != earnerTag && tag != amountTag {
					want = append(want, tag)
				}
			}
			got := slices.DeleteFunc(slices.Clone(detailTags), func(tag string) bool {
				return slices.Contains(tc.extra, tag)
			})
			if !slices.Equal(got, want) {
				t.Errorf("detail keys %v, want %v (every DTO field must be a column or a detail key)", got, want)
			}
		})
	}
}

// jsonTags returns a struct's json tag names in declaration order.
func jsonTags(t *testing.T, v any) []string {
	t.Helper()
	rt := reflect.TypeOf(v)
	if rt.Kind() != reflect.Struct {
		t.Fatalf("want a struct, got %s", rt.Kind())
	}
	out := make([]string, 0, rt.NumField())
	for i := range rt.NumField() {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Fatalf("%s.%s has no json tag; it would not persist", rt.Name(), rt.Field(i).Name)
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	return out
}

// The mappers' output has to survive the real write path, not just the shared
// validator. Routing it through the copy source exercises the code that
// actually builds the row pgx sends.
func TestMappedResultsSurviveTheWritePath(t *testing.T) {
	earner := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	source := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	unilevel, err := ResultFromCommissionEarning(CommissionEarningDTO{
		EarnerID: earner.String(), SourceID: source.String(), Level: 1, Rate: 0.1, CVAmount: 10, DollarAmount: 1,
	})
	if err != nil {
		t.Fatalf("commission earning: %v", err)
	}
	binary, err := ResultFromBinaryEarning(BinaryCommissionEarningDTO{
		EarnerID: earner.String(), PositionID: source.String(), DollarAmount: 2,
	})
	if err != nil {
		t.Fatalf("binary: %v", err)
	}
	board, err := ResultFromBoardCycleEarning(BoardCycleEarningDTO{
		EarnerID: earner.String(), BoardID: source.String(), DollarAmount: 3, CycleNumber: 1,
	})
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	all := []CommissionResultInput{unilevel, binary, board}

	if err := validateResultInputs("primary", all); err != nil {
		t.Fatalf("mapper output rejected by the store's validator: %v", err)
	}

	src := newCommissionResultCopySource(uuid.New(), "primary", all)
	for i := range all {
		if !src.Next() {
			t.Fatalf("copy source stopped at row %d of %d", i, len(all))
		}
		vals, err := src.Values()
		if err != nil {
			t.Fatalf("row %d rejected by the copy source: %v", i, err)
		}
		if len(vals) != 5 {
			t.Fatalf("row %d produced %d values, want 5 columns", i, len(vals))
		}
	}
	if src.Next() {
		t.Fatal("copy source yielded more rows than it was given")
	}
}

func TestResultMappersRejectABadEarnerID(t *testing.T) {
	if _, err := ResultFromCommissionEarning(CommissionEarningDTO{EarnerID: "not-a-uuid"}); err == nil {
		t.Fatal("expected an error for a malformed earner id")
	}
	if _, err := ResultFromBinaryEarning(BinaryCommissionEarningDTO{EarnerID: ""}); err == nil {
		t.Fatal("expected an error for an empty earner id")
	}
	if _, err := ResultFromBoardCycleEarning(BoardCycleEarningDTO{EarnerID: "nope"}); err == nil {
		t.Fatal("expected an error for a malformed earner id")
	}
}

// dollar_amount gets three explicit finite checks. A non-finite value in a
// detail field is caught only by json.Marshal refusing it, which is real but
// incidental, so it is pinned here — and the error has to name the earner, or
// a failure in a 500k-row batch has no starting point.
func TestNonFiniteDetailFieldIsRejectedWithTheEarner(t *testing.T) {
	earner := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	cases := []struct {
		name  string
		value float64
	}{
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResultFromCommissionEarning(CommissionEarningDTO{
				EarnerID: earner.String(), Rate: tc.value, DollarAmount: 1,
			})
			if err == nil {
				t.Fatal("expected a non-finite detail field to be rejected")
			}
			if !strings.Contains(err.Error(), earner.String()) {
				t.Fatalf("error should name the earner, got %q", err.Error())
			}
		})
	}
}

// uuid.Parse accepts the all-zero UUID, so a mapper cannot catch it. The
// store does, at the write. Pinned so the division of labor is deliberate
// rather than an oversight: a nil earner fails loudly, just one layer later.
func TestNilEarnerIDSurvivesMappingAndIsCaughtByTheStore(t *testing.T) {
	got, err := ResultFromCommissionEarning(CommissionEarningDTO{
		EarnerID: uuid.Nil.String(), DollarAmount: 1,
	})
	if err != nil {
		t.Fatalf("mapping a nil earner id should succeed; the store is the gate: %v", err)
	}
	if got.EarnerID != uuid.Nil {
		t.Fatalf("EarnerID = %s, want the nil UUID", got.EarnerID)
	}
	if err := validateResultInputs("primary", []CommissionResultInput{got}); err == nil {
		t.Fatal("the store must reject a nil earner id")
	}
}
