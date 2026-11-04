package networkengine

import (
	"encoding/json"
	"fmt"

	"github.com/mlmforge/mlmforge/internal/dec"
)

// carryForwardVersion tags every stored carry_forward envelope.
const carryForwardVersion = 1

// kindBinaryLegs tags a structure entry that holds binary leg volumes.
//
// The string is persisted. Changing it orphans every row that carries the old
// value.
const kindBinaryLegs = "binary_legs"

// carryForwardEnvelope is the stored carry_forward object.
type carryForwardEnvelope struct {
	V          int                        `json:"v"`
	Structures map[string]binaryLegsCarry `json:"structures"`
}

// binaryLegsCarry is one binary structure's carried leg volumes, keyed by
// position UUID.
type binaryLegsCarry struct {
	Kind  string                     `json:"kind"`
	Carry map[string]legVolumesCarry `json:"carry"`
}

// legVolumesCarry is the stored form of one position's leg volumes.
type legVolumesCarry struct {
	Left  dec.Amount `json:"left"`
	Right dec.Amount `json:"right"`
}

// CarryForwardFrom builds the stored carry_forward envelope from binary pairing
// carry-forward, keyed by structure name.
func CarryForwardFrom(binary map[string]map[string]LegVolumesDTO) (json.RawMessage, error) {
	if len(binary) == 0 {
		return nil, nil
	}
	env := carryForwardEnvelope{
		V:          carryForwardVersion,
		Structures: make(map[string]binaryLegsCarry, len(binary)),
	}
	for name, legs := range binary {
		carry := make(map[string]legVolumesCarry, len(legs))
		for key, leg := range legs {
			carry[key] = legVolumesCarry{Left: leg.Left, Right: leg.Right}
		}
		env.Structures[name] = binaryLegsCarry{Kind: kindBinaryLegs, Carry: carry}
	}
	out, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("carry_forward: marshal: %w", err)
	}
	return out, nil
}
