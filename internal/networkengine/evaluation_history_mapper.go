package networkengine

import (
	"fmt"

	"github.com/google/uuid"
)

// evaluationResultToHistoryEntries maps an EvaluationResultDTO into the
// write-shape consumed by QualificationHistoryStore.SaveResult.
//
// Qualified entries become (Rank, Ordinal) pointers. Unranked entries
// become (nil, nil). Unknown Kind values return an error so silent
// data corruption is impossible.
func evaluationResultToHistoryEntries(r *EvaluationResultDTO) ([]QualificationHistoryEntry, error) {
	if r == nil {
		return nil, nil
	}
	out := make([]QualificationHistoryEntry, 0, len(r.Ranks))
	for id, ev := range r.Ranks {
		userID, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("parse user_id %q: %w", id, err)
		}
		switch ev.Kind {
		case "qualified":
			if ev.Rank == "" {
				return nil, fmt.Errorf("qualified rank for user %s has empty rank name", id)
			}
			if ev.Ordinal == 0 {
				return nil, fmt.Errorf("qualified rank for user %s has zero ordinal", id)
			}
			rank := ev.Rank
			ord := ev.Ordinal
			out = append(out, QualificationHistoryEntry{
				UserID:  userID,
				Rank:    &rank,
				Ordinal: &ord,
			})
		case "unranked":
			out = append(out, QualificationHistoryEntry{
				UserID:  userID,
				Rank:    nil,
				Ordinal: nil,
			})
		default:
			return nil, fmt.Errorf("unknown rank kind %q for user %s", ev.Kind, id)
		}
	}
	return out, nil
}
