use network_engine::config::eligibility::CommissionEligibility;
use network_engine::config::{CompensationPlan, StructureConfig, UnilevelStructureConfig};
use network_engine::test_support::{
    build_test_plan, make_rank, member_snapshot, snapshot_with_rank, uuid_from_index,
};

fn empty_unilevel_structure() -> StructureConfig {
    StructureConfig::Unilevel(UnilevelStructureConfig {
        name: "Test".to_string(),
        level_commission: network_engine::config::commission::LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: None,
            max_depth: 3,
            rate_table: Default::default(),
        },
        compression: None,
        pass_up: None,
    })
}

fn permissive_eligibility() -> CommissionEligibility {
    CommissionEligibility {
        minimum_pv: 0.0,
        require_order_in_period: false,
        eligible_statuses: vec![],
        active_leg_tiers: vec![],
    }
}

#[test]
fn test_support_helpers_are_available_to_integration_tests() {
    let plan: CompensationPlan =
        build_test_plan(permissive_eligibility(), empty_unilevel_structure(), "Test");
    let snapshot = snapshot_with_rank("associate");
    let member = member_snapshot();
    let rank = make_rank("silver", 2, vec!["Test".to_string()]);

    assert_eq!(plan.name, "Test Plan");
    assert_eq!(snapshot.rank, "associate");
    assert_eq!(snapshot.personal_volume, 100.0);
    assert_eq!(member.rank, "member");
    assert_eq!(rank.name, "silver");
    assert_eq!(rank.ordinal, 2);
    assert_eq!(rank.qualified_structures, vec!["Test".to_string()]);
    assert!(matches!(
        rank.demotion_policy,
        network_engine::config::rank::DemotionPolicy::PromotionOnly
    ));
    assert_ne!(uuid_from_index(1), uuid_from_index(2));
}
