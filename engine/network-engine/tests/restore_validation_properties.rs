mod common;

use common::uuid_from_index;
use network_engine::config::matrix::SpilloverDirection;
use network_engine::config::streamline::StreamAssignmentMode;
use network_engine::streamline::engine::{StreamlineConfig, StreamlineEngine};
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::matrix::{MatrixTree, PruningMode};
use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use uuid::Uuid;

/// One random operation: a kind and three picks resolved against live members.
type Op = (u8, usize, usize, usize);

fn ops() -> impl Strategy<Value = Vec<Op>> {
    prop::collection::vec((0u8..4, 0usize..64, 0usize..64, 0usize..8), 1..80)
}

fn fresh_id(next: &mut usize) -> Uuid {
    *next += 1;
    uuid_from_index(*next)
}

fn unilevel_step(tree: &mut UnilevelTree, live: &mut Vec<Uuid>, next: &mut usize, op: Op) {
    let (kind, a, b, _) = op;
    if live.is_empty() {
        let id = fresh_id(next);
        tree.add_root(id, *next as i64).unwrap();
        live.push(id);
        return;
    }
    if kind % 2 == 0 {
        let id = fresh_id(next);
        let parent = live[a % live.len()];
        let sponsor = live[b % live.len()];
        if tree.add_node(id, parent, sponsor, *next as i64).is_ok() {
            live.push(id);
        }
    } else {
        let target = live[a % live.len()];
        if tree.remove_node(target).is_ok() {
            live.retain(|&u| u != target);
        }
    }
}

fn binary_step(tree: &mut BinaryTree, live: &mut Vec<Uuid>, next: &mut usize, op: Op) {
    let (kind, a, b, c) = op;
    if live.is_empty() {
        let id = fresh_id(next);
        tree.add_root(id, *next as i64).unwrap();
        live.push(id);
        return;
    }
    if kind % 2 == 0 {
        let id = fresh_id(next);
        let parent = live[a % live.len()];
        let sponsor = live[b % live.len()];
        if tree
            .add_node(id, parent, c % 2, sponsor, *next as i64)
            .is_ok()
        {
            live.push(id);
        }
    } else {
        let target = live[a % live.len()];
        if tree.remove_node(target).is_ok() {
            live.retain(|&u| u != target);
        }
    }
}

fn matrix_step(tree: &mut MatrixTree, live: &mut Vec<Uuid>, next: &mut usize, op: Op) {
    let (kind, a, b, c) = op;
    if live.is_empty() {
        let id = fresh_id(next);
        tree.add_root(id, *next as i64).unwrap();
        live.push(id);
        return;
    }
    let width = tree.width();
    match kind {
        0 => {
            let id = fresh_id(next);
            let sponsor = live[b % live.len()];
            if tree.add_node(id, sponsor, *next as i64).is_ok() {
                live.push(id);
            }
        }
        1 => {
            let id = fresh_id(next);
            let sponsor = live[b % live.len()];
            let parent = live[a % live.len()];
            let position = (c % usize::from(width)) as u8;
            if tree
                .add_node_at(id, sponsor, parent, position, *next as i64)
                .is_ok()
            {
                live.push(id);
            }
        }
        2 => {
            let target = live[a % live.len()];
            let mode = if c % 2 == 0 {
                PruningMode::PromoteEarliest
            } else {
                PruningMode::HoldingTank
            };
            if let Ok(result) = tree.remove_node(target, mode) {
                live.retain(|u| *u != result.removed && !result.moved_to_tank.contains(u));
            }
        }
        _ => {
            let tank: Vec<Uuid> = tree
                .get_holding_tank(None)
                .iter()
                .map(|entry| entry.user_id)
                .collect();
            if tank.is_empty() {
                return;
            }
            let user = tank[b % tank.len()];
            let parent = live[a % live.len()];
            let position = (c % usize::from(width)) as u8;
            if tree.place_from_tank(user, parent, position).is_ok() {
                live.push(user);
            }
        }
    }
}

fn streamline_step(engine: &mut StreamlineEngine, live: &mut Vec<Uuid>, next: &mut usize, op: Op) {
    let (kind, a, b, c) = op;
    let timestamp = 1000 + *next as i64;
    if live.is_empty() || kind == 0 {
        let id = fresh_id(next);
        let sponsor = if live.is_empty() {
            uuid_from_index(0)
        } else {
            live[b % live.len()]
        };
        let stream_override = if c % 3 == 0 {
            Some((c % 4) as u32 + 1)
        } else {
            None
        };
        if engine
            .add_member(id, sponsor, timestamp, stream_override)
            .is_ok()
        {
            live.push(id);
        }
        return;
    }
    let target = live[a % live.len()];
    match kind {
        1 => {
            if engine.remove_member(target, timestamp).is_ok() {
                live.retain(|&u| u != target);
            }
        }
        2 => {
            let _ = engine.expand_streams(target, (c % 4) as u32 + 1, timestamp);
        }
        _ => {
            let _ = engine.update_stream_allowance(target, (c % 4) as u32 + 1, timestamp);
        }
    }
}

proptest! {
    #[test]
    fn unilevel_mutators_leave_a_restorable_tree(ops in ops()) {
        let mut tree = UnilevelTree::new();
        let (mut live, mut next) = (Vec::new(), 0);
        for op in ops {
            unilevel_step(&mut tree, &mut live, &mut next, op);
            prop_assert_eq!(
                tree.validate_restored(),
                Ok(()),
                "after {:?}; state {}",
                op,
                serde_json::to_string(&tree).unwrap()
            );
        }
        let restored: UnilevelTree =
            serde_json::from_str(&serde_json::to_string(&tree).unwrap()).unwrap();
        prop_assert_eq!(restored.validate_restored(), Ok(()));
    }

    #[test]
    fn binary_mutators_leave_a_restorable_tree(ops in ops()) {
        let mut tree = BinaryTree::new();
        let (mut live, mut next) = (Vec::new(), 0);
        for op in ops {
            binary_step(&mut tree, &mut live, &mut next, op);
            prop_assert_eq!(
                tree.validate_restored(),
                Ok(()),
                "after {:?}; state {}",
                op,
                serde_json::to_string(&tree).unwrap()
            );
        }
        let restored: BinaryTree =
            serde_json::from_str(&serde_json::to_string(&tree).unwrap()).unwrap();
        prop_assert_eq!(restored.validate_restored(), Ok(()));
    }

    #[test]
    fn matrix_mutators_leave_a_restorable_tree(width in 2u8..5, ops in ops()) {
        let mut tree = MatrixTree::new(width, SpilloverDirection::BreadthFirst).unwrap();
        let (mut live, mut next) = (Vec::new(), 0);
        for op in ops {
            matrix_step(&mut tree, &mut live, &mut next, op);
            prop_assert_eq!(
                tree.validate_restored(),
                Ok(()),
                "after {:?}; state {}",
                op,
                serde_json::to_string(&tree).unwrap()
            );
        }
        let restored: MatrixTree =
            serde_json::from_str(&serde_json::to_string(&tree).unwrap()).unwrap();
        prop_assert_eq!(restored.validate_restored(), Ok(()));
    }

    #[test]
    fn streamline_mutators_leave_a_restorable_engine(
        round_robin in any::<bool>(),
        choice in any::<bool>(),
        freeze in any::<bool>(),
        ops in ops(),
    ) {
        let config = StreamlineConfig {
            assignment_mode: if round_robin {
                StreamAssignmentMode::RoundRobin
            } else {
                StreamAssignmentMode::SponsorStream
            },
            enrollment_stream_choice: choice,
            freeze_on_demotion: freeze,
        };
        let mut engine = StreamlineEngine::new(config, 1000);
        let (mut live, mut next) = (Vec::new(), 0);
        for op in ops {
            streamline_step(&mut engine, &mut live, &mut next, op);
            prop_assert_eq!(
                engine.validate_restored(),
                Ok(()),
                "after {:?}; state {}",
                op,
                serde_json::to_string(&engine).unwrap()
            );
        }
        let restored: StreamlineEngine =
            serde_json::from_str(&serde_json::to_string(&engine).unwrap()).unwrap();
        prop_assert_eq!(restored.validate_restored(), Ok(()));
    }
}
