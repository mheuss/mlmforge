package config

// minimalPlan returns a valid CompensationPlan for use as a baseline in
// business-rule tests. Each test introduces one violation on top of this
// known-good plan.
func minimalPlan() *CompensationPlan {
	return &CompensationPlan{
		Name:    "Test Plan",
		Version: 1,
		Period: PeriodConfig{
			Length:        "month",
			PayoutLagDays: 14,
		},
		Volume: VolumeConfig{
			BaseCurrency:             "USD",
			VolumeToDollarMultiplier: 1.0,
		},
		Ranks: []RankDefinition{
			{
				Name:    "Associate",
				Ordinal: 1,
				Qualification: RankQualification{
					Structures: []StructureQualification{
						{Structure: "Primary"},
					},
					RequiredProducts: []string{},
				},
				QualifiedStructures: []string{"Primary"},
				DemotionPolicy:      DemotionPolicy{StringValue: "promotion_only"},
			},
			{
				Name:    "Silver",
				Ordinal: 2,
				Qualification: RankQualification{
					Structures: []StructureQualification{
						{
							Structure:      "Primary",
							PersonalVolume: 100,
							GroupVolume:    3000,
						},
					},
					RequiredProducts: []string{},
				},
				QualifiedStructures: []string{"Primary"},
				DemotionPolicy:      DemotionPolicy{StringValue: "promotion_only"},
			},
		},
		RankTracking: RankTrackingConfig{TrackAchievedRank: false},
		CommissionEligibility: CommissionEligibility{
			EligibleStatuses: []string{"active"},
			ActiveLegTiers:   []ActiveLegTier{},
		},
		Structures: []StructureConfig{
			{Name: "Primary", Type: "unilevel"},
		},
		Payout: PayoutConfig{
			BaseCurrency:  "USD",
			MinimumAmount: 50,
			Methods:       []PaymentMethod{{Type: "bank_transfer", Fee: 2.50}},
		},
		Caps: CapsConfig{
			CompanyPayoutCapPercent: 0.42,
			CapEnforcement:          "pro_rata",
		},
		Placement: PlacementConfig{
			DonatedPlacementEnabled: false,
		},
	}
}
