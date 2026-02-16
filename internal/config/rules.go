package config

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// validateBusinessRules runs all business-rule checks against a parsed
// CompensationPlan. Returns a slice of ValidationError. An empty slice
// means the plan passed all checks.
func validateBusinessRules(plan *CompensationPlan) []ValidationError {
	var errs []ValidationError
	errs = append(errs, validateRanks(plan)...)
	errs = append(errs, validateStructureRefs(plan)...)
	errs = append(errs, validateBonuses(plan)...)
	errs = append(errs, validatePlacement(plan)...)
	errs = append(errs, validateEligibility(plan)...)
	errs = append(errs, validateWarnings(plan)...)
	return errs
}

// --- Helpers ---

// structureNames returns a set of defined structure names for fast lookup.
func structureNames(plan *CompensationPlan) map[string]bool {
	names := make(map[string]bool, len(plan.Structures))
	for _, s := range plan.Structures {
		names[s.Name] = true
	}
	return names
}

// rankNames returns a set of defined rank names for fast lookup.
func rankNames(plan *CompensationPlan) map[string]bool {
	names := make(map[string]bool, len(plan.Ranks))
	for _, r := range plan.Ranks {
		names[r.Name] = true
	}
	return names
}

// rankOrdinalMap returns a map from rank name to ordinal.
func rankOrdinalMap(plan *CompensationPlan) map[string]int {
	m := make(map[string]int, len(plan.Ranks))
	for _, r := range plan.Ranks {
		m[r.Name] = r.Ordinal
	}
	return m
}

// --- Rank rules ---

// validateRanks checks rank ordinals, structure references, and cross-field
// constraints within rank definitions.
func validateRanks(plan *CompensationPlan) []ValidationError {
	var errs []ValidationError
	structs := structureNames(plan)
	ordinals := rankOrdinalMap(plan)

	// Ordinals must be strictly ascending with no duplicates.
	seen := make(map[int]string, len(plan.Ranks))
	prevOrdinal := -1
	for i, r := range plan.Ranks {
		if existing, dup := seen[r.Ordinal]; dup {
			errs = append(errs, ValidationError{
				Path:     fmt.Sprintf("/ranks/%d/ordinal", i),
				Code:     "ordering_violation",
				Message:  fmt.Sprintf("rank %q has duplicate ordinal %d (same as %q)", r.Name, r.Ordinal, existing),
				Severity: SeverityError,
			})
		} else if r.Ordinal <= prevOrdinal {
			errs = append(errs, ValidationError{
				Path:     fmt.Sprintf("/ranks/%d/ordinal", i),
				Code:     "ordering_violation",
				Message:  fmt.Sprintf("rank %q ordinal %d is not ascending (previous was %d)", r.Name, r.Ordinal, prevOrdinal),
				Severity: SeverityError,
			})
		}
		seen[r.Ordinal] = r.Name
		if r.Ordinal > prevOrdinal {
			prevOrdinal = r.Ordinal
		}

		// qualified_structures must reference defined structures.
		for j, qs := range r.QualifiedStructures {
			if !structs[qs] {
				errs = append(errs, ValidationError{
					Path:     fmt.Sprintf("/ranks/%d/qualified_structures/%d", i, j),
					Code:     "undefined_reference",
					Message:  fmt.Sprintf("rank %q references undefined structure %q in qualified_structures", r.Name, qs),
					Severity: SeverityError,
				})
			}
		}

		// qualification.structures[].structure must reference defined structures.
		for j, sq := range r.Qualification.Structures {
			if !structs[sq.Structure] {
				errs = append(errs, ValidationError{
					Path:     fmt.Sprintf("/ranks/%d/qualification/structures/%d/structure", i, j),
					Code:     "undefined_reference",
					Message:  fmt.Sprintf("rank %q qualification references undefined structure %q", r.Name, sq.Structure),
					Severity: SeverityError,
				})
			}

			// max_group_volume_per_leg must not exceed group_volume when both are set.
			if sq.MaxGroupVolumePerLeg > 0 && sq.GroupVolume > 0 && sq.MaxGroupVolumePerLeg > sq.GroupVolume {
				errs = append(errs, ValidationError{
					Path:     fmt.Sprintf("/ranks/%d/qualification/structures/%d/max_group_volume_per_leg", i, j),
					Code:     "cross_field_dependency",
					Message:  fmt.Sprintf("rank %q max_group_volume_per_leg (%.0f) exceeds group_volume (%.0f)", r.Name, sq.MaxGroupVolumePerLeg, sq.GroupVolume),
					Severity: SeverityError,
				})
			}

			// distributor_count.min_rank must reference a lower-ordinal rank.
			if sq.DistributorCount != nil && sq.DistributorCount.MinRank != "" {
				refOrd, exists := ordinals[sq.DistributorCount.MinRank]
				if !exists {
					errs = append(errs, ValidationError{
						Path:     fmt.Sprintf("/ranks/%d/qualification/structures/%d/distributor_count/min_rank", i, j),
						Code:     "undefined_reference",
						Message:  fmt.Sprintf("rank %q distributor_count references undefined rank %q", r.Name, sq.DistributorCount.MinRank),
						Severity: SeverityError,
					})
				} else if refOrd >= r.Ordinal {
					errs = append(errs, ValidationError{
						Path:     fmt.Sprintf("/ranks/%d/qualification/structures/%d/distributor_count/min_rank", i, j),
						Code:     "ordering_violation",
						Message:  fmt.Sprintf("rank %q distributor_count.min_rank %q must be lower ordinal than %d", r.Name, sq.DistributorCount.MinRank, r.Ordinal),
						Severity: SeverityError,
					})
				}
			}
		}
	}

	return errs
}

// validateStructureRefs checks cross-references within structure definitions,
// including boundary_rank references and binary pairing constraints.
func validateStructureRefs(plan *CompensationPlan) []ValidationError {
	var errs []ValidationError
	ranks := rankNames(plan)

	for i, s := range plan.Structures {
		switch rc := s.resolvedCommission.(type) {
		case *GenerationCommission:
			if rc.Generation.BoundaryRank != "" && !ranks[rc.Generation.BoundaryRank] {
				errs = append(errs, ValidationError{
					Path:     fmt.Sprintf("/structures/%d/commission/generation/boundary_rank", i),
					Code:     "undefined_reference",
					Message:  fmt.Sprintf("structure %q generation boundary_rank references undefined rank %q", s.Name, rc.Generation.BoundaryRank),
					Severity: SeverityError,
				})
			}
		case *StairstepCommission:
			if rc.Breakaway != nil {
				if rc.Breakaway.ThresholdRank != "" && !ranks[rc.Breakaway.ThresholdRank] {
					errs = append(errs, ValidationError{
						Path:     fmt.Sprintf("/structures/%d/commission/breakaway/threshold_rank", i),
						Code:     "undefined_reference",
						Message:  fmt.Sprintf("structure %q breakaway threshold_rank references undefined rank %q", s.Name, rc.Breakaway.ThresholdRank),
						Severity: SeverityError,
					})
				}
				if rc.Breakaway.Differential != nil {
					for rankName := range rc.Breakaway.Differential.RankRates {
						if !ranks[rankName] {
							errs = append(errs, ValidationError{
								Path:     fmt.Sprintf("/structures/%d/commission/breakaway/differential/rank_rates", i),
								Code:     "undefined_reference",
								Message:  fmt.Sprintf("structure %q breakaway differential rank_rates references undefined rank %q", s.Name, rankName),
								Severity: SeverityError,
							})
						}
					}
				}
				if rc.Breakaway.Generation != nil && rc.Breakaway.Generation.BoundaryRank != "" {
					if !ranks[rc.Breakaway.Generation.BoundaryRank] {
						errs = append(errs, ValidationError{
							Path:     fmt.Sprintf("/structures/%d/commission/breakaway/generation/boundary_rank", i),
							Code:     "undefined_reference",
							Message:  fmt.Sprintf("structure %q breakaway generation boundary_rank references undefined rank %q", s.Name, rc.Breakaway.Generation.BoundaryRank),
							Severity: SeverityError,
						})
					}
				}
			}
		case *StreamlineCommission:
			for level, sl := range rc.DynamicCompression {
				if sl.MinRank != "" && !ranks[sl.MinRank] {
					errs = append(errs, ValidationError{
						Path:     fmt.Sprintf("/structures/%d/commission/dynamic_compression/%s/min_rank", i, level),
						Code:     "undefined_reference",
						Message:  fmt.Sprintf("structure %q dynamic_compression level %s references undefined rank %q", s.Name, level, sl.MinRank),
						Severity: SeverityError,
					})
				}
			}
		case *BinaryCommission:
			if rc.Pairing != nil && rc.Pairing.CarryForwardCap != nil && rc.Pairing.VolumeAfterPayout != "carry_forward" {
				errs = append(errs, ValidationError{
					Path:     fmt.Sprintf("/structures/%d/commission/pairing/carry_forward_cap", i),
					Code:     "cross_field_dependency",
					Message:  fmt.Sprintf("structure %q has carry_forward_cap set but volume_after_payout is %q, not \"carry_forward\"", s.Name, rc.Pairing.VolumeAfterPayout),
					Severity: SeverityError,
				})
			}
		}
	}

	return errs
}

// --- Bonus rules ---

// validateBonuses checks bonus program configuration for referential integrity
// and cross-section dependencies.
func validateBonuses(plan *CompensationPlan) []ValidationError {
	var errs []ValidationError
	ranks := rankNames(plan)

	// pay_once_only requires track_achieved_rank.
	if plan.Bonuses.RankAdvancement != nil && plan.Bonuses.RankAdvancement.PayOnceOnly && !plan.RankTracking.TrackAchievedRank {
		errs = append(errs, ValidationError{
			Path:     "/bonuses/rank_advancement/pay_once_only",
			Code:     "cross_section_dependency",
			Message:  "pay_once_only requires rank_tracking.track_achieved_rank to be true",
			Severity: SeverityError,
		})
	}

	// matched_commission_types must reference structure types in the plan.
	if plan.Bonuses.Matching != nil {
		structureTypes := make(map[string]bool, len(plan.Structures))
		for _, s := range plan.Structures {
			structureTypes[s.Type] = true
		}
		for _, ct := range plan.Bonuses.Matching.MatchedCommissionTypes {
			if !structureTypes[ct] {
				errs = append(errs, ValidationError{
					Path:     "/bonuses/matching/matched_commission_types",
					Code:     "undefined_reference",
					Message:  fmt.Sprintf("matched_commission_types references unknown structure type %q", ct),
					Severity: SeverityError,
				})
			}
		}
	}

	// rank_advancement amounts must reference defined ranks.
	if plan.Bonuses.RankAdvancement != nil {
		for rankName := range plan.Bonuses.RankAdvancement.Amounts {
			if !ranks[rankName] {
				errs = append(errs, ValidationError{
					Path:     "/bonuses/rank_advancement/amounts",
					Code:     "undefined_reference",
					Message:  fmt.Sprintf("rank_advancement references undefined rank %q", rankName),
					Severity: SeverityError,
				})
			}
		}
	}

	// lifestyle tier min_rank must reference defined ranks.
	if plan.Bonuses.Lifestyle != nil {
		for i, tier := range plan.Bonuses.Lifestyle.Tiers {
			if tier.MinRank != "" && !ranks[tier.MinRank] {
				errs = append(errs, ValidationError{
					Path:     fmt.Sprintf("/bonuses/lifestyle/tiers/%d/min_rank", i),
					Code:     "undefined_reference",
					Message:  fmt.Sprintf("lifestyle tier references undefined rank %q", tier.MinRank),
					Severity: SeverityError,
				})
			}
		}
	}

	// pool qualification min_rank must reference defined ranks.
	for i, pool := range plan.Bonuses.Pool {
		if pool.Qualification.MinRank != nil && *pool.Qualification.MinRank != "" && !ranks[*pool.Qualification.MinRank] {
			errs = append(errs, ValidationError{
				Path:     fmt.Sprintf("/bonuses/pool/%d/qualification/min_rank", i),
				Code:     "undefined_reference",
				Message:  fmt.Sprintf("pool %q qualification references undefined rank %q", pool.Name, *pool.Qualification.MinRank),
				Severity: SeverityError,
			})
		}
	}

	return errs
}

// --- Placement rules ---

// validatePlacement checks placement configuration for cross-field consistency.
func validatePlacement(plan *CompensationPlan) []ValidationError {
	var errs []ValidationError

	// donated_placement_enabled requires donated_placement_restriction.
	if plan.Placement.DonatedPlacementEnabled && plan.Placement.DonatedPlacementRestriction == nil {
		errs = append(errs, ValidationError{
			Path:     "/placement/donated_placement_enabled",
			Code:     "cross_field_dependency",
			Message:  "donated_placement_enabled is true but donated_placement_restriction is not set",
			Severity: SeverityError,
		})
	}

	return errs
}

// --- Eligibility rules ---

// validateEligibility checks commission eligibility configuration.
func validateEligibility(plan *CompensationPlan) []ValidationError {
	var errs []ValidationError

	// Active leg tiers must be sorted by ascending min_active_legs.
	tiers := plan.CommissionEligibility.ActiveLegTiers
	if len(tiers) > 1 {
		sorted := sort.SliceIsSorted(tiers, func(i, j int) bool {
			return tiers[i].MinActiveLegs < tiers[j].MinActiveLegs
		})
		if !sorted {
			errs = append(errs, ValidationError{
				Path:     "/commission_eligibility/active_leg_tiers",
				Code:     "ordering_violation",
				Message:  "active_leg_tiers must be sorted by ascending min_active_legs",
				Severity: SeverityError,
			})
		}
	}

	// An unlimited depth tier (MaxCommissionDepth == 0) must be the last entry.
	for i, tier := range tiers {
		if tier.MaxCommissionDepth == 0 && i != len(tiers)-1 {
			errs = append(errs, ValidationError{
				Path:     fmt.Sprintf("/commission_eligibility/active_leg_tiers/%d", i),
				Code:     "ordering_violation",
				Message:  "active_leg_tier with unlimited depth (max_commission_depth=0) must be the last entry",
				Severity: SeverityError,
			})
			break
		}
	}

	return errs
}

// --- Warnings ---

// validateWarnings produces non-fatal warnings for potentially problematic
// configuration values.
func validateWarnings(plan *CompensationPlan) []ValidationError {
	var errs []ValidationError

	// Long payout lag.
	if plan.Period.PayoutLagDays > 30 {
		errs = append(errs, ValidationError{
			Path:     "/period/payout_lag_days",
			Code:     "long_payout_lag",
			Message:  fmt.Sprintf("payout_lag_days is %d, which exceeds 30 days", plan.Period.PayoutLagDays),
			Severity: SeverityWarning,
		})
	}

	// Rate table completeness: every defined rank should have an entry.
	ranks := rankNames(plan)
	for i, s := range plan.Structures {
		var rateTable map[string]map[string]float64
		switch rc := s.resolvedCommission.(type) {
		case *UnilevelCommission:
			rateTable = rc.RateTable
		case *MatrixCommission:
			rateTable = rc.RateTable
		case *StairstepCommission:
			rateTable = rc.RateTable
		case *GenerationCommission:
			rateTable = rc.RateTable
		}
		if rateTable != nil {
			var missing []string
			for name := range ranks {
				if _, ok := rateTable[name]; !ok {
					missing = append(missing, name)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				errs = append(errs, ValidationError{
					Path:     fmt.Sprintf("/structures/%d/commission/rate_table", i),
					Code:     "incomplete_rate_table",
					Message:  fmt.Sprintf("structure %q rate_table is missing ranks: %s", s.Name, strings.Join(missing, ", ")),
					Severity: SeverityWarning,
				})
			}
		}
	}

	// Matrix size warning: width^height > 1,000,000.
	for i, s := range plan.Structures {
		if s.Structure != nil && s.Structure.Width > 0 && s.Structure.Height > 0 {
			size := math.Pow(float64(s.Structure.Width), float64(s.Structure.Height))
			if size > 1_000_000 {
				errs = append(errs, ValidationError{
					Path:     fmt.Sprintf("/structures/%d/structure", i),
					Code:     "large_matrix",
					Message:  fmt.Sprintf("matrix %q has width^height = %.0f, which exceeds 1,000,000", s.Name, size),
					Severity: SeverityWarning,
				})
			}
		}
	}

	return errs
}
