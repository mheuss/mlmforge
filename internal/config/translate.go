package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// translateToEngine converts a validated CompensationPlan from YAML-shape
// to Rust-ready JSON. Five structural translations are applied:
//
//  1. Structure adjacent tagging: flat type + sibling fields become
//     {"type": "...", "config": {...}}
//  2. Binary mode tagging: mode string + sibling config become
//     {"mode": {"pairing": {...}}} (externally tagged enum)
//  3. Donated placement collapse: bool + string become single optional
//  4. Streamline levels: map keyed by level string becomes sorted vector
//  5. Binary placement key rename: "binary" becomes "binary_placement"
//
// Pass-through sections (period, volume, ranks, bonuses, payout, caps, etc.)
// are marshalled directly using json struct tags.
func translateToEngine(plan *CompensationPlan) ([]byte, error) {
	structures, err := translateStructures(plan.Structures)
	if err != nil {
		return nil, err
	}

	doc := map[string]any{
		"name":                   plan.Name,
		"version":                plan.Version,
		"period":                 plan.Period,
		"volume":                 plan.Volume,
		"ranks":                  plan.Ranks,
		"rank_tracking":          plan.RankTracking,
		"rank_features":          plan.RankFeatures,
		"commission_eligibility": plan.CommissionEligibility,
		"structures":             structures,
		"bonuses":                plan.Bonuses,
		"payout":                 plan.Payout,
		"caps":                   plan.Caps,
		"placement":              translatePlacement(&plan.Placement),
	}

	return json.Marshal(doc)
}

// taggedStructure marshals its fields in declaration order (type before config).
// This is now about byte stability, not correctness: TestConfigContractFixtures
// MatchPipeline byte-compares this output against committed fixtures, so the
// order must not drift. Serde reads the "type" tag first and decodes "config"
// directly, which is the cheaper path, but since HEU-648 the reverse order
// deserializes too. A map[string]any
// marshals keys alphabetically ("config" before "type"), which forces serde to
// buffer the content before it knows the variant — and that buffer fails on
// non-string map keys such as rate_table's u8 keys (HEU-513: contract round-trip).
type taggedStructure struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

// translateStructures converts each structure from flat YAML shape to
// adjacently tagged shape: {"type": "...", "config": {...}}.
func translateStructures(structures []StructureConfig) ([]any, error) {
	result := make([]any, 0, len(structures))
	for _, s := range structures {
		cfg, err := translateStructureConfig(&s)
		if err != nil {
			return nil, fmt.Errorf("structure %q: %w", s.Name, err)
		}
		result = append(result, taggedStructure{Type: s.Type, Config: cfg})
	}
	return result, nil
}

// translateStructureConfig builds the "config" object for a single structure.
// Each structure type has different fields in its config.
func translateStructureConfig(s *StructureConfig) (map[string]any, error) {
	switch s.Type {
	case "unilevel":
		return translateUnilevelConfig(s)
	case "binary":
		return translateBinaryConfig(s)
	case "matrix":
		return translateMatrixConfig(s)
	case "stairstep":
		return translateStairstepConfig(s)
	case "generation":
		return translateGenerationConfig(s)
	case "streamline":
		return translateStreamlineConfig(s)
	case "board_plan":
		return translateBoardPlanConfig(s)
	default:
		return nil, fmt.Errorf("unknown structure type: %s", s.Type)
	}
}

// translateUnilevelConfig builds the config for a unilevel structure.
// Rust expects: name, level_commission, compression, pass_up.
func translateUnilevelConfig(s *StructureConfig) (map[string]any, error) {
	c, ok := s.resolvedCommission.(*UnilevelCommission)
	if !ok {
		return nil, fmt.Errorf("expected *UnilevelCommission, got %T", s.resolvedCommission)
	}
	return map[string]any{
		"name":             s.Name,
		"level_commission": buildLevelCommission(c.BroadCommissionPercent, c.VolumeToDollarMultiplier, c.CommissionableDepth, c.RateTable),
		"compression":      c.Compression,
		"pass_up":          c.PassUp,
	}, nil
}

// translateBinaryConfig builds the config for a binary structure.
// Rust expects: name, binary_commission (with externally tagged mode).
func translateBinaryConfig(s *StructureConfig) (map[string]any, error) {
	c, ok := s.resolvedCommission.(*BinaryCommission)
	if !ok {
		return nil, fmt.Errorf("expected *BinaryCommission, got %T", s.resolvedCommission)
	}
	bc, err := translateBinaryCommission(c)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"name":              s.Name,
		"binary_commission": bc,
	}, nil
}

// translateBinaryCommission converts the binary commission from flat YAML
// format to the Rust externally tagged enum format.
// YAML: mode: "pairing", pairing: {...}
// Rust: mode: {"pairing": {...}}
func translateBinaryCommission(c *BinaryCommission) (map[string]any, error) {
	var modeContent any
	switch c.Mode {
	case "pairing":
		modeContent = c.Pairing
	case "cycle_step":
		if c.CycleStep != nil && c.CycleStep.VolumeAfterCycle == "net_off" {
			return nil, fmt.Errorf("net_off is not supported for cycle_step volume_after_cycle; use full_flush or carry_forward")
		}
		modeContent = c.CycleStep
	default:
		return nil, fmt.Errorf("unknown binary commission mode: %s", c.Mode)
	}

	return map[string]any{
		"volume_to_dollar_multiplier": c.VolumeToDollarMultiplier,
		"mode": map[string]any{
			c.Mode: modeContent,
		},
	}, nil
}

// translateMatrixConfig builds the config for a matrix structure.
// Rust expects: name, matrix_params, level_commission, compression, pruning.
func translateMatrixConfig(s *StructureConfig) (map[string]any, error) {
	c, ok := s.resolvedCommission.(*MatrixCommission)
	if !ok {
		return nil, fmt.Errorf("expected *MatrixCommission, got %T", s.resolvedCommission)
	}
	return map[string]any{
		"name":             s.Name,
		"matrix_params":    s.Structure,
		"level_commission": buildLevelCommission(c.BroadCommissionPercent, c.VolumeToDollarMultiplier, c.CommissionableDepth, c.RateTable),
		"compression":      c.Compression,
		"pruning":          s.Pruning,
	}, nil
}

// translateStairstepConfig builds the config for a stairstep structure.
// Rust expects: name, level_commission, compression, breakaway.
func translateStairstepConfig(s *StructureConfig) (map[string]any, error) {
	c, ok := s.resolvedCommission.(*StairstepCommission)
	if !ok {
		return nil, fmt.Errorf("expected *StairstepCommission, got %T", s.resolvedCommission)
	}
	return map[string]any{
		"name":             s.Name,
		"level_commission": buildLevelCommission(c.BroadCommissionPercent, c.VolumeToDollarMultiplier, c.CommissionableDepth, c.RateTable),
		"compression":      c.Compression,
		"breakaway":        c.Breakaway,
	}, nil
}

// translateGenerationConfig builds the config for a generation structure.
// Rust expects: name, level_commission (optional), compression, generation_commission,
// level_commissions_enabled.
func translateGenerationConfig(s *StructureConfig) (map[string]any, error) {
	c, ok := s.resolvedCommission.(*GenerationCommission)
	if !ok {
		return nil, fmt.Errorf("expected *GenerationCommission, got %T", s.resolvedCommission)
	}

	var levelCommission any
	if c.LevelCommissionsEnabled {
		levelCommission = buildLevelCommission(c.BroadCommissionPercent, c.VolumeToDollarMultiplier, c.CommissionableDepth, c.RateTable)
	}

	return map[string]any{
		"name":                      s.Name,
		"level_commission":          levelCommission,
		"compression":               c.Compression,
		"generation_commission":     c.Generation,
		"level_commissions_enabled": c.LevelCommissionsEnabled,
	}, nil
}

// translateStreamlineConfig builds the config for a streamline structure.
// Rust expects: name, streamline_commission (with dynamic_compression as Vec).
func translateStreamlineConfig(s *StructureConfig) (map[string]any, error) {
	c, ok := s.resolvedCommission.(*StreamlineCommission)
	if !ok {
		return nil, fmt.Errorf("expected *StreamlineCommission, got %T", s.resolvedCommission)
	}
	sc, err := translateStreamlineCommission(c)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"name":                  s.Name,
		"streamline_commission": sc,
	}, nil
}

// translateBoardPlanConfig builds the config for a board plan structure.
// Rust expects: name, width, height, board_cycling.
func translateBoardPlanConfig(s *StructureConfig) (map[string]any, error) {
	c, ok := s.resolvedCommission.(*BoardPlanCommission)
	if !ok {
		return nil, fmt.Errorf("expected *BoardPlanCommission, got %T", s.resolvedCommission)
	}
	if s.Structure == nil {
		return nil, fmt.Errorf("board plan structure %q requires width and height but Structure is nil", s.Name)
	}
	cfg := map[string]any{
		"name":          s.Name,
		"width":         s.Structure.Width,
		"height":        s.Structure.Height,
		"board_cycling": c.BoardCycling,
	}
	return cfg, nil
}

// translateStreamlineCommission converts the streamline commission from
// map-keyed format to sorted vector format.
// YAML: dynamic_compression: {"1": {min_rank: ..., percent: ...}, "2": ...}
// Rust: dynamic_compression: [{level: 1, min_rank: ..., percent: ...}, ...]
func translateStreamlineCommission(c *StreamlineCommission) (map[string]any, error) {
	levels, err := sortStreamlineLevels(c.DynamicCompression)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"volume_to_dollar_multiplier": c.VolumeToDollarMultiplier,
		"commissionable_depth":        c.CommissionableDepth,
		"dynamic_compression":         levels,
		"streams":                     c.Streams,
	}, nil
}

// sortStreamlineLevels converts a map of level-string to StreamlineLevel into
// a sorted slice with the level number included on each entry.
func sortStreamlineLevels(levels map[string]StreamlineLevel) ([]map[string]any, error) {
	if len(levels) == 0 {
		return nil, fmt.Errorf("streamline dynamic_compression must have at least one level")
	}

	type numbered struct {
		level int
		sl    StreamlineLevel
	}

	sorted := make([]numbered, 0, len(levels))
	for k, v := range levels {
		n, err := strconv.Atoi(k)
		if err != nil {
			return nil, fmt.Errorf("dynamic_compression key %q is not a valid level number: %w", k, err)
		}
		if n < 1 || n > 255 {
			return nil, fmt.Errorf("dynamic_compression level %d is out of range [1, 255]", n)
		}
		sorted = append(sorted, numbered{level: n, sl: v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].level < sorted[j].level
	})

	result := make([]map[string]any, 0, len(sorted))
	for _, entry := range sorted {
		result = append(result, map[string]any{
			"level":    entry.level,
			"min_rank": entry.sl.MinRank,
			"percent":  entry.sl.Percent,
		})
	}
	return result, nil
}

// buildLevelCommission creates the Rust-side level_commission object from
// the shared fields that appear in unilevel, matrix, stairstep, and
// generation commission configs.
func buildLevelCommission(broadPercent float64, multiplier *float64, depth uint8, rateTable map[string]map[string]float64) map[string]any {
	return map[string]any{
		"broad_commission_percent":    broadPercent,
		"volume_to_dollar_multiplier": multiplier,
		"commissionable_depth":        depth,
		"rate_table":                  rateTable,
	}
}

// translatePlacement converts the YAML-shape placement to Rust-shape.
// Three translations:
//   - donated_placement_enabled + donated_placement_restriction collapse
//     into a single donated_placement field (Option<enum>)
//   - binary key is renamed to binary_placement
//   - matrix key is dropped (Rust PlacementConfig doesn't have it;
//     matrix placement is configured via matrix_params.spillover_direction)
//
// Precondition: validatePlacement must run before this function. If
// donated_placement_enabled is true but restriction is nil, the output
// will contain donated_placement: null, which is incorrect. The business
// rule in validatePlacement catches this case.
func translatePlacement(p *PlacementConfig) map[string]any {
	var donatedPlacement any
	if p.DonatedPlacementEnabled && p.DonatedPlacementRestriction != nil {
		donatedPlacement = *p.DonatedPlacementRestriction
	}

	result := map[string]any{
		"donated_placement": donatedPlacement,
		"holding_tank":      p.HoldingTank,
		"binary_placement":  p.Binary,
	}

	return result
}
