//! Streamline commission calculator.
//!
//! Iterates streams in the engine, builds a LevelWalkConfig with dynamic
//! compression thresholds, and calls the shared walk per stream.

use std::collections::{BTreeMap, HashMap};
use uuid::Uuid;

use crate::config::{CompensationPlan, StreamlineStructureConfig};
use crate::streamline::StreamlineEngine;

use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};
use super::walk;

/// Calculate streamline commissions across all active streams.
///
/// Each unfrozen stream is walked independently. Dynamic compression
/// thresholds gate per-level qualification by rank ordinal. Monoline
/// behavior falls out naturally when all thresholds are zero.
pub fn calculate_streamline(
    engine: &StreamlineEngine,
    plan: &CompensationPlan,
    structure: &StreamlineStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    let rank_ordinals = walk::build_rank_ordinals(plan);

    // Build dynamic threshold array from the per-level config.
    // Convert each level's min_rank name to its ordinal. Empty
    // min_rank means no threshold (ordinal 0).
    let mut thresholds: Vec<u16> = Vec::with_capacity(structure.streamline_commission.levels.len());
    for level in &structure.streamline_commission.levels {
        if level.min_rank.is_empty() {
            thresholds.push(0);
        } else {
            let ordinal = rank_ordinals
                .get(level.min_rank.as_str())
                .copied()
                .ok_or_else(|| {
                    CalculationError::ConfigError(format!(
                        "streamline level {} references unknown rank {:?}",
                        level.level, level.min_rank
                    ))
                })?;
            thresholds.push(ordinal);
        }
    }

    let multiplier = structure
        .streamline_commission
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    // Build a rate table where every rank maps to the same per-level percents.
    // The dynamic threshold check handles rank gating separately.
    let mut level_rates = BTreeMap::new();
    for level in &structure.streamline_commission.levels {
        level_rates.insert(level.level, level.percent);
    }
    let mut rate_table: BTreeMap<String, BTreeMap<u8, f64>> = BTreeMap::new();
    for rank in &plan.ranks {
        rate_table.insert(rank.name.clone(), level_rates.clone());
    }

    let max_depth = structure.streamline_commission.max_depth;

    let mut all_earnings = Vec::new();

    for stream in engine.active_streams() {
        let eligibility_cache =
            walk::evaluate_eligibility(snapshots, &stream.tree, &plan.eligibility);

        // Filter volume to sources that are members of this stream.
        let stream_volume: Vec<&VolumeSource> = volume
            .iter()
            .filter(|v| stream.tree.contains(v.source_id))
            .collect();

        if stream_volume.is_empty() {
            continue;
        }

        let config = walk::LevelWalkConfig {
            max_depth,
            broad_pct: 1.0,
            multiplier,
            compression: None,
            threshold_ordinal: None,
            rank_ordinals: &rank_ordinals,
            rate_table: &rate_table,
            pass_up: None,
            dynamic_thresholds: Some(&thresholds),
        };

        // Convert filtered refs to owned slice for the walk.
        let owned_volume: Vec<VolumeSource> = stream_volume.iter().map(|v| (*v).clone()).collect();

        let earnings = walk::walk_level_commissions(
            &stream.tree,
            &config,
            &eligibility_cache,
            snapshots,
            &owned_volume,
            |_| false,
        )?;

        all_earnings.extend(earnings);
    }

    walk::sort_earnings(&mut all_earnings);
    Ok(all_earnings)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers;
    use crate::config::streamline::{
        StreamAssignmentMode, StreamlineCommissionConfig, StreamlineLevel,
    };
    use crate::streamline::engine::StreamlineConfig;

    fn test_uuid(n: u8) -> Uuid {
        crate::tree::test_helpers::test_uuid(n)
    }

    fn make_structure(levels: Vec<StreamlineLevel>, max_depth: u8) -> StreamlineStructureConfig {
        StreamlineStructureConfig {
            name: "test_streamline".to_string(),
            streamline_commission: StreamlineCommissionConfig {
                volume_to_dollar_multiplier: Some(1.0),
                max_depth,
                levels,
                stream_config: None,
            },
        }
    }

    fn make_engine(n_members: u8) -> StreamlineEngine {
        let config = StreamlineConfig {
            assignment_mode: StreamAssignmentMode::SponsorStream,
            enrollment_stream_choice: false,
            freeze_on_demotion: true,
        };
        let mut engine = StreamlineEngine::new(config, 1000);
        for i in 1..=n_members {
            if i == 1 {
                engine
                    .add_member(test_uuid(i), test_uuid(99), 1000 + i as i64, None)
                    .unwrap();
            } else {
                engine
                    .add_member(test_uuid(i), test_uuid(1), 1000 + i as i64, None)
                    .unwrap();
            }
        }
        engine
    }

    #[test]
    fn single_stream_dynamic_compression() {
        let engine = make_engine(5);
        let levels = vec![
            StreamlineLevel {
                level: 1,
                min_rank: "bronze".to_string(),
                percent: 0.05,
            },
            StreamlineLevel {
                level: 2,
                min_rank: "bronze".to_string(),
                percent: 0.04,
            },
            StreamlineLevel {
                level: 3,
                min_rank: "silver".to_string(),
                percent: 0.03,
            },
        ];
        let structure = make_structure(levels, 5);

        let mut plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        // Add multiple ranks.
        plan.ranks = vec![
            crate::config::rank::RankDefinition {
                name: "associate".to_string(),
                ordinal: 0,
                qualification: crate::config::rank::RankQualification {
                    structures: vec![],
                    required_products: vec![],
                },
                qualified_structures: vec!["test_streamline".to_string()],
                demotion_policy: crate::config::rank::DemotionPolicy::PromotionOnly,
            },
            crate::config::rank::RankDefinition {
                name: "bronze".to_string(),
                ordinal: 1,
                qualification: crate::config::rank::RankQualification {
                    structures: vec![],
                    required_products: vec![],
                },
                qualified_structures: vec!["test_streamline".to_string()],
                demotion_policy: crate::config::rank::DemotionPolicy::PromotionOnly,
            },
            crate::config::rank::RankDefinition {
                name: "silver".to_string(),
                ordinal: 2,
                qualification: crate::config::rank::RankQualification {
                    structures: vec![],
                    required_products: vec![],
                },
                qualified_structures: vec!["test_streamline".to_string()],
                demotion_policy: crate::config::rank::DemotionPolicy::PromotionOnly,
            },
        ];

        let mut snapshots = HashMap::new();
        // Chain: 1 → 2 → 3 → 4 → 5
        // Node 1 = silver, 2 = bronze, 3 = associate, 4 = bronze, 5 = associate
        let ranks = ["silver", "bronze", "associate", "bronze", "associate"];
        for (i, rank) in ranks.iter().enumerate() {
            snapshots.insert(
                test_uuid((i + 1) as u8),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: 150.0,
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        // Volume at node 5 (bottom of chain).
        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let earnings =
            calculate_streamline(&engine, &plan, &structure, &snapshots, &volume).unwrap();

        // Walk upline from 5: 4 (bronze, qualifies L1), 3 (associate, skipped L2),
        // 2 (bronze, qualifies L2), 1 (silver, qualifies L3).
        assert_eq!(earnings.len(), 3);
    }

    #[test]
    fn frozen_stream_skipped() {
        let config = StreamlineConfig {
            assignment_mode: StreamAssignmentMode::SponsorStream,
            enrollment_stream_choice: false,
            freeze_on_demotion: true,
        };
        let mut engine = StreamlineEngine::new(config, 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine
            .add_member(test_uuid(2), test_uuid(1), 1001, None)
            .unwrap();
        engine.expand_streams(test_uuid(1), 2, 1002).unwrap();
        // Freeze stream 2.
        engine
            .update_stream_allowance(test_uuid(1), 1, 2000)
            .unwrap();

        let levels = vec![StreamlineLevel {
            level: 1,
            min_rank: "associate".to_string(),
            percent: 0.10,
        }];
        let structure = make_structure(levels, 5);

        let plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );

        let mut snapshots = HashMap::new();
        for i in 1..=2 {
            snapshots.insert(test_uuid(i), test_helpers::eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let earnings =
            calculate_streamline(&engine, &plan, &structure, &snapshots, &volume).unwrap();
        // Only stream 1 is active. Node 1 earns from node 2's volume.
        assert_eq!(earnings.len(), 1);
        assert_eq!(earnings[0].earner_id, test_uuid(1));
    }

    #[test]
    fn monoline_no_rank_gating() {
        let engine = make_engine(3);
        // All min_rank = "associate" (ordinal 0 = no gating).
        let levels = vec![
            StreamlineLevel {
                level: 1,
                min_rank: "associate".to_string(),
                percent: 0.10,
            },
            StreamlineLevel {
                level: 2,
                min_rank: "associate".to_string(),
                percent: 0.05,
            },
        ];
        let structure = make_structure(levels, 5);

        let plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );

        let mut snapshots = HashMap::new();
        for i in 1..=3 {
            snapshots.insert(test_uuid(i), test_helpers::eligible_snapshot());
        }

        // Volume at node 3. Walk upline: 2, 1.
        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let earnings =
            calculate_streamline(&engine, &plan, &structure, &snapshots, &volume).unwrap();
        // Both nodes 2 and 1 earn (no rank gating = monoline).
        assert_eq!(earnings.len(), 2);
    }

    #[test]
    fn empty_stream_no_earnings() {
        let config = StreamlineConfig {
            assignment_mode: StreamAssignmentMode::SponsorStream,
            enrollment_stream_choice: false,
            freeze_on_demotion: true,
        };
        let mut engine = StreamlineEngine::new(config, 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine.expand_streams(test_uuid(1), 2, 1001).unwrap();

        let levels = vec![StreamlineLevel {
            level: 1,
            min_rank: "associate".to_string(),
            percent: 0.10,
        }];
        let structure = make_structure(levels, 5);

        let plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), test_helpers::eligible_snapshot());

        // Volume in stream 1 only. Stream 2 is empty.
        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let earnings =
            calculate_streamline(&engine, &plan, &structure, &snapshots, &volume).unwrap();
        // Node 1 is at root of stream 1, no one above to earn.
        assert_eq!(earnings.len(), 0);
    }

    #[test]
    fn max_depth_cutoff() {
        let engine = make_engine(5);
        let levels = vec![
            StreamlineLevel {
                level: 1,
                min_rank: "associate".to_string(),
                percent: 0.10,
            },
            StreamlineLevel {
                level: 2,
                min_rank: "associate".to_string(),
                percent: 0.05,
            },
        ];
        // Max depth = 2 but chain is 4 deep.
        let structure = make_structure(levels, 2);

        let plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );

        let mut snapshots = HashMap::new();
        for i in 1..=5 {
            snapshots.insert(test_uuid(i), test_helpers::eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let earnings =
            calculate_streamline(&engine, &plan, &structure, &snapshots, &volume).unwrap();
        // Only 2 levels paid (depth cutoff), not 4.
        assert_eq!(earnings.len(), 2);
    }
}
