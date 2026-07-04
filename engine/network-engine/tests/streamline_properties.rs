mod common;

use common::uuid_from_index;
use network_engine::config::streamline::StreamAssignmentMode;
use network_engine::streamline::engine::{StreamlineConfig, StreamlineEngine};
use proptest::prelude::*;
use std::collections::HashSet;

fn default_config() -> StreamlineConfig {
    StreamlineConfig {
        assignment_mode: StreamAssignmentMode::SponsorStream,
        enrollment_stream_choice: false,
        freeze_on_demotion: true,
    }
}

/// Builds an engine with `member_count` members in a single linear chain.
fn build_engine(member_count: usize) -> StreamlineEngine {
    let mut engine = StreamlineEngine::new(default_config(), 1000);
    for i in 1..=member_count {
        let sponsor = if i == 1 {
            uuid_from_index(0)
        } else {
            uuid_from_index(1)
        };
        engine
            .add_member(uuid_from_index(i), sponsor, 1000 + i as i64, None)
            .expect("fixture construction should succeed");
    }
    engine
}

// ---------------------------------------------------------------------------
// Property 1: User-stream consistency
// ---------------------------------------------------------------------------

proptest! {
    #[test]
    fn user_stream_consistency(member_count in 1_usize..20) {
        let engine = build_engine(member_count);

        // Every user in user_streams has a position in that stream's tree.
        for i in 1..=member_count {
            let user = uuid_from_index(i);
            if let Some(stream_ids) = engine.get_member_streams(user) {
                for &sid in stream_ids {
                    let stream = engine.get_stream(sid).expect("stream exists");
                    assert!(stream.tree.contains(user),
                        "user {:?} listed in stream {} but not in tree", user, sid);
                }
            }
        }

        // Every tree member's user_streams entry includes this stream.
        for stream in engine.active_streams() {
            for uid in stream.tree.user_ids() {
                let member_streams = engine.get_member_streams(uid);
                assert!(member_streams.is_some_and(|ids| ids.contains(&stream.id)),
                    "user {:?} in stream {} tree but user_streams doesn't reference that stream",
                    uid, stream.id);
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Property 2: Frozen streams reject placement
// ---------------------------------------------------------------------------

proptest! {
    #[test]
    fn frozen_streams_reject_placement(member_count in 1_usize..10) {
        let choice_config = StreamlineConfig {
            assignment_mode: StreamAssignmentMode::SponsorStream,
            enrollment_stream_choice: true,
            freeze_on_demotion: true,
        };
        let mut engine = StreamlineEngine::new(choice_config, 1000);
        for i in 1..=member_count {
            let sponsor = if i == 1 { uuid_from_index(0) } else { uuid_from_index(1) };
            engine
                .add_member(uuid_from_index(i), sponsor, 1000 + i as i64, None)
                .expect("fixture construction should succeed");
        }

        // Expand and freeze.
        engine.expand_streams(uuid_from_index(1), 3, 2000).expect("expansion should succeed");
        engine.update_stream_allowance(uuid_from_index(1), 1, 2000).expect("freeze should succeed");

        // Assert freeze precondition: exactly 2 frozen streams.
        let frozen_count = (2..=3_u32)
            .filter(|&sid| engine.get_stream(sid).is_some_and(|s| s.frozen))
            .count();
        prop_assert_eq!(frozen_count, 2, "expected 2 frozen streams after freeze to 1");

        // Attempting to place in any frozen stream should fail.
        let new_user = uuid_from_index(100);
        for sid in 2..=3_u32 {
            if let Some(stream) = engine.get_stream(sid) {
                if stream.frozen {
                    let result = engine.add_member(
                        new_user, uuid_from_index(1), 3000, Some(sid),
                    );
                    prop_assert!(result.is_err(), "placement in frozen stream {} should fail", sid);
                }
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Property 3: Stream count matches allowance
// ---------------------------------------------------------------------------

proptest! {
    #[test]
    fn stream_count_matches_allowance(
        initial_expand in 2_u32..6,
        freeze_to in 1_u32..3,
    ) {
        let mut engine = build_engine(1);
        let user = uuid_from_index(1);

        engine.expand_streams(user, initial_expand, 2000).expect("expansion should succeed");
        let active_before = engine.active_streams().count() as u32;
        prop_assert_eq!(active_before, initial_expand);

        let target = freeze_to.min(initial_expand);
        let _ = engine.update_stream_allowance(user, target, 2000).expect("allowance update should succeed");
        let active_after = engine.active_streams().count() as u32;
        prop_assert_eq!(active_after, target);
    }
}

// ---------------------------------------------------------------------------
// Property 4: No orphan streams
// ---------------------------------------------------------------------------

proptest! {
    #[test]
    fn no_orphan_streams(member_count in 1_usize..15) {
        let mut engine = build_engine(member_count);
        engine.expand_streams(uuid_from_index(1), 3, 2000).expect("expansion should succeed");

        let summaries = engine.list_streams();
        let mut all_owner_ids = HashSet::new();
        for s in &summaries {
            all_owner_ids.insert(s.owner_id);
            // Stream must exist.
            assert!(engine.get_stream(s.id).is_some(),
                "stream {} in list_streams but not accessible", s.id);
        }

        // Every owner should be a member or at least the bootstrap user.
        for oid in &all_owner_ids {
            if !oid.is_nil() {
                assert!(engine.contains_member(*oid),
                    "stream owner {:?} is not a member", oid);
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Property 5: Commission walk completeness (monoline)
// ---------------------------------------------------------------------------

proptest! {
    #[test]
    fn commission_walk_completeness(chain_depth in 2_usize..8) {
        use std::collections::HashMap;
        use network_engine::commission::calculate_streamline;
        use network_engine::commission::types::{DistributorSnapshot, VolumeSource};
        use network_engine::config::streamline::{StreamlineCommissionConfig, StreamlineLevel};
        use network_engine::config::StreamlineStructureConfig;

        let engine = build_engine(chain_depth);

        // Monoline: all thresholds are the lowest rank.
        let max_depth = (chain_depth - 1) as u8; // depth from bottom to top
        let levels: Vec<StreamlineLevel> = (1..=max_depth).map(|l| StreamlineLevel {
            level: l,
            min_rank: "member".to_string(),
            percent: 0.05,
        }).collect();

        let structure = StreamlineStructureConfig {
            name: "Test".to_string(),
            streamline_commission: StreamlineCommissionConfig {
                volume_to_dollar_multiplier: Some(1.0),
                max_depth,
                levels,
                stream_config: None,
            },
        };

        let plan = common::build_base_plan(
            common::permissive_eligibility(),
            network_engine::config::StructureConfig::Streamline(structure.clone()),
            "Test",
        );

        let mut snapshots = HashMap::new();
        for i in 1..=chain_depth {
            snapshots.insert(uuid_from_index(i), DistributorSnapshot {
                rank: "member".to_string(),
                personal_volume: 150.0,
                status: "active".to_string(),
                has_order_in_period: true,
            });
        }

        // Volume at the bottom of the chain.
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(chain_depth),
            cv_amount: 100.0,
        }];

        let earnings = calculate_streamline(&engine, &plan, &structure, &snapshots, &volume)
            .expect("calculation should not fail");

        // In a fully qualified monoline, earnings = min(chain_depth - 1, max_depth).
        let expected = (chain_depth - 1).min(max_depth as usize);
        prop_assert_eq!(earnings.len(), expected,
            "expected {} earnings for chain depth {}, max_depth {}, got {}",
            expected, chain_depth, max_depth, earnings.len());

        // No duplicate earners.
        let earner_ids: HashSet<_> = earnings.iter().map(|e| e.earner_id).collect();
        prop_assert_eq!(earner_ids.len(), earnings.len(), "duplicate earners detected");
    }
}

// ---------------------------------------------------------------------------
// Property 6: Commission dollar-value law (monoline)
// ---------------------------------------------------------------------------

proptest! {
    /// Every streamline earning's dollar amount must equal
    /// `cv_amount * broad_pct * volume_to_dollar_multiplier * level.percent`,
    /// where streamline hardcodes `broad_pct = 1.0` (streamline.rs:86) and the
    /// per-level rate comes from the config-built rate table (walk.rs:474-488).
    ///
    /// Companion to `commission_walk_completeness`: that pins the earner set,
    /// this pins the money. Stays in the clean monoline, fully-qualified
    /// regime (no compression/skip) where the law holds per earner. Uses
    /// distinct per-level percents so a level->rate mapping bug can't hide
    /// behind a uniform rate, and rebuilds `expected` from the same
    /// `structure.streamline_commission.levels` the calculator reads.
    #[test]
    fn commission_dollar_value_law(
        chain_depth in 2_usize..8,
        cv_amount in 1.0f64..10_000.0,
        multiplier in 0.1f64..5.0,
    ) {
        use std::collections::HashMap;
        use network_engine::commission::calculate_streamline;
        use network_engine::commission::types::{DistributorSnapshot, VolumeSource};
        use network_engine::config::streamline::{StreamlineCommissionConfig, StreamlineLevel};
        use network_engine::config::StreamlineStructureConfig;

        let engine = build_engine(chain_depth);

        // Monoline: every level qualifies at the lowest rank. Distinct
        // per-level percents (0.01, 0.02, ...) so the level->rate mapping is
        // actually exercised, not masked by a uniform rate.
        let max_depth = (chain_depth - 1) as u8;
        let levels: Vec<StreamlineLevel> = (1..=max_depth).map(|l| StreamlineLevel {
            level: l,
            min_rank: "member".to_string(),
            percent: 0.01 * l as f64,
        }).collect();

        let structure = StreamlineStructureConfig {
            name: "Test".to_string(),
            streamline_commission: StreamlineCommissionConfig {
                volume_to_dollar_multiplier: Some(multiplier),
                max_depth,
                levels,
                stream_config: None,
            },
        };

        let plan = common::build_base_plan(
            common::permissive_eligibility(),
            network_engine::config::StructureConfig::Streamline(structure.clone()),
            "Test",
        );

        let mut snapshots = HashMap::new();
        for i in 1..=chain_depth {
            snapshots.insert(uuid_from_index(i), DistributorSnapshot {
                rank: "member".to_string(),
                personal_volume: 150.0,
                status: "active".to_string(),
                has_order_in_period: true,
            });
        }

        // Volume at the bottom of the chain.
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(chain_depth),
            cv_amount,
        }];

        let earnings = calculate_streamline(&engine, &plan, &structure, &snapshots, &volume)
            .expect("calculation should not fail");

        // Fully-qualified monoline pays at least one level, so the law is
        // exercised on real earnings and the property keeps its teeth.
        prop_assert!(!earnings.is_empty(), "expected at least one earning");

        // streamline.rs:86 hardcodes broad_pct = 1.0.
        let broad_pct = 1.0f64;
        let cfg_levels = &structure.streamline_commission.levels;
        for earning in &earnings {
            let percent = cfg_levels
                .iter()
                .find(|l| l.level == earning.level)
                .map(|l| l.percent)
                .expect("earning level should map to a configured streamline level");

            let expected = cv_amount * broad_pct * multiplier * percent;
            prop_assert!(
                (earning.dollar_amount - expected).abs() < 1e-10,
                "level {}: dollar {} != cv {} * broad {} * mult {} * pct {} = {}",
                earning.level,
                earning.dollar_amount,
                cv_amount,
                broad_pct,
                multiplier,
                percent,
                expected
            );
        }
    }
}
