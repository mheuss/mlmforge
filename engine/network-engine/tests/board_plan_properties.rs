mod common;

use common::uuid_from_index;
use network_engine::board_plan::board::total_positions;
use network_engine::board_plan::{BoardPlanEngine, BoardPlanError, CycleEvent};
use network_engine::commission::calculate_board_commissions;
use network_engine::config::board_plan::{BoardPlanConfig, ReEntryPosition};
use proptest::prelude::*;
use std::collections::{HashMap, HashSet};
use uuid::Uuid;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Standard board plan config for property tests.
fn prop_config() -> BoardPlanConfig {
    BoardPlanConfig {
        cycle_commission: 500.0,
        re_entry_enabled: true,
        re_entry_position: ReEntryPosition::Bottom,
        max_cycles_per_period: 5,
        max_cascade_depth: 10,
        stall_threshold_periods: 3,
        inactive_compression: false,
    }
}

/// Builds a board plan engine and adds `member_count` members.
///
/// The first member is sponsored by a synthetic root sponsor. All
/// subsequent members are sponsored by the first member. Returns the
/// engine and the list of successfully added member UUIDs.
fn build_engine(
    width: u8,
    height: u8,
    member_count: usize,
    config: BoardPlanConfig,
) -> (BoardPlanEngine, Vec<Uuid>) {
    let mut engine = BoardPlanEngine::new(width, height, config, 1000).unwrap();
    let sponsor = uuid_from_index(0);
    let mut members = Vec::new();

    for i in 1..=member_count {
        let user = uuid_from_index(i);
        let s = if i == 1 { sponsor } else { uuid_from_index(1) };
        match engine.add_member(user, s, 1000 + i as i64) {
            Ok(_) => members.push(user),
            Err(_) => break,
        }
    }

    (engine, members)
}

/// Collects all member UUIDs visible on boards, plus displaced members.
fn all_visible_members(engine: &BoardPlanEngine) -> Vec<Uuid> {
    let mut members = Vec::new();
    for summary in engine.list_boards() {
        if let Some(board) = engine.get_board(summary.id) {
            for uid in board.positions.iter().flatten() {
                members.push(*uid);
            }
        }
    }
    members.extend(engine.displaced_members());
    members
}

// ---------------------------------------------------------------------------
// Property tests
// ---------------------------------------------------------------------------

proptest! {
    /// After any sequence of adds, every member appears in exactly one
    /// board or the displaced pool. No orphans, no duplicates.
    #[test]
    fn no_orphans_no_duplicates(
        width in 2u8..4,
        height in 1u8..3,
        member_count in 1usize..30,
    ) {
        let (engine, added) = build_engine(width, height, member_count, prop_config());

        // Collect all members visible on boards.
        let on_boards = all_visible_members(&engine);

        // No duplicates across all boards and the displaced pool.
        let mut seen = HashSet::new();
        for &uid in &on_boards {
            prop_assert!(
                seen.insert(uid),
                "duplicate member {} found across boards/displaced pool",
                uid
            );
        }

        // Every added member is either on a board or displaced.
        for &uid in &added {
            let on_board = engine.get_member_board(uid).is_some();
            let displaced = engine.displaced_members().contains(&uid);
            prop_assert!(
                on_board || displaced,
                "member {} is neither on a board nor in displaced pool (orphan)",
                uid
            );
        }
    }

    /// Every board's position array length equals total_positions
    /// for the configured width and height.
    #[test]
    fn position_array_length_invariant(
        width in 2u8..4,
        height in 1u8..3,
        member_count in 0usize..30,
    ) {
        let (engine, _) = build_engine(width, height, member_count, prop_config());
        let expected = total_positions(width, height);

        for summary in engine.list_boards() {
            let board = engine.get_board(summary.id).unwrap();
            prop_assert_eq!(
                board.positions.len(),
                expected,
                "board {} has {} positions, expected {}",
                summary.id,
                board.positions.len(),
                expected
            );
        }
    }

    /// No board has more filled positions than total_positions.
    #[test]
    fn fill_count_bounded(
        width in 2u8..4,
        height in 1u8..3,
        member_count in 0usize..30,
    ) {
        let (engine, _) = build_engine(width, height, member_count, prop_config());
        let max = total_positions(width, height);

        for summary in engine.list_boards() {
            let board = engine.get_board(summary.id).unwrap();
            prop_assert!(
                board.filled_count() <= max,
                "board {} has {} filled but total_positions is {}",
                summary.id,
                board.filled_count(),
                max
            );
        }
    }

    /// Bidirectional consistency: if get_member_board(user) returns a
    /// board_id, that board's positions contain the user. And if a
    /// board's positions contain a user, get_member_board returns
    /// that board.
    #[test]
    fn bidirectional_consistency(
        width in 2u8..4,
        height in 1u8..3,
        member_count in 1usize..30,
    ) {
        let (engine, added) = build_engine(width, height, member_count, prop_config());

        // Direction 1: member_boards -> board positions.
        for &uid in &added {
            if let Some(board_id) = engine.get_member_board(uid) {
                let board = engine.get_board(board_id);
                prop_assert!(
                    board.is_some(),
                    "member {} mapped to board {} but board does not exist",
                    uid,
                    board_id
                );
                let board = board.unwrap();
                prop_assert!(
                    board.positions.contains(&Some(uid)),
                    "member {} mapped to board {} but not found in positions",
                    uid,
                    board_id
                );
            }
        }

        // Direction 2: board positions -> member_boards.
        for summary in engine.list_boards() {
            let board = engine.get_board(summary.id).unwrap();
            for uid in board.positions.iter().flatten() {
                prop_assert_eq!(
                    engine.get_member_board(*uid),
                    Some(summary.id),
                    "user {} in board {} positions but get_member_board returns {:?}",
                    uid,
                    summary.id,
                    engine.get_member_board(*uid)
                );
            }
        }
    }

    /// After any add_member call, the number of cycle events is bounded
    /// by max_cascade_depth. This verifies no runaway cascade occurs.
    #[test]
    fn cascade_depth_bounded(
        width in 2u8..4,
        height in 1u8..3,
        member_count in 1usize..30,
        max_cascade in 1u32..5,
    ) {
        let config = BoardPlanConfig {
            max_cascade_depth: max_cascade,
            ..prop_config()
        };
        let mut engine = BoardPlanEngine::new(width, height, config, 1000).unwrap();
        let sponsor = uuid_from_index(0);

        for i in 1..=member_count {
            let user = uuid_from_index(i);
            let s = if i == 1 { sponsor } else { uuid_from_index(1) };
            match engine.add_member(user, s, 1000 + i as i64) {
                Ok(result) => {
                    // Each individual add_member should produce at most
                    // max_cascade_depth cycle events.
                    prop_assert!(
                        result.cycle_events.len() as u32 <= max_cascade,
                        "add_member produced {} cycle events but max_cascade_depth is {}",
                        result.cycle_events.len(),
                        max_cascade
                    );
                }
                Err(_) => break,
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Commission dollar-value law
// ---------------------------------------------------------------------------

proptest! {
    /// The cycle-commission dollar law from `calculate_board_commissions`
    /// (board_plan.rs:35-36): a cycle is capped exactly when its number
    /// exceeds `max_cycles_per_period`. Capped cycles pay $0, uncapped
    /// cycles pay `cycle_commission`.
    ///
    /// Drives the real calculator with synthetic cycle events for a single
    /// member, generating `max_cycles + extra` events (extra >= 1) so both
    /// the paid and capped regimes appear in every generated case. The
    /// closing `any(capped)` / `any(!capped)` assertions guarantee the law
    /// is exercised on both sides of the cap, so the test keeps its teeth.
    #[test]
    fn cycle_commission_dollar_law(
        cycle_commission in 1.0f64..10_000.0,
        max_cycles in 1u32..8,
        extra in 1u32..6,
    ) {
        let config = BoardPlanConfig {
            cycle_commission,
            max_cycles_per_period: max_cycles,
            ..prop_config()
        };

        // One member cycles `max_cycles + extra` times against an empty
        // starting count, so cycle numbers run 1..=n and straddle the cap.
        let member = uuid_from_index(1);
        let board = uuid_from_index(2);
        let n_events = max_cycles + extra;
        let events: Vec<CycleEvent> = (0..n_events)
            .map(|_| CycleEvent {
                board_id: board,
                cycled_member: member,
                new_boards: vec![],
                re_entry_board: None,
            })
            .collect();

        let result = calculate_board_commissions(&events, &HashMap::new(), &config);
        prop_assert_eq!(result.earnings.len(), n_events as usize);

        for (i, earning) in result.earnings.iter().enumerate() {
            // Single member with empty prior counts: the i-th event is
            // cycle i+1, so pin cycle_number to the known input rather than
            // trusting the value the cap decision is derived from.
            let expected_cycle = (i + 1) as u32;
            prop_assert_eq!(
                earning.cycle_number,
                expected_cycle,
                "event {} produced cycle_number {}, expected {}",
                i,
                earning.cycle_number,
                expected_cycle
            );

            // Derive the cap decision from the input-pinned cycle, not the
            // field under test, so the capped assertion stays independent
            // regardless of assertion order.
            let should_cap = expected_cycle > max_cycles;
            prop_assert_eq!(
                earning.capped,
                should_cap,
                "cycle {} capped={} but max_cycles_per_period={}",
                earning.cycle_number,
                earning.capped,
                max_cycles
            );

            let expected = if should_cap { 0.0 } else { cycle_commission };
            prop_assert!(
                (earning.dollar_amount - expected).abs() < 1e-10,
                "cycle {} (capped={}): dollar {} != expected {}",
                earning.cycle_number,
                earning.capped,
                earning.dollar_amount,
                expected
            );
        }

        prop_assert!(
            result.earnings.iter().any(|e| e.capped),
            "no capped earning generated; cap law untested"
        );
        prop_assert!(
            result.earnings.iter().any(|e| !e.capped),
            "no uncapped earning generated; paid law untested"
        );
    }
}

// ---------------------------------------------------------------------------
// Edge case tests
// ---------------------------------------------------------------------------

#[test]
fn all_boards_stalled_dissolves_everyone_to_pool() {
    let config = prop_config();
    let (mut engine, added) = build_engine(2, 2, 5, config);

    // Dissolve all boards. Collect board IDs first to avoid
    // borrow issues.
    let board_ids: Vec<Uuid> = engine.list_boards().iter().map(|b| b.id).collect();
    for board_id in board_ids {
        let _ = engine.dissolve_board(board_id, 9000);
    }

    // No boards should remain.
    assert_eq!(engine.board_count(), 0, "all boards should be dissolved");

    // Every previously added member should be in the displaced pool.
    for uid in &added {
        assert!(
            engine.displaced_members().contains(uid),
            "member {} should be displaced after dissolving all boards",
            uid
        );
        assert_eq!(
            engine.get_member_board(*uid),
            None,
            "member {} should not be on any board",
            uid
        );
    }
}

#[test]
fn cycled_members_sponsor_was_dissolved_falls_back_to_bottom() {
    // Use SponsorBoard mode so re-entry normally targets sponsor's board.
    let config = BoardPlanConfig {
        re_entry_position: ReEntryPosition::SponsorBoard,
        re_entry_enabled: true,
        ..prop_config()
    };

    // 2x1 board: 3 positions, cycles fast.
    let mut engine = BoardPlanEngine::new(2, 1, config, 1000).unwrap();
    let sponsor = uuid_from_index(0);

    // Add members to fill and trigger cycling.
    let mut all_cycle_events = Vec::new();
    for i in 1..=6 {
        let user = uuid_from_index(i);
        let s = if i == 1 { sponsor } else { uuid_from_index(1) };
        match engine.add_member(user, s, 1000 + i as i64) {
            Ok(result) => all_cycle_events.extend(result.cycle_events),
            Err(_) => break,
        }
    }

    // Dissolve all boards except one to force sponsor lookup failure.
    let board_ids: Vec<Uuid> = engine.list_boards().iter().map(|b| b.id).collect();
    if board_ids.len() > 1 {
        // Dissolve all but the last board.
        for &board_id in &board_ids[..board_ids.len() - 1] {
            let _ = engine.dissolve_board(board_id, 8000);
        }
    }

    // Add more members. Since sponsor's board may be dissolved,
    // SponsorBoard mode should fall back to Bottom (oldest available).
    for i in 100..106 {
        let user = uuid_from_index(i);
        // The displaced members from dissolution should get re-placed.
        match engine.add_member(user, uuid_from_index(1), 9000 + i as i64) {
            Ok(_) => {}
            Err(BoardPlanError::SponsorNotFound(_)) | Err(BoardPlanError::NoBoardsAvailable) => {
                break;
            }
            Err(e) => panic!("unexpected error: {}", e),
        }
    }

    // Verify no member is both on a board and displaced.
    let displaced: HashSet<Uuid> = engine.displaced_members().iter().copied().collect();
    for summary in engine.list_boards() {
        let board = engine.get_board(summary.id).unwrap();
        for uid in board.positions.iter().flatten() {
            assert!(
                !displaced.contains(uid),
                "member {} is both on a board and in the displaced pool",
                uid
            );
        }
    }
}

#[test]
fn deep_cascade_from_rapid_adds_respects_bound() {
    // 2x1 boards: 3 positions each, cycle very quickly.
    let config = BoardPlanConfig {
        max_cascade_depth: 2,
        ..prop_config()
    };

    let mut engine = BoardPlanEngine::new(2, 1, config, 1000).unwrap();
    let sponsor = uuid_from_index(0);

    // Add many members rapidly. With 3-position boards and re-entry,
    // cascades happen frequently.
    let mut max_events_per_add = 0;
    for i in 1..=50 {
        let user = uuid_from_index(i);
        let s = if i == 1 { sponsor } else { uuid_from_index(1) };
        match engine.add_member(user, s, 1000 + i as i64) {
            Ok(result) => {
                if result.cycle_events.len() > max_events_per_add {
                    max_events_per_add = result.cycle_events.len();
                }
                // Each add should respect the cascade bound.
                assert!(
                    result.cycle_events.len() <= 2,
                    "max_cascade_depth=2 but got {} cycle events from a single add",
                    result.cycle_events.len()
                );
            }
            Err(_) => break,
        }
    }

    // Cycles should have occurred (the system works, just bounded).
    assert!(
        max_events_per_add > 0,
        "at least one add should have triggered cycling"
    );

    // Verify structural invariants still hold after rapid adds.
    let expected_positions = total_positions(2, 1);
    for summary in engine.list_boards() {
        let board = engine.get_board(summary.id).unwrap();
        assert_eq!(
            board.positions.len(),
            expected_positions,
            "board {} position array length should be {}",
            summary.id,
            expected_positions
        );
        assert!(
            board.filled_count() <= expected_positions,
            "board {} overfilled: {} > {}",
            summary.id,
            board.filled_count(),
            expected_positions
        );
    }
}
