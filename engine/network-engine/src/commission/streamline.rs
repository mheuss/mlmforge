//! Streamline commission calculator.
//!
//! Iterates streams in the engine, builds a LevelWalkConfig with dynamic
//! compression thresholds, and calls the shared walk per stream.

use std::collections::{BTreeMap, HashMap};
use uuid::Uuid;

use crate::config::{CompensationPlan, StreamlineStructureConfig};
use crate::decimal::{AuthoredDec, Dec};
use crate::streamline::StreamlineEngine;

use super::types::{
    CalculationError, CommissionCalculationResult, DistributorSnapshot, PlanIdentity, VolumeSource,
};
use super::{walk, walk_order};

/// Calculate streamline commissions across all active streams.
///
/// Rejects an invalid dynamic-compression config, a snapshot naming a rank the
/// plan does not define, a snapshot whose personal_volume is negative, and a
/// stream member with no snapshot, frozen streams included. Also rejects volume
/// it cannot pay: a source with a negative CV amount, or a source held by no
/// stream. A source held only by a frozen stream is accepted and earns nothing.
/// Fails when an exact result cannot be held.
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
    plan_identity: &PlanIdentity,
) -> Result<CommissionCalculationResult, CalculationError> {
    let rank_ordinals = walk::build_rank_ordinals(plan);

    // Place each threshold at its declared level rather than at its position.
    // walk.rs reads thresholds[level - 1] and the rate table is keyed by the
    // declared level, so a position fill desynchronizes the two whenever the
    // table is not contiguous. validate() rejects such tables; this keeps the
    // pairing correct on any path that reaches here without it (HEU-612).
    //
    // Empty min_rank means no threshold (ordinal 0).
    //
    // An empty table is the third member of this guard family and the one with
    // the worst failure mode. Without this it falls through: max_level is 0,
    // slots is empty, the gap map over an empty vec yields Ok, and the walk
    // gets dynamic_thresholds: Some(&[]) — no level gated, no rate in the
    // table, so every rate falls to 0.0 and the call pays nobody without
    // saying why. validate() rejects it at load; this covers the direct
    // caller (HEU-670).
    if structure.streamline_commission.levels.is_empty() {
        return Err(CalculationError::ConfigError(
            "streamline dynamic_compression is empty; no level can pay".to_string(),
        ));
    }

    let max_level = structure
        .streamline_commission
        .levels
        .iter()
        .map(|l| l.level)
        .max()
        .expect("levels is non-empty; guarded above");
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
        // In bounds: slots is sized to max_level, and the guard above proved
        // level.level >= 1.
        slots[usize::from(level.level) - 1] = Some(ordinal);
    }

    // A gap means no threshold was declared for that level. There is no safe
    // default here: 0 pays everyone, and every other value gates somebody.
    // Refuse.
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

    walk::validate_snapshots(plan, snapshots, engine.member_ids())?;

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
    let mut rate_table: BTreeMap<String, BTreeMap<u8, AuthoredDec>> = BTreeMap::new();
    for rank in &plan.ranks {
        rate_table.insert(rank.name.clone(), level_rates.clone());
    }

    // Validate every source before any stream is walked.
    //
    // The checks run per source rather than per check across the slice, so a
    // faulty source's first failing check is the error that surfaces.
    for source in volume {
        walk::validate_cv(source)?;
        if !engine.contains_member(source.source_id) {
            return Err(CalculationError::SourceNotInTree(source.source_id));
        }
        if !snapshots.contains_key(&source.source_id) {
            return Err(CalculationError::SourceNotInSnapshot(source.source_id));
        }
    }

    let max_depth = structure.streamline_commission.max_depth;

    let mut all_earnings = Vec::new();
    // Outside the per-stream loop on purpose: one collector accumulates every
    // stream's walks, so their ids stay unique across the response.
    let mut walks = Vec::new();

    // Visit streams in id order so a failed calculation names the same operands every run.
    let mut streams: Vec<_> = engine.active_streams().collect();
    streams.sort_unstable_by_key(|stream| stream.id);
    for stream in streams {
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
            broad_pct: Dec::ONE,
            multiplier: multiplier.dec(),
            compression: None,
            threshold_ordinal: None,
            rank_ordinals: &rank_ordinals,
            rate_table: &rate_table,
            pass_up: None,
            dynamic_thresholds: Some(&thresholds),
        };

        // Convert filtered refs to owned slice for the walk.
        let owned_volume: Vec<VolumeSource> = stream_volume.iter().map(|v| (*v).clone()).collect();

        // The walks this stream is about to append start here.
        let stream_walks_from = walks.len();

        let earnings = walk::walk_level_commissions(
            &stream.tree,
            &config,
            &eligibility_cache,
            snapshots,
            &owned_volume,
            |_| false,
            &mut walks,
        )?;

        // Stamp the stream on exactly the walks it produced.
        for w in &mut walks[stream_walks_from..] {
            w.stream_id = Some(stream.id);
        }

        all_earnings.extend(earnings);
    }

    Ok(walk_order::assemble(
        all_earnings,
        walks,
        volume,
        &rank_ordinals,
        plan_identity,
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers;
    use crate::config::streamline::{
        StreamAssignmentMode, StreamlineCommissionConfig, StreamlineLevel,
    };
    use crate::streamline::engine::StreamlineConfig;
    use crate::{assert_d, d};

    fn test_uuid(n: u8) -> Uuid {
        crate::tree::test_helpers::test_uuid(n)
    }

    fn make_structure(levels: Vec<StreamlineLevel>, max_depth: u8) -> StreamlineStructureConfig {
        StreamlineStructureConfig {
            name: "test_streamline".to_string(),
            streamline_commission: StreamlineCommissionConfig {
                volume_to_dollar_multiplier: Some(d!(1.0)),
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
        test_helpers::make_rank(name, ordinal, vec!["test_streamline".to_string()])
    }

    fn level(n: u8, min_rank: &str, percent: AuthoredDec) -> StreamlineLevel {
        StreamlineLevel {
            level: n,
            min_rank: min_rank.to_string(),
            percent,
        }
    }

    #[test]
    fn every_streamline_walk_carries_its_stream_id() {
        let engine = make_engine(5);
        let levels = vec![
            StreamlineLevel {
                level: 1,
                min_rank: "bronze".to_string(),
                percent: d!(0.05),
            },
            StreamlineLevel {
                level: 2,
                min_rank: "bronze".to_string(),
                percent: d!(0.04),
            },
            StreamlineLevel {
                level: 3,
                min_rank: "silver".to_string(),
                percent: d!(0.03),
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
                name: "bronze".to_string(),
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
            crate::config::rank::RankDefinition {
                name: "silver".to_string(),
                ordinal: 3,
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
                    personal_volume: d!(150.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        // Volume at node 5 (bottom of chain).
        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: d!(100.0),
        }];

        let result = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap();

        assert!(!result.walks.is_empty(), "the fixture must produce a walk");

        // Every streamline walk names the stream it came from. This is what
        // walk_order sorts on, and without it the index order falls through to
        // a collector id assigned in HashMap iteration order, which design 029
        // forbids.
        for w in &result.walks {
            assert!(
                w.stream_id.is_some(),
                "a streamline walk must carry its stream id, got {w:?}"
            );
        }

        // No determinism check here. It would be vacuous: this fixture has a
        // single stream, and re-running against the same `StreamlineEngine`
        // reuses one `HashMap` with a fixed `RandomState`, so the iteration
        // order is identical by construction rather than by the sort.
        // `walk_order::tests::streams_sort_ascending_within_a_kind` pins the
        // ordering one layer down, where it can actually fail.
    }

    #[test]
    fn single_stream_dynamic_compression() {
        let engine = make_engine(5);
        let levels = vec![
            StreamlineLevel {
                level: 1,
                min_rank: "bronze".to_string(),
                percent: d!(0.05),
            },
            StreamlineLevel {
                level: 2,
                min_rank: "bronze".to_string(),
                percent: d!(0.04),
            },
            StreamlineLevel {
                level: 3,
                min_rank: "silver".to_string(),
                percent: d!(0.03),
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
                name: "bronze".to_string(),
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
            crate::config::rank::RankDefinition {
                name: "silver".to_string(),
                ordinal: 3,
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
                    personal_volume: d!(150.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        // Volume at node 5 (bottom of chain).
        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: d!(100.0),
        }];

        let earnings = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap()
        .earnings;

        // Walk upline from 5: 4 (bronze, qualifies L1), 3 (associate, skipped L2),
        // 2 (bronze, qualifies L2), 1 (silver, qualifies L3).
        //
        // Assert the shape, not the count. Ungated, the walk pays 4, 3 and 2 —
        // also three earnings, so a length check passes either way and never
        // sees the compression this test is named for.
        let shape: Vec<(Uuid, u8, Option<Dec>)> = earnings
            .iter()
            .map(|e| (e.earner_id, e.level, e.rate))
            .collect();
        assert_eq!(
            shape,
            vec![
                (test_uuid(1), 3, Some(d!(0.03))),
                (test_uuid(2), 2, Some(d!(0.04))),
                (test_uuid(4), 1, Some(d!(0.05))),
            ]
        );
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
            percent: d!(0.10),
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
            cv_amount: d!(100.0),
        }];

        let earnings = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap()
        .earnings;
        // Only stream 1 is active. Node 1 earns from node 2's volume.
        assert_eq!(earnings.len(), 1);
        assert_eq!(earnings[0].earner_id, test_uuid(1));
    }

    #[test]
    fn monoline_no_rank_gating() {
        let engine = make_engine(3);
        // All min_rank = "associate", so no level gates anyone out.
        let levels = vec![
            StreamlineLevel {
                level: 1,
                min_rank: "associate".to_string(),
                percent: d!(0.10),
            },
            StreamlineLevel {
                level: 2,
                min_rank: "associate".to_string(),
                percent: d!(0.05),
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
            cv_amount: d!(100.0),
        }];

        let earnings = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap()
        .earnings;
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
            percent: d!(0.10),
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
            cv_amount: d!(100.0),
        }];

        let earnings = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap()
        .earnings;
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
                percent: d!(0.10),
            },
            StreamlineLevel {
                level: 2,
                min_rank: "associate".to_string(),
                percent: d!(0.05),
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
            cv_amount: d!(100.0),
        }];

        let earnings = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap()
        .earnings;
        // Only 2 levels paid (depth cutoff), not 4.
        assert_eq!(earnings.len(), 2);
    }

    #[test]
    fn calculate_streamline_rejects_a_rank_not_in_the_ladder() {
        let engine = make_engine(5);
        let structure = make_structure(
            vec![
                level(1, "associate", d!(0.10)),
                level(2, "bronze", d!(0.05)),
            ],
            5,
        );
        let mut plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        // Ordinals are 1-based. HEU-723.
        plan.ranks = vec![rank_def("associate", 1), rank_def("bronze", 2)];

        // Node 1 asserts a rank the ladder does not hold.
        let mut snapshots = HashMap::new();
        let ranks = ["diamond", "bronze", "associate", "bronze", "associate"];
        for (i, rank) in ranks.iter().enumerate() {
            snapshots.insert(
                test_uuid((i + 1) as u8),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(150.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: d!(100.0),
        }];

        let err = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap_err();

        assert_eq!(
            err,
            CalculationError::UnknownSnapshotRank(test_uuid(1), "diamond".to_string())
        );
    }

    #[test]
    fn calculate_streamline_rejects_a_negative_personal_volume() {
        let engine = make_engine(5);
        let structure = make_structure(
            vec![
                level(1, "associate", d!(0.10)),
                level(2, "bronze", d!(0.05)),
            ],
            5,
        );
        let mut plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        plan.ranks = vec![rank_def("associate", 1), rank_def("bronze", 2)];

        let mut snapshots = HashMap::new();
        let ranks = ["associate", "bronze", "associate", "bronze", "associate"];
        for (i, rank) in ranks.iter().enumerate() {
            snapshots.insert(
                test_uuid((i + 1) as u8),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(150.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }
        snapshots.get_mut(&test_uuid(1)).unwrap().personal_volume = d!(-0.5);

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: d!(100.0),
        }];

        let err = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap_err();

        assert!(
            matches!(
                err,
                CalculationError::InvalidPersonalVolume(id, pv)
                    if id == test_uuid(1) && pv == d!(-0.5)
            ),
            "got {err:?}"
        );
    }

    /// Records the boundary of what the ladder check buys: a rank the plan
    /// defines is accepted even if the distributor has not earned it, so an
    /// over-claim still clears a level threshold.
    ///
    /// This pins the narrow half. Streamline maps every rank to the same
    /// per-level percents, so here an over-claim moves qualification only. In
    /// unilevel and matrix the rate row is keyed by the asserted rank, so it
    /// moves price too, and nothing pins that. HEU-714.
    ///
    /// The neighbouring shuffled-table test asserts the same shape for a
    /// different reason. This one is what fails if the ladder check ever grows
    /// an entitlement check.
    #[test]
    fn an_in_ladder_over_claim_clears_a_level_threshold() {
        let engine = make_engine(5);
        let structure = make_structure(
            vec![
                level(1, "associate", d!(0.10)),
                level(2, "bronze", d!(0.05)),
                level(3, "silver", d!(0.02)),
            ],
            5,
        );
        let mut plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        // Ordinals are 1-based. HEU-723.
        plan.ranks = vec![
            rank_def("associate", 1),
            rank_def("bronze", 2),
            rank_def("silver", 3),
        ];

        // Chain 1 -> 2 -> 3 -> 4 -> 5. Volume at 5 walks up 4, 3, 2, 1.
        // Node 1 asserts silver, which the ladder holds, so the check passes it
        // through and the level-3 threshold is cleared.
        let mut snapshots = HashMap::new();
        let ranks = ["silver", "bronze", "associate", "bronze", "associate"];
        for (i, rank) in ranks.iter().enumerate() {
            snapshots.insert(
                test_uuid((i + 1) as u8),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(150.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: d!(100.0),
        }];

        let earnings = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .expect("an in-ladder rank is accepted")
        .earnings;

        let top = earnings
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("the asserted silver rank did not clear the level-3 threshold");
        assert_eq!(
            earnings
                .iter()
                .filter(|e| e.earner_id == test_uuid(1))
                .count(),
            1
        );
        assert_eq!(top.level, 3);
        assert_eq!(top.rate, Some(d!(0.02)));
        assert_d!(top.dollar_amount, 2.0);
    }

    /// Pins the rank check above the per-source checks. This input is bad on
    /// both counts, so only the check order decides which error surfaces.
    #[test]
    fn streamline_unknown_rank_wins_over_invalid_cv() {
        let engine = make_engine(5);
        let structure = make_structure(
            vec![
                level(1, "associate", d!(0.10)),
                level(2, "bronze", d!(0.05)),
            ],
            5,
        );
        let mut plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        plan.ranks = vec![rank_def("associate", 1), rank_def("bronze", 2)];

        let mut snapshots = HashMap::new();
        let ranks = ["diamond", "bronze", "associate", "bronze", "associate"];
        for (i, rank) in ranks.iter().enumerate() {
            snapshots.insert(
                test_uuid((i + 1) as u8),
                DistributorSnapshot {
                    rank: rank.to_string(),
                    personal_volume: d!(150.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: d!(-50.0),
        }];

        let err = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap_err();

        assert_eq!(
            err,
            CalculationError::UnknownSnapshotRank(test_uuid(1), "diamond".to_string())
        );
    }

    /// Pins the rank check below all four dynamic-compression config guards: a
    /// broken plan still reports before one bad snapshot rank. This input is
    /// bad on both counts.
    ///
    /// Declares level 2 and not level 1, which trips the gap guard, the last
    /// of the four. Tripping the first would leave the test green for a
    /// placement between any two of them.
    #[test]
    fn streamline_config_error_wins_over_unknown_rank() {
        let engine = make_engine(5);
        let structure = make_structure(vec![level(2, "associate", d!(0.10))], 5);
        let mut plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        plan.ranks = vec![rank_def("associate", 1), rank_def("bronze", 2)];

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "diamond".to_string(),
                personal_volume: d!(150.0),
                status: "active".to_string(),
                has_order_in_period: true,
            },
        );

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: d!(100.0),
        }];

        let err = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .unwrap_err();

        // Pins which of the four guards fired, so the docblock above stays
        // true if a refactor changes the order.
        match err {
            CalculationError::ConfigError(msg) => {
                assert!(msg.contains("no entry for level 1"), "wrong guard: {msg}")
            }
            other => panic!("expected ConfigError, got {other:?}"),
        }
    }

    #[test]
    fn shuffled_table_pairs_threshold_to_declared_level() {
        // calculate_streamline is reachable without validate(), so a shuffled
        // table must pair each threshold with its own declared level's rate,
        // not with its position in the vector (HEU-612).
        let engine = make_engine(5);

        let sorted = vec![
            level(1, "associate", d!(0.10)),
            level(2, "bronze", d!(0.05)),
            level(3, "silver", d!(0.02)),
        ];
        let shuffled = vec![
            level(3, "silver", d!(0.02)),
            level(1, "associate", d!(0.10)),
            level(2, "bronze", d!(0.05)),
        ];

        let run = |levels: Vec<StreamlineLevel>| {
            let structure = make_structure(levels, 5);
            let mut plan = test_helpers::build_test_plan(
                test_helpers::default_eligibility(),
                crate::config::StructureConfig::Streamline(structure.clone()),
                "test_streamline",
            );
            plan.ranks = vec![
                rank_def("associate", 1),
                rank_def("bronze", 2),
                rank_def("silver", 3),
            ];

            let mut snapshots = HashMap::new();
            // Chain 1 -> 2 -> 3 -> 4 -> 5. Volume at 5 walks up 4, 3, 2, 1.
            let ranks = ["silver", "bronze", "associate", "bronze", "associate"];
            for (i, rank) in ranks.iter().enumerate() {
                snapshots.insert(
                    test_uuid((i + 1) as u8),
                    DistributorSnapshot {
                        rank: rank.to_string(),
                        personal_volume: d!(150.0),
                        status: "active".to_string(),
                        has_order_in_period: true,
                    },
                );
            }

            let volume = vec![VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(100.0),
            }];

            calculate_streamline(
                &engine,
                &plan,
                &structure,
                &snapshots,
                &volume,
                &crate::test_support::test_plan_identity(),
            )
            .unwrap()
            .earnings
        };

        let from_sorted = run(sorted);
        let from_shuffled = run(shuffled);
        assert_eq!(from_sorted, from_shuffled);

        // Pin the shape too. Equality alone would still hold if rank gating
        // were accidentally disabled on both runs, which is the failure this
        // test is least able to see.
        //
        // Walk up from 5: node 4 is bronze and level 1 needs associate, so it
        // earns at level 1 and 0.10. Node 3 is associate and level 2 needs
        // bronze, so it is compressed without consuming the level. Node 2 is
        // bronze and earns at level 2 and 0.05. Node 1 is silver and level 3
        // needs silver, so it earns at level 3 and 0.02.
        //
        // calculate_streamline ends with walk_order::assemble, which sorts by
        // (earner_id, source_id, level, walk). test_uuid puts the index in the
        // leading byte, so the earners come back ascending: 1, 2, 4.
        let shape: Vec<(Uuid, u8, Option<Dec>)> = from_shuffled
            .iter()
            .map(|e| (e.earner_id, e.level, e.rate))
            .collect();
        assert_eq!(
            shape,
            vec![
                (test_uuid(1), 3, Some(d!(0.02))),
                (test_uuid(2), 2, Some(d!(0.05))),
                (test_uuid(4), 1, Some(d!(0.10))),
            ]
        );
    }

    #[test]
    fn gapped_table_errors_rather_than_paying() {
        // apex sits at 65535 here. This test turns only on the error, so that
        // value does not change its outcome.
        let engine = make_engine(5);
        let levels = vec![level(1, "associate", d!(0.10)), level(3, "apex", d!(0.02))];
        let structure = make_structure(levels, 5);

        let mut plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        plan.ranks = vec![rank_def("associate", 1), rank_def("apex", 65535)];

        let mut snapshots = HashMap::new();
        for i in 1..=5u8 {
            snapshots.insert(
                test_uuid(i),
                DistributorSnapshot {
                    rank: "apex".to_string(),
                    personal_volume: d!(150.0),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: d!(100.0),
        }];

        let err = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .expect_err("a gapped table must not produce earnings");
        match err {
            CalculationError::ConfigError(msg) => {
                assert!(msg.contains("level 2"), "unexpected message: {msg}");
            }
            other => panic!("expected ConfigError, got {other:?}"),
        }
    }

    #[test]
    fn empty_table_errors_rather_than_paying_nobody() {
        // The third guard. Before HEU-670 this returned Ok(vec![]) and paid
        // nobody, which is quieter and worse than the gap case beside it.
        let engine = make_engine(3);
        let structure = make_structure(vec![], 5);

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
            cv_amount: d!(100.0),
        }];

        let err = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .expect_err("an empty table must not silently pay nobody");
        match err {
            CalculationError::ConfigError(msg) => {
                assert!(msg.contains("is empty"), "unexpected message: {msg}");
            }
            other => panic!("expected ConfigError, got {other:?}"),
        }
    }

    #[test]
    fn level_zero_errors_rather_than_panicking() {
        // Levels are 1-based. A declared level of 0 would underflow the index.
        let engine = make_engine(3);
        let levels = vec![level(0, "associate", d!(0.10))];
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
            cv_amount: d!(100.0),
        }];

        let err = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .expect_err("level 0 must be rejected, not panic");
        match err {
            CalculationError::ConfigError(msg) => {
                // Pin the message: the shared fixture could otherwise satisfy
                // this test with an unrelated ConfigError.
                assert!(msg.contains("1-based"), "unexpected message: {msg}");
            }
            other => panic!("expected ConfigError, got {other:?}"),
        }
    }

    /// Engine with members 1..=5, a plan and structure that pay, and a snapshot
    /// for every member.
    fn validation_fixture() -> (
        StreamlineEngine,
        CompensationPlan,
        StreamlineStructureConfig,
        HashMap<Uuid, DistributorSnapshot>,
    ) {
        let engine = make_engine(5);
        let structure = make_structure(vec![level(1, "associate", d!(0.10))], 5);
        let plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        let mut snapshots = HashMap::new();
        for i in 1..=5 {
            snapshots.insert(test_uuid(i), test_helpers::eligible_snapshot());
        }
        (engine, plan, structure, snapshots)
    }

    #[test]
    fn source_in_no_stream_returns_source_not_in_tree() {
        let (engine, plan, structure, snapshots) = validation_fixture();
        let volume = vec![VolumeSource {
            source_id: test_uuid(99),
            cv_amount: d!(100.0),
        }];

        let result = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        );

        assert!(matches!(
            result,
            Err(CalculationError::SourceNotInTree(id)) if id == test_uuid(99)
        ));
    }

    #[test]
    fn invalid_cv_wins_over_source_not_in_tree() {
        let (engine, plan, structure, snapshots) = validation_fixture();
        let volume = vec![VolumeSource {
            source_id: test_uuid(99),
            cv_amount: d!(-1.0),
        }];

        let result = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        );

        assert!(matches!(
            result,
            Err(CalculationError::InvalidCvAmount(id, _)) if id == test_uuid(99)
        ));
    }

    #[test]
    fn stream_member_with_no_snapshot_returns_distributor_not_in_snapshot() {
        let (engine, plan, structure, mut snapshots) = validation_fixture();
        snapshots.remove(&test_uuid(3));
        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: d!(100.0),
        }];

        let result = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        );

        assert_eq!(
            result.unwrap_err(),
            CalculationError::DistributorNotInSnapshot(test_uuid(3))
        );
    }

    fn earnings_from_member_three(engine: &StreamlineEngine) -> Vec<(Uuid, Uuid, u8, Dec)> {
        let structure = make_structure(vec![level(1, "associate", d!(0.10))], 5);
        let plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        let mut snapshots = HashMap::new();
        for i in [2, 3] {
            snapshots.insert(test_uuid(i), test_helpers::eligible_snapshot());
        }
        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: d!(100.0),
        }];
        calculate_streamline(
            engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .expect("expected ok")
        .earnings
        .iter()
        .map(|e| (e.earner_id, e.source_id, e.level, e.dollar_amount))
        .collect()
    }

    #[test]
    fn a_re_bootstrapped_engine_pays_like_a_fresh_one() {
        let config = StreamlineConfig {
            assignment_mode: StreamAssignmentMode::SponsorStream,
            enrollment_stream_choice: false,
            freeze_on_demotion: false,
        };
        let mut fresh = StreamlineEngine::new(config.clone(), 1000);
        fresh
            .add_member(test_uuid(2), test_uuid(1), 1002, None)
            .unwrap();
        fresh
            .add_member(test_uuid(3), test_uuid(2), 1003, None)
            .unwrap();

        let mut rebuilt = StreamlineEngine::new(config, 1000);
        rebuilt
            .add_member(test_uuid(1), test_uuid(99), 1001, None)
            .unwrap();
        rebuilt
            .update_stream_allowance(test_uuid(1), 0, 1001)
            .unwrap();
        rebuilt
            .add_member(test_uuid(2), test_uuid(1), 1002, None)
            .unwrap();
        rebuilt
            .add_member(test_uuid(3), test_uuid(2), 1003, None)
            .unwrap();

        let fresh_earnings = earnings_from_member_three(&fresh);
        assert_eq!(
            fresh_earnings,
            vec![(test_uuid(2), test_uuid(3), 1, d!(10.0))],
            "the fresh engine should pay member 2 on member 3's volume"
        );
        assert_eq!(earnings_from_member_three(&rebuilt), fresh_earnings);
    }

    /// A member held only by a frozen stream still needs a snapshot, even with
    /// no volume.
    #[test]
    fn frozen_stream_member_with_no_snapshot_is_rejected() {
        let (mut engine, plan, structure, mut snapshots) = validation_fixture();
        // Dropping the owner's allowance to 0 freezes every stream it owns.
        engine
            .update_stream_allowance(test_uuid(1), 0, 2000)
            .unwrap();
        snapshots.remove(&test_uuid(3));
        let volume: Vec<VolumeSource> = vec![];

        let result = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        );

        assert_eq!(
            result.unwrap_err(),
            CalculationError::DistributorNotInSnapshot(test_uuid(3))
        );
    }

    /// A frozen-only source is valid input, not a caller mistake.
    ///
    /// Narrowing the membership check to active streams would turn this into an
    /// error. HEU-611 rules that out.
    #[test]
    fn frozen_only_source_with_snapshot_returns_ok_with_no_earnings() {
        let (mut engine, plan, structure, snapshots) = validation_fixture();
        engine
            .update_stream_allowance(test_uuid(1), 0, 2000)
            .unwrap();
        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: d!(100.0),
        }];

        let result = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        )
        .expect("expected ok from a frozen-only source");

        assert!(
            result.earnings.is_empty(),
            "a frozen stream pays nothing, got: {:?}",
            result.earnings
        );
    }

    /// Pins that streamline refuses a member with no snapshot that sits upline
    /// of the source.
    #[test]
    fn missing_upline_snapshot_errors() {
        let (engine, plan, structure, mut snapshots) = validation_fixture();
        snapshots.remove(&test_uuid(4));

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: d!(100.0),
        }];

        let result = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        );

        assert_eq!(
            result.unwrap_err(),
            CalculationError::DistributorNotInSnapshot(test_uuid(4))
        );
    }

    /// Faults resolve per source in input order, not per check across the
    /// slice. The first source's tree fault must beat the second source's negative CV.
    #[test]
    fn faults_resolve_per_source_in_input_order() {
        let (engine, plan, structure, snapshots) = validation_fixture();
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(99),
                cv_amount: d!(100.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(-1.0),
            },
        ];

        let result = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        );

        assert_eq!(
            result.unwrap_err(),
            CalculationError::SourceNotInTree(test_uuid(99))
        );
    }

    /// A missing snapshot is reported before any source's CV is checked.
    #[test]
    fn missing_snapshot_beats_an_invalid_cv() {
        let (engine, plan, structure, mut snapshots) = validation_fixture();
        snapshots.remove(&test_uuid(3));
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(100.0),
            },
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(-1.0),
            },
        ];

        let result = calculate_streamline(
            &engine,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &crate::test_support::test_plan_identity(),
        );

        assert_eq!(
            result.unwrap_err(),
            CalculationError::DistributorNotInSnapshot(test_uuid(3))
        );
    }

    /// One stream holding `a` and its child `b`, as snapshot JSON.
    fn one_stream_snapshot(a: u8, b: u8) -> serde_json::Value {
        let config = StreamlineConfig {
            assignment_mode: StreamAssignmentMode::SponsorStream,
            enrollment_stream_choice: false,
            freeze_on_demotion: true,
        };
        let mut engine = StreamlineEngine::new(config, 1000);
        engine
            .add_member(test_uuid(a), test_uuid(99), 1000, None)
            .unwrap();
        engine
            .add_member(test_uuid(b), test_uuid(a), 1001, None)
            .unwrap();
        serde_json::to_value(&engine).unwrap()
    }

    /// Two active streams, 1 -> 2 as stream 1 and 3 -> 4 as stream 2.
    fn two_stream_snapshot() -> String {
        let mut first = one_stream_snapshot(1, 2);
        let second = one_stream_snapshot(3, 4);
        let mut stream = second["streams"]["1"].clone();
        stream["id"] = serde_json::json!(2);
        first["streams"]["2"] = stream;
        for user in [3u8, 4] {
            first["user_streams"][test_uuid(user).to_string()] = serde_json::json!([2]);
        }
        first["stream_owners"][test_uuid(3).to_string()] = serde_json::json!([2]);
        first["next_stream_id"] = serde_json::json!(3);
        first.to_string()
    }

    #[test]
    fn streams_name_the_same_failing_operands_on_every_run() {
        let structure = StreamlineStructureConfig {
            name: "test_streamline".to_string(),
            streamline_commission: StreamlineCommissionConfig {
                volume_to_dollar_multiplier: Some(d!(0.0000000001)),
                max_depth: 5,
                levels: vec![StreamlineLevel {
                    level: 1,
                    min_rank: "associate".to_string(),
                    percent: d!(0.0000000001),
                }],
                stream_config: None,
            },
        };
        let plan = test_helpers::build_test_plan(
            test_helpers::default_eligibility(),
            crate::config::StructureConfig::Streamline(structure.clone()),
            "test_streamline",
        );
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(0.0000000001),
            },
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(0.0000000003),
            },
        ];
        let json = two_stream_snapshot();
        let mut seen = std::collections::BTreeSet::new();
        // Restored inside the loop so each pass samples a fresh HashMap order.
        for _ in 0..32 {
            let engine: StreamlineEngine = serde_json::from_str(&json).unwrap();
            engine.validate_restored().unwrap();
            let snapshots: HashMap<Uuid, DistributorSnapshot> = (1..=4)
                .map(|i| (test_uuid(i), test_helpers::eligible_snapshot()))
                .collect();
            let err = calculate_streamline(
                &engine,
                &plan,
                &structure,
                &snapshots,
                &volume,
                &crate::test_support::test_plan_identity(),
            )
            .unwrap_err();
            seen.insert(err.to_string());
        }
        assert_eq!(seen.len(), 1, "distinct errors across runs: {seen:?}");
        let only = seen.first().unwrap();
        assert!(only.contains("0.00000000000000000001 and"), "{only}");
    }
}
