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

    // Place each threshold at its declared level rather than at its position.
    // walk.rs reads thresholds[level - 1] and the rate table is keyed by the
    // declared level, so a position fill desynchronizes the two whenever the
    // table is not contiguous. validate() rejects such tables; this keeps the
    // pairing correct on any path that reaches here without it (HEU-612).
    //
    // Empty min_rank means no threshold (ordinal 0).
    let max_level = structure
        .streamline_commission
        .levels
        .iter()
        .map(|l| l.level)
        .max()
        .unwrap_or(0);
    let mut slots: Vec<Option<u16>> = vec![None; usize::from(max_level)];

    for level in &structure.streamline_commission.levels {
        // Levels are 1-based. A declared 0 would underflow the index below,
        // and a panic in the worker is worse than the bug being fixed.
        if level.level < 1 {
            return Err(CalculationError::ConfigError(
                "streamline dynamic_compression level 0 is invalid; levels are 1-based".to_string(),
            ));
        }
        let ordinal = if level.min_rank.is_empty() {
            0
        } else {
            rank_ordinals
                .get(level.min_rank.as_str())
                .copied()
                .ok_or_else(|| {
                    CalculationError::ConfigError(format!(
                        "streamline level {} references unknown rank {:?}",
                        level.level, level.min_rank
                    ))
                })?
        };
        slots[usize::from(level.level) - 1] = Some(ordinal);
    }

    // A gap means no threshold was declared for that level. There is no safe
    // default: 0 pays everyone, and any sentinel is a real ordinal, because
    // rank ordinals span the whole u16 range (config/rank.rs:29). Refuse.
    let thresholds: Vec<u16> = slots
        .into_iter()
        .enumerate()
        .map(|(idx, slot)| {
            slot.ok_or_else(|| {
                CalculationError::ConfigError(format!(
                    "streamline dynamic_compression has no entry for level {}",
                    idx + 1
                ))
            })
        })
        .collect::<Result<_, _>>()?;

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

    fn rank_def(name: &str, ordinal: u16) -> crate::config::rank::RankDefinition {
        crate::config::rank::RankDefinition {
            name: name.to_string(),
            ordinal,
            qualification: crate::config::rank::RankQualification {
                structures: vec![],
                required_products: vec![],
                window: None,
                tenure: None,
            },
            qualified_structures: vec!["test_streamline".to_string()],
            demotion_policy: crate::config::rank::DemotionPolicy::PromotionOnly,
        }
    }

    fn level(n: u8, min_rank: &str, percent: f64) -> StreamlineLevel {
        StreamlineLevel {
            level: n,
            min_rank: min_rank.to_string(),
            percent,
        }
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
                    window: None,
                    tenure: None,
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
                    window: None,
                    tenure: None,
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
                    window: None,
                    tenure: None,
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

    #[test]
    fn shuffled_table_pairs_threshold_to_declared_level() {
        // calculate_streamline is reachable without validate(), so a shuffled
        // table must pair each threshold with its own declared level's rate,
        // not with its position in the vector (HEU-612).
        let engine = make_engine(5);

        let sorted = vec![
            level(1, "associate", 0.10),
            level(2, "bronze", 0.05),
            level(3, "silver", 0.02),
        ];
        let shuffled = vec![
            level(3, "silver", 0.02),
            level(1, "associate", 0.10),
            level(2, "bronze", 0.05),
        ];

        let run = |levels: Vec<StreamlineLevel>| {
            let structure = make_structure(levels, 5);
            let mut plan = test_helpers::build_test_plan(
                test_helpers::default_eligibility(),
                crate::config::StructureConfig::Streamline(structure.clone()),
                "test_streamline",
            );
            plan.ranks = vec![
                rank_def("associate", 0),
                rank_def("bronze", 1),
                rank_def("silver", 2),
            ];

            let mut snapshots = HashMap::new();
            // Chain 1 -> 2 -> 3 -> 4 -> 5. Volume at 5 walks up 4, 3, 2, 1.
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

            let volume = vec![VolumeSource {
                source_id: test_uuid(5),
                cv_amount: 100.0,
            }];

            calculate_streamline(&engine, &plan, &structure, &snapshots, &volume).unwrap()
        };

        let from_sorted = run(sorted);
        let from_shuffled = run(shuffled);
        assert_eq!(from_sorted, from_shuffled);

        // Pin the shape too. Equality alone would still hold if rank gating
        // were accidentally disabled on both runs, which is the failure this
        // test is least able to see.
        //
        // Walk up from 5: node 4 is bronze (ordinal 1) and level 1 needs
        // associate (0), so it earns at level 1 and 0.10. Node 3 is associate
        // (0) and level 2 needs bronze (1), so it is compressed without
        // consuming the level. Node 2 is bronze and earns at level 2 and 0.05.
        // Node 1 is silver (2) and level 3 needs silver, so it earns at level 3
        // and 0.02.
        //
        // calculate_streamline ends with walk::sort_earnings, which orders by
        // (earner_id, source_id, level). test_uuid puts the index in the
        // leading byte, so the earners come back ascending: 1, 2, 4.
        let shape: Vec<(Uuid, u8, f64)> = from_shuffled
            .iter()
            .map(|e| (e.earner_id, e.level, e.rate))
            .collect();
        assert_eq!(
            shape,
            vec![
                (test_uuid(1), 3, 0.02),
                (test_uuid(2), 2, 0.05),
                (test_uuid(4), 1, 0.10),
            ]
        );
    }

    #[test]
    fn gapped_table_errors_rather_than_paying() {
        // The plan defines a rank at ordinal 65535 on purpose. That is the
        // exact input that made the first design draft's u16::MAX gap sentinel
        // pay instead of skip, so without it this test passes against the
        // broken design too.
        let engine = make_engine(5);
        let levels = vec![level(1, "associate", 0.10), level(3, "apex", 0.02)];
        let structure = make_structure(levels, 5);

        let mut plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        plan.ranks = vec![rank_def("associate", 0), rank_def("apex", 65535)];

        let mut snapshots = HashMap::new();
        for i in 1..=5u8 {
            snapshots.insert(
                test_uuid(i),
                DistributorSnapshot {
                    rank: "apex".to_string(),
                    personal_volume: 150.0,
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let err = calculate_streamline(&engine, &plan, &structure, &snapshots, &volume)
            .expect_err("a gapped table must not produce earnings");
        match err {
            CalculationError::ConfigError(msg) => {
                assert!(msg.contains("level 2"), "unexpected message: {msg}");
            }
            other => panic!("expected ConfigError, got {other:?}"),
        }
    }

    #[test]
    fn level_zero_errors_rather_than_panicking() {
        // Levels are 1-based. A declared level of 0 would underflow the index.
        let engine = make_engine(3);
        let levels = vec![level(0, "associate", 0.10)];
        let structure = make_structure(levels, 5);

        let plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );

        let mut snapshots = HashMap::new();
        for i in 1..=3u8 {
            snapshots.insert(test_uuid(i), test_helpers::eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let err = calculate_streamline(&engine, &plan, &structure, &snapshots, &volume)
            .expect_err("level 0 must be rejected, not panic");
        assert!(matches!(err, CalculationError::ConfigError(_)));
    }
}
