package networkengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
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
	for _, name := range slices.Sorted(maps.Keys(binary)) {
		if err := checkCarryStructureName(name); err != nil {
			return nil, err
		}
		legs := binary[name]
		carry := make(map[string]legVolumesCarry, len(legs))
		for _, key := range slices.Sorted(maps.Keys(legs)) {
			if err := checkCarryKey(name, key); err != nil {
				return nil, err
			}
			carry[key] = legVolumesCarry{Left: legs[key].Left, Right: legs[key].Right}
		}
		env.Structures[name] = binaryLegsCarry{Kind: kindBinaryLegs, Carry: carry}
	}
	out, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("carry_forward: marshal: %w", err)
	}
	return out, nil
}

// checkCarryStructureName refuses a structure name the envelope cannot carry
// intact.
func checkCarryStructureName(name string) error {
	if name == "" {
		return errors.New("carry_forward: structure name must be non-empty")
	}
	// json.Marshal rewrites invalid UTF-8, and jsonb refuses a \u0000 escape.
	if !utf8.ValidString(name) {
		return fmt.Errorf("carry_forward: structure %q is not valid UTF-8", name)
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("carry_forward: structure %q contains a NUL byte", name)
	}
	return nil
}

// checkCarryKey refuses a carry key that is not a canonical UUID.
func checkCarryKey(structure, key string) error {
	id, err := uuid.Parse(key)
	if err != nil {
		return fmt.Errorf("carry_forward: structure %q: key %q is not a UUID: %w", structure, key, err)
	}
	if id.String() != key {
		return fmt.Errorf("carry_forward: structure %q: key %q is not canonical, want %q", structure, key, id.String())
	}
	return nil
}
