mod common;

use common::uuid_from_index;
use network_engine::board_plan::BoardPlanEngine;
use network_engine::config::board_plan::{BoardPlanConfig, ReEntryPosition};
use network_engine::d;
use proptest::prelude::*;
use std::num::NonZeroU32;
use uuid::Uuid;

type Op = (u8, usize, usize);

fn members(engine: &BoardPlanEngine) -> Vec<Uuid> {
    let mut found: Vec<Uuid> = engine
        .list_boards()
        .iter()
        .filter_map(|summary| engine.get_board(summary.id))
        .flat_map(|board| board.members())
        .collect();
    found.extend_from_slice(engine.displaced_members());
    found.sort();
    found
}

/// Occupied boards ordered by their lowest occupant, so the order does not follow hash order.
fn occupied_boards(engine: &BoardPlanEngine) -> Vec<Uuid> {
    let mut boards: Vec<(Uuid, Uuid)> = engine
        .list_boards()
        .iter()
        .filter_map(|summary| engine.get_board(summary.id))
        .filter_map(|board| board.members().into_iter().min().map(|low| (low, board.id)))
        .collect();
    boards.sort();
    boards.into_iter().map(|(_, id)| id).collect()
}

fn anchor() -> Uuid {
    uuid_from_index(0)
}

fn full_boards(engine: &BoardPlanEngine) -> Vec<Uuid> {
    engine
        .list_boards()
        .iter()
        .filter(|summary| summary.filled_count == summary.total_positions)
        .map(|summary| summary.id)
        .collect()
}

fn board_config(cascade: u32, re_entry: bool, sponsor_board: bool) -> BoardPlanConfig {
    BoardPlanConfig {
        cycle_commission: d!(500.0),
        re_entry_enabled: re_entry,
        re_entry_position: if sponsor_board {
            ReEntryPosition::SponsorBoard
        } else {
            ReEntryPosition::Bottom
        },
        max_cycles_per_period: 5,
        max_cascade_depth: NonZeroU32::new(cascade).unwrap(),
        stall_threshold_periods: 3,
        inactive_compression: false,
    }
}

/// Each board's seq, timestamps and occupants in seq order, then the
/// displaced pool.
type Layout = (Vec<(u64, i64, i64, Vec<Option<Uuid>>)>, Vec<Uuid>);

fn layout(engine: &BoardPlanEngine) -> Layout {
    let mut boards: Vec<(u64, i64, i64, Vec<Option<Uuid>>)> = engine
        .list_boards()
        .iter()
        .filter_map(|summary| engine.get_board(summary.id))
        .map(|board| {
            (
                board.seq,
                board.created_at,
                board.last_activity_at,
                board.positions.clone(),
            )
        })
        .collect();
    boards.sort_by_key(|(seq, ..)| *seq);
    (boards, engine.displaced_members().to_vec())
}

/// Runs one generated operation. Returns true when an operation ran and
/// returned `Ok`.
fn board_step(engine: &mut BoardPlanEngine, next: &mut usize, op: Op) -> bool {
    let (kind, a, b) = op;
    *next += 1;
    let timestamp = *next as i64;
    let present = members(engine);
    let removable: Vec<Uuid> = present
        .iter()
        .copied()
        .filter(|member| *member != anchor())
        .collect();
    match kind {
        0 | 1 => {
            let id = uuid_from_index(*next);
            let sponsor = present[b % present.len()];
            engine.add_member(id, sponsor, timestamp).is_ok()
        }
        2 => {
            !removable.is_empty()
                && engine
                    .remove_member(removable[a % removable.len()], timestamp)
                    .is_ok()
        }
        3 => {
            if removable.is_empty() {
                return false;
            }
            let picked: Vec<Uuid> = (0..1 + b % 3)
                .map(|k| removable[(a + k * b) % removable.len()])
                .collect();
            engine.compress_inactive(picked, timestamp).is_ok()
        }
        _ => {
            let boards = occupied_boards(engine);
            engine.board_count() > 1
                && !boards.is_empty()
                && engine
                    .dissolve_board(boards[a % boards.len()], timestamp)
                    .is_ok()
        }
    }
}

proptest! {
    #[test]
    fn board_mutators_leave_a_restorable_engine(
        width in 2u8..4,
        height in 1u8..3,
        cascade in prop_oneof![3 => Just(1u32), 1 => 2u32..4],
        re_entry in prop::bool::weighted(d!(0.8)),
        sponsor_board in any::<bool>(),
        ops in prop::collection::vec((0u8..5, 0usize..64, 0usize..64), 1..120),
    ) {
        let config = board_config(cascade, re_entry, sponsor_board);
        let mut engine = BoardPlanEngine::new(width, height, config, 0).unwrap();
        engine.add_member(anchor(), anchor(), 0).unwrap();
        let mut next = 0;
        for op in ops {
            let full_before = full_boards(&engine);
            let succeeded = board_step(&mut engine, &mut next, op);
            if succeeded {
                let still_full: Vec<Uuid> = full_boards(&engine)
                    .into_iter()
                    .filter(|board_id| full_before.contains(board_id))
                    .collect();
                prop_assert!(
                    still_full.is_empty(),
                    "boards {:?} were full before {:?} and still are after it returned ok",
                    still_full,
                    op
                );
            }
            prop_assert_eq!(
                engine.validate_restored(),
                Ok(()),
                "after {:?}; state {}",
                op,
                serde_json::to_string(&engine).unwrap()
            );
        }
        let restored: BoardPlanEngine =
            serde_json::from_str(&serde_json::to_string(&engine).unwrap()).unwrap();
        prop_assert_eq!(restored.validate_restored(), Ok(()));
    }

    #[test]
    fn one_operation_sequence_gives_one_layout(
        width in 2u8..4,
        height in 1u8..3,
        cascade in 1u32..4,
        re_entry in any::<bool>(),
        sponsor_board in any::<bool>(),
        ops in prop::collection::vec((0u8..5, 0usize..64, 0usize..64), 1..120),
    ) {
        let mut first: Option<Vec<(bool, Layout)>> = None;
        for _ in 0..4 {
            let mut engine = BoardPlanEngine::new(
                width,
                height,
                board_config(cascade, re_entry, sponsor_board),
                0,
            )
            .unwrap();
            engine.add_member(anchor(), anchor(), 0).unwrap();
            let mut next = 0;
            let mut layouts = Vec::new();
            for op in &ops {
                let succeeded = board_step(&mut engine, &mut next, *op);
                layouts.push((succeeded, layout(&engine)));
            }
            match &first {
                None => first = Some(layouts),
                Some(expected) => prop_assert_eq!(&layouts, expected),
            }
        }
    }
}
