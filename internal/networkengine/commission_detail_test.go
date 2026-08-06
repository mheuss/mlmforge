package networkengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// goldenDetail reads a stored-shape fixture. Byte equality is the point: a
// renamed field or a reordered struct changes the bytes, and that is exactly
// the drift these fixtures exist to catch.
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
	if want := goldenDetail(t, "unilevel.json"); string(got.Detail) != want {
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
	if got.EarnerID != earner || got.DollarAmount != 30 {
		t.Fatalf("got %+v, want earner %s and amount 30", got, earner)
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
	if got.EarnerID != earner || got.DollarAmount != 25 {
		t.Fatalf("got %+v, want earner %s and amount 25", got, earner)
	}
	if want := goldenDetail(t, "board.json"); string(got.Detail) != want {
		t.Fatalf("detail =\n%s\nwant\n%s", got.Detail, want)
	}
}

// TestDetailNeverRepeatsColumnFields guards the rule that makes the split
// worth having: earner_id and dollar_amount are columns, so repeating them
// inside detail would store the same fact twice and let the two disagree.
func TestDetailNeverRepeatsColumnFields(t *testing.T) {
	for _, name := range []string{"unilevel.json", "binary.json", "board.json"} {
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

// Every shape carries the version tag. Without it a future shape change
// leaves old rows unreadable except by guessing.
func TestDetailCarriesAVersion(t *testing.T) {
	for _, name := range []string{"unilevel.json", "binary.json", "board.json"} {
		t.Run(name, func(t *testing.T) {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(goldenDetail(t, name)), &parsed); err != nil {
				t.Fatalf("fixture is not readable JSON: %v", err)
			}
			if parsed["v"] != float64(detailVersion) {
				t.Fatalf("v = %v, want %d", parsed["v"], detailVersion)
			}
		})
	}
}

// Every mapper's output has to satisfy the store it feeds. validateResultInputs
// and the detail CHECK both require a JSON object, so a mapper emitting
// anything else would be rejected at the write rather than here.
func TestMappedResultsAreAcceptedByTheStore(t *testing.T) {
	earner := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	unilevel, err := ResultFromCommissionEarning(CommissionEarningDTO{
		EarnerID: earner.String(), SourceID: earner.String(), Level: 1, Rate: 0.1, CVAmount: 10, DollarAmount: 1,
	})
	if err != nil {
		t.Fatalf("unilevel: %v", err)
	}
	binary, err := ResultFromBinaryEarning(BinaryCommissionEarningDTO{
		EarnerID: earner.String(), PositionID: earner.String(), DollarAmount: 2,
	})
	if err != nil {
		t.Fatalf("binary: %v", err)
	}
	board, err := ResultFromBoardCycleEarning(BoardCycleEarningDTO{
		EarnerID: earner.String(), BoardID: earner.String(), DollarAmount: 3, CycleNumber: 1,
	})
	if err != nil {
		t.Fatalf("board: %v", err)
	}

	all := []CommissionResultInput{unilevel, binary, board}
	if err := validateResultInputs("primary", all); err != nil {
		t.Fatalf("mapper output rejected by the store's validator: %v", err)
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
