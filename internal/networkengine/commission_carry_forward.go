package networkengine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// encoding/json decodes a lone surrogate escape to U+FFFD with no error.
	if strings.ContainsRune(name, utf8.RuneError) {
		return fmt.Errorf("carry_forward: structure %q contains U+FFFD", name)
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
	if id == uuid.Nil {
		return fmt.Errorf("carry_forward: structure %q: key %q is the nil UUID", structure, key)
	}
	return nil
}

// ParseCarryForward reads a stored carry_forward envelope back into binary
// pairing carry-forward, keyed by structure name.
func ParseCarryForward(raw json.RawMessage) (map[string]map[string]LegVolumesDTO, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// encoding/json replaces invalid UTF-8 with U+FFFD and reports no error,
	// which would hide a bad structure name from the name check.
	if !utf8.Valid(raw) {
		return nil, errors.New("carry_forward: envelope is not valid UTF-8")
	}
	envelope, err := decodeCarryObject(raw, "envelope")
	if err != nil {
		return nil, err
	}
	vRaw, ok := envelope["v"]
	if !ok || isJSONNull(vRaw) {
		return nil, errors.New(`carry_forward: envelope field "v" is missing or null`)
	}
	var v int
	if err := json.Unmarshal(vRaw, &v); err != nil {
		return nil, fmt.Errorf("carry_forward: v is not an integer: %w", err)
	}
	switch v {
	case 1:
		return parseCarryForwardV1(envelope)
	default:
		return nil, fmt.Errorf("carry_forward: version %d has no decoder", v)
	}
}

// parseCarryForwardV1 reads a version 1 envelope.
func parseCarryForwardV1(envelope map[string]json.RawMessage) (map[string]map[string]LegVolumesDTO, error) {
	if err := requireCarryFields(envelope, "envelope", "v", "structures"); err != nil {
		return nil, err
	}
	structures, err := decodeCarryObject(envelope["structures"], "structures")
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]LegVolumesDTO, len(structures))
	for _, name := range slices.Sorted(maps.Keys(structures)) {
		if err := checkCarryStructureName(name); err != nil {
			return nil, err
		}
		legs, err := parseBinaryLegsEntry(name, structures[name])
		if err != nil {
			return nil, err
		}
		out[name] = legs
	}
	return out, nil
}

// parseBinaryLegsEntry reads one structure entry, which must be binary_legs.
func parseBinaryLegsEntry(name string, raw json.RawMessage) (map[string]LegVolumesDTO, error) {
	what := fmt.Sprintf("structure %q", name)
	entry, err := decodeCarryObject(raw, what)
	if err != nil {
		return nil, err
	}
	// Kind is read before the field check, so a board_cycles entry reports
	// its kind rather than its counts field.
	kindRaw, ok := entry["kind"]
	if !ok || isJSONNull(kindRaw) {
		return nil, fmt.Errorf("carry_forward: %s has no kind", what)
	}
	var kind string
	if err := json.Unmarshal(kindRaw, &kind); err != nil {
		return nil, fmt.Errorf("carry_forward: %s kind is not a string: %w", what, err)
	}
	if kind != kindBinaryLegs {
		return nil, fmt.Errorf("carry_forward: %s has kind %q, which has no decoder", what, kind)
	}
	if err := requireCarryFields(entry, what, "kind", "carry"); err != nil {
		return nil, err
	}
	carry, err := decodeCarryObject(entry["carry"], what+" carry")
	if err != nil {
		return nil, err
	}
	legs := make(map[string]LegVolumesDTO, len(carry))
	for _, key := range slices.Sorted(maps.Keys(carry)) {
		if err := checkCarryKey(name, key); err != nil {
			return nil, err
		}
		leg, err := parseLegVolumes(fmt.Sprintf("%s key %q", what, key), carry[key])
		if err != nil {
			return nil, err
		}
		legs[key] = leg
	}
	return legs, nil
}

// parseLegVolumes reads one stored leg object.
func parseLegVolumes(what string, raw json.RawMessage) (LegVolumesDTO, error) {
	obj, err := decodeCarryObject(raw, what)
	if err != nil {
		return LegVolumesDTO{}, err
	}
	if err := requireCarryFields(obj, what, "left", "right"); err != nil {
		return LegVolumesDTO{}, err
	}
	var leg LegVolumesDTO
	if err := json.Unmarshal(obj["left"], &leg.Left); err != nil {
		return LegVolumesDTO{}, fmt.Errorf("carry_forward: %s left: %w", what, err)
	}
	if err := json.Unmarshal(obj["right"], &leg.Right); err != nil {
		return LegVolumesDTO{}, fmt.Errorf("carry_forward: %s right: %w", what, err)
	}
	return leg, nil
}

// decodeCarryObject decodes exactly one non-null JSON object into its raw
// members, refusing a repeated member name.
func decodeCarryObject(raw json.RawMessage, what string) (map[string]json.RawMessage, error) {
	if isJSONNull(raw) {
		return nil, fmt.Errorf("carry_forward: %s is null, want an object", what)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	open, err := d.Token()
	if err != nil {
		return nil, fmt.Errorf("carry_forward: %s is not a JSON object: %w", what, err)
	}
	if open != json.Delim('{') {
		return nil, fmt.Errorf("carry_forward: %s is not a JSON object", what)
	}
	// Walked member by member because decoding into a map keeps the last of
	// two repeated names and reports no error.
	obj := make(map[string]json.RawMessage)
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("carry_forward: %s is not a JSON object: %w", what, err)
		}
		name, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("carry_forward: %s is not a JSON object", what)
		}
		if _, dup := obj[name]; dup {
			return nil, fmt.Errorf("carry_forward: %s repeats member %q", what, name)
		}
		var val json.RawMessage
		if err := d.Decode(&val); err != nil {
			return nil, fmt.Errorf("carry_forward: %s member %q: %w", what, name, err)
		}
		obj[name] = val
	}
	if _, err := d.Token(); err != nil {
		return nil, fmt.Errorf("carry_forward: %s is not a JSON object: %w", what, err)
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("carry_forward: %s has data after the object", what)
	}
	return obj, nil
}

// requireCarryFields refuses a field outside the named set, and a named field
// that is missing or null.
func requireCarryFields(obj map[string]json.RawMessage, what string, fields ...string) error {
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		if !slices.Contains(fields, key) {
			return fmt.Errorf("carry_forward: %s has unknown field %q", what, key)
		}
	}
	for _, f := range fields {
		if val, ok := obj[f]; !ok || isJSONNull(val) {
			return fmt.Errorf("carry_forward: %s field %q is missing or null", what, f)
		}
	}
	return nil
}

// isJSONNull reports whether raw is the JSON literal null.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
