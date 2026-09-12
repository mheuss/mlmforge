package networkengine

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// detailVersion tags every stored detail object. It costs one key and means a
// future shape change does not require guessing what old rows meant.
//
// It is shared across all three shapes, so bumping it relabels rows whose
// shape did not change. That is tolerable only because kind identifies the
// shape independently — a reader keys off kind first, then version.
//
// Bumped to 2 by HEU-641, which added `walk` to commission_earning. The
// version has to move for that field to be readable: `walk` is serialized
// even when null, precisely so an unrecorded walk stays distinct from a row
// written before the field existed. Without a version bump those two are the
// same bytes and the distinction the field was designed for is unrecoverable.
//
// Relabelling binary_pairing and board_cycle costs nothing today. Nothing in
// production constructs a CommissionResultInput yet, so no v1 row exists
// outside test fixtures. That is why this is a one-line change now and an
// unfixable one after HEU-46 starts writing rows.
//
// Bumped to 3 by HEU-699, which made `rate` nullable on commission_earning.
// A v2 row always carried a number there. Without the bump, a reader has no
// way to know which promise the row was written under.
//
// Not the NDJSON protocol version, and not the plan schema version. This one
// labels a stored detail shape. The protocol version is the engine wire
// contract in engine/network-engine-worker/src/protocol.rs. The plan schema
// version is the authoring format in schemas/compensation-plan.schema.json.
// None of them is coupled to the others, and this one asserts nothing about
// the rest.
const detailVersion = 3

// The kind values stored in every detail object. A version alone cannot say
// which shape a row is, and the structure column cannot either: it holds a
// user-supplied structure name ("primary", "leader-board"), and five
// calculators share one shape, so no name maps one-to-one. Without kind a
// reader has to sniff key sets, which is exactly the guessing the version key
// exists to prevent.
//
// This matches carry_forward, the sibling persisted format in this ticket,
// which tags its entries "binary_legs" and "board_cycles".
//
// These strings are persisted. Changing one orphans every row that carries
// the old value.
const (
	kindCommissionEarning = "commission_earning"
	kindBinaryPairing     = "binary_pairing"
	kindBoardCycle        = "board_cycle"
)

// The three detail shapes. Each mirrors its wire DTO minus earner_id and
// dollar_amount, which are real columns on commission_results. Repeating them
// here would store the same fact twice and let the two disagree.
//
// Field order here is the order encoding/json emits, which is what the
// testdata/commission_detail/*.json fixtures pin. It is NOT the order the
// bytes sit in the table: jsonb parses to a binary form and re-emits keys
// sorted by length then bytewise, so a row read back from Postgres has a
// different key order and different whitespace. The fixtures still catch what
// matters — a renamed, added, dropped, or retyped field — but do not compare
// them against a SELECT.
//
// Numbers survive that trip exactly. jsonb stores them as numeric, so a
// float64 written here reads back bit-identical even though Postgres may
// reformat the text (1e21 becomes 1000000000000000000000).

// commissionEarningDetail is the shape for calculate_unilevel,
// calculate_matrix, calculate_stairstep, calculate_generation, and
// calculate_streamline. Named after the DTO rather than after unilevel,
// because four of its five producers are not unilevel.
type commissionEarningDetail struct {
	V        int    `json:"v"`
	Kind     string `json:"kind"`
	SourceID string `json:"source_id"`
	Level    int    `json:"level"`
	// Null means no rate was applied, not a zero rate. These rows are retained
	// for years to settle disputes, so the two must stay distinguishable.
	//
	// No omitempty, for the same reason given on Walk below.
	Rate     *float64 `json:"rate"`
	CVAmount float64  `json:"cv_amount"`
	// Index of the walk that produced this earning, into the walks array of
	// the same response. Null where none was recorded, which today means
	// stairstep Walk 2: design 029 excludes that traversal, so the null is a
	// recorded gap rather than missing data.
	//
	// No omitempty. A dropped key would make an unrecorded walk look like a
	// row written before the field existed, and these rows are retained for
	// years to settle disputes.
	Walk *uint32 `json:"walk"`
}

// binaryPairingDetail is the shape for calculate_binary_pairing.
//
// Known limit: it cannot say which pairing mode produced the row. Cycle-step
// reports ratio and percent as 0.0 (see BinaryCommissionEarningDTO), so the
// mode is inferable but not stated, and inference from an undocumented
// invariant is thin ground for a row retained for years to settle disputes.
// Fixing it needs the engine to emit the mode; tracked on the HEU-555 plan.
type binaryPairingDetail struct {
	V             int     `json:"v"`
	Kind          string  `json:"kind"`
	PositionID    string  `json:"position_id"`
	LeftVolume    float64 `json:"left_volume"`
	RightVolume   float64 `json:"right_volume"`
	MatchedVolume float64 `json:"matched_volume"`
	Ratio         float64 `json:"ratio"`
	Percent       float64 `json:"percent"`
	Capped        bool    `json:"capped"`
}

// boardCycleDetail is the shape for board_calculate_commissions.
type boardCycleDetail struct {
	V       int    `json:"v"`
	Kind    string `json:"kind"`
	BoardID string `json:"board_id"`
	// int64, not int: int is 32 bits on some targets and this holds a u32.
	CycleNumber int64 `json:"cycle_number"`
	Capped      bool  `json:"capped"`
}

// ResultFromCommissionEarning maps the shape returned by calculate_unilevel,
// calculate_matrix, calculate_stairstep, calculate_generation, and
// calculate_streamline.
func ResultFromCommissionEarning(e CommissionEarningDTO) (CommissionResultInput, error) {
	return newResultInput(e.EarnerID, e.DollarAmount, commissionEarningDetail{
		V:        detailVersion,
		Kind:     kindCommissionEarning,
		SourceID: e.SourceID,
		Level:    int(e.Level),
		Rate:     e.Rate,
		CVAmount: e.CVAmount,
		Walk:     e.Walk,
	})
}

// ResultFromBinaryEarning maps the shape returned by
// calculate_binary_pairing, including cycle-step mode.
func ResultFromBinaryEarning(e BinaryCommissionEarningDTO) (CommissionResultInput, error) {
	return newResultInput(e.EarnerID, e.DollarAmount, binaryPairingDetail{
		V:             detailVersion,
		Kind:          kindBinaryPairing,
		PositionID:    e.PositionID,
		LeftVolume:    e.LeftVolume,
		RightVolume:   e.RightVolume,
		MatchedVolume: e.MatchedVolume,
		Ratio:         e.Ratio,
		Percent:       e.Percent,
		Capped:        e.Capped,
	})
}

// ResultFromBoardCycleEarning maps the shape returned by
// board_calculate_commissions.
func ResultFromBoardCycleEarning(e BoardCycleEarningDTO) (CommissionResultInput, error) {
	return newResultInput(e.EarnerID, e.DollarAmount, boardCycleDetail{
		V:           detailVersion,
		Kind:        kindBoardCycle,
		BoardID:     e.BoardID,
		CycleNumber: int64(e.CycleNumber),
		Capped:      e.Capped,
	})
}

// newResultInput is the shared tail of all three mappers: parse the earner,
// marshal the shape, assemble the row. The shape structs stay separate and
// explicit because they are the persisted format; only these four mechanical
// steps are shared, so a fix lands once rather than three times.
func newResultInput(earnerID string, amount float64, shape any) (CommissionResultInput, error) {
	earner, err := parseEarnerID(earnerID)
	if err != nil {
		return CommissionResultInput{}, err
	}
	// This is also where a non-finite float in a detail field is caught:
	// json.Marshal refuses NaN and infinities. dollar_amount gets three
	// explicit guards, so name the earner here rather than surfacing a bare
	// "unsupported value: NaN" with nothing to trace it to.
	detail, err := json.Marshal(shape)
	if err != nil {
		return CommissionResultInput{}, fmt.Errorf("earner %s: marshal %T: %w", earner, shape, err)
	}
	return CommissionResultInput{
		EarnerID:     earner,
		DollarAmount: amount,
		Detail:       detail,
	}, nil
}

// parseEarnerID converts the wire DTO's string id to the uuid the results
// table stores. The DTOs carry strings because that is the NDJSON shape; the
// column is UUID, so the conversion has to happen somewhere and a named
// failure here beats a driver error later.
//
// It accepts the all-zero UUID, which uuid.Parse treats as valid. The store's
// validateResultInputs rejects it at the write. Catching it here as well
// would be tighter, but the mapper's job is conversion and the store is
// already the single gate every write path passes through.
//
// The other ids — source_id, position_id, board_id — are deliberately not
// parsed. They land in detail as opaque strings, so a malformed one persists
// rather than failing. A dispute query joining detail->>'source_id' against a
// distributor table is reading unchecked text; validating them would mean
// deciding what an unparseable source id should do to a whole batch, and that
// belongs with the caller that has the context to answer it.
func parseEarnerID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("earner id %q: %w", s, err)
	}
	return id, nil
}
