mod common;

use common::uuid_from_index;
use network_engine::board_plan::BoardPlanEngine;
use network_engine::config::board_plan::{BoardPlanConfig, ReEntryPosition};
use proptest::prelude::*;
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

/// Occupied boards ordered by their lowest occupant, which the strategy controls.
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

/// The member every run starts with, which removal never picks.
fn anchor() -> Uuid {
    uuid_from_index(0)
}

fn board_step(engine: &mut BoardPlanEngine, next: &mut usize, op: Op) {
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
            let sponsor = if present.is_empty() {
                id
            } else {
                present[b % present.len()]
            };
            let _ = engine.add_member(id, sponsor, timestamp);
        }
        2 => {
            if !removable.is_empty() {
                let _ = engine.remove_member(removable[a % removable.len()], timestamp);
            }
        }
        3 => {
            if !removable.is_empty() {
                let _ = engine.compress_inactive(vec![removable[a % removable.len()]], timestamp);
            }
        }
        _ => {
            let boards = occupied_boards(engine);
            if engine.board_count() > 1 && !boards.is_empty() {
                let _ = engine.dissolve_board(boards[a % boards.len()], timestamp);
            }
        }
    }
}

proptest! {
    #[test]
    fn board_mutators_leave_a_restorable_engine(
        width in 2u8..4,
        height in 1u8..3,
        cascade in 1u32..4,
        re_entry in any::<bool>(),
        sponsor_board in any::<bool>(),
        ops in prop::collection::vec((0u8..5, 0usize..64, 0usize..64), 1..120),
    ) {
        let config = BoardPlanConfig {
            cycle_commission: 500.0,
            re_entry_enabled: re_entry,
            re_entry_position: if sponsor_board {
                ReEntryPosition::SponsorBoard
            } else {
                ReEntryPosition::Bottom
            },
            max_cycles_per_period: 5,
            max_cascade_depth: cascade,
            stall_threshold_periods: 3,
            inactive_compression: false,
        };
        let mut engine = BoardPlanEngine::new(width, height, config, 0).unwrap();
        engine.add_member(anchor(), anchor(), 0).unwrap();
        let mut next = 0;
        for op in ops {
            board_step(&mut engine, &mut next, op);
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
}
