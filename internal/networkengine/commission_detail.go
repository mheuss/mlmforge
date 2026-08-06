package networkengine

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// detailVersion tags every stored detail object. It costs one key and means a
// future shape change does not require guessing what old rows meant.
const detailVersion = 1

// The three detail shapes. Each mirrors its wire DTO minus earner_id and
// dollar_amount, which are real columns on commission_results. Repeating them
// here would store the same fact twice and let the two disagree.
//
// Field order is the stored byte order, because encoding/json emits struct
// fields in declaration order. Reordering these changes every future row's
// bytes, which is why testdata/commission_detail/*.json pins them.

type unilevelDetail struct {
	V        int     `json:"v"`
	SourceID string  `json:"source_id"`
	Level    int     `json:"level"`
	Rate     float64 `json:"rate"`
	CVAmount float64 `json:"cv_amount"`
}

type binaryDetail struct {
	V             int     `json:"v"`
	PositionID    string  `json:"position_id"`
	LeftVolume    float64 `json:"left_volume"`
	RightVolume   float64 `json:"right_volume"`
	MatchedVolume float64 `json:"matched_volume"`
	Ratio         float64 `json:"ratio"`
	Percent       float64 `json:"percent"`
	Capped        bool    `json:"capped"`
}

type boardDetail struct {
	V           int    `json:"v"`
	BoardID     string `json:"board_id"`
	CycleNumber int    `json:"cycle_number"`
	Capped      bool   `json:"capped"`
}

// ResultFromCommissionEarning maps the shape returned by calculate_unilevel,
// calculate_matrix, calculate_stairstep, calculate_generation, and
// calculate_streamline.
func ResultFromCommissionEarning(e CommissionEarningDTO) (CommissionResultInput, error) {
	earner, err := parseEarnerID(e.EarnerID)
	if err != nil {
		return CommissionResultInput{}, err
	}
	detail, err := json.Marshal(unilevelDetail{
		V:        detailVersion,
		SourceID: e.SourceID,
		Level:    e.Level,
		Rate:     e.Rate,
		CVAmount: e.CVAmount,
	})
	if err != nil {
		return CommissionResultInput{}, fmt.Errorf("marshal unilevel detail: %w", err)
	}
	return CommissionResultInput{
		EarnerID:     earner,
		DollarAmount: e.DollarAmount,
		Detail:       detail,
	}, nil
}

// ResultFromBinaryEarning maps the shape returned by
// calculate_binary_pairing, including cycle-step mode.
func ResultFromBinaryEarning(e BinaryCommissionEarningDTO) (CommissionResultInput, error) {
	earner, err := parseEarnerID(e.EarnerID)
	if err != nil {
		return CommissionResultInput{}, err
	}
	detail, err := json.Marshal(binaryDetail{
		V:             detailVersion,
		PositionID:    e.PositionID,
		LeftVolume:    e.LeftVolume,
		RightVolume:   e.RightVolume,
		MatchedVolume: e.MatchedVolume,
		Ratio:         e.Ratio,
		Percent:       e.Percent,
		Capped:        e.Capped,
	})
	if err != nil {
		return CommissionResultInput{}, fmt.Errorf("marshal binary detail: %w", err)
	}
	return CommissionResultInput{
		EarnerID:     earner,
		DollarAmount: e.DollarAmount,
		Detail:       detail,
	}, nil
}

// ResultFromBoardCycleEarning maps the shape returned by
// board_calculate_commissions.
func ResultFromBoardCycleEarning(e BoardCycleEarningDTO) (CommissionResultInput, error) {
	earner, err := parseEarnerID(e.EarnerID)
	if err != nil {
		return CommissionResultInput{}, err
	}
	detail, err := json.Marshal(boardDetail{
		V:           detailVersion,
		BoardID:     e.BoardID,
		CycleNumber: e.CycleNumber,
		Capped:      e.Capped,
	})
	if err != nil {
		return CommissionResultInput{}, fmt.Errorf("marshal board detail: %w", err)
	}
	return CommissionResultInput{
		EarnerID:     earner,
		DollarAmount: e.DollarAmount,
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
func parseEarnerID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("earner id %q: %w", s, err)
	}
	return id, nil
}
