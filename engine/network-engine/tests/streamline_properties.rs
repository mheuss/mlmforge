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
        let _ = engine.add_member(uuid_from_index(i), sponsor, 1000 + i as i64, None);
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

        // Every tree member appears in user_streams.
        for stream in engine.active_streams() {
            for uid in stream.tree.user_ids() {
                assert!(engine.contains_member(uid),
                    "user {:?} in stream {} tree but not in user_streams", uid, stream.id);
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
        let mut engine = build_engine(member_count);

        // Expand and freeze.
        let _ = engine.expand_streams(uuid_from_index(1), 3, 2000);
        let _ = engine.update_stream_allowance(uuid_from_index(1), 1);

        // Attempting to place in any frozen stream should fail.
        let new_user = uuid_from_index(100);
        for sid in 2..=3_u32 {
            if let Some(stream) = engine.get_stream(sid) {
                if stream.frozen {
                    let result = engine.add_member(
                        new_user, uuid_from_index(1), 3000, Some(sid),
                    );
                    assert!(result.is_err(), "placement in frozen stream {} should fail", sid);
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

        let _ = engine.expand_streams(user, initial_expand, 2000);
        let active_before = engine.active_streams().count() as u32;
        prop_assert_eq!(active_before, initial_expand);

        let target = freeze_to.min(initial_expand);
        let _ = engine.update_stream_allowance(user, target);
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
        let _ = engine.expand_streams(uuid_from_index(1), 3, 2000);

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
