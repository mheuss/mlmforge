//! Board plan cycle commission calculator.
//!
//! Computes fixed-amount payouts for cycle events. Each time a member
//! cycles out of a board, they earn `cycle_commission` from the plan
//! config. A per-period cap (`max_cycles_per_period`) limits how many
//! paid cycles a member can accumulate. Cycles beyond the cap are
//! recorded with `capped=true` and `dollar_amount=0`.

use std::collections::HashMap;

use uuid::Uuid;

use crate::board_plan::types::{BoardCommissionResult, BoardCycleEarning, CycleEvent};
use crate::config::board_plan::BoardPlanConfig;

/// Calculate board cycle commissions for a set of cycle events.
///
/// Increments each member's cycle count for the period. If the count
/// exceeds `max_cycles_per_period`, the earning is zero and marked
/// as capped. Prior cycle counts from earlier in the period are
/// passed via `period_cycle_counts`.
pub fn calculate_board_commissions(
    cycle_events: &[CycleEvent],
    period_cycle_counts: &HashMap<Uuid, u32>,
    config: &BoardPlanConfig,
) -> BoardCommissionResult {
    let mut counts: HashMap<Uuid, u32> = period_cycle_counts.clone();
    let mut earnings = Vec::new();

    for event in cycle_events {
        let member = event.cycled_member;
        let count = counts.entry(member).or_insert(0);
        *count += 1;

        let capped = *count > config.max_cycles_per_period;
        let dollar_amount = if capped { 0.0 } else { config.cycle_commission };

        earnings.push(BoardCycleEarning {
            earner_id: member,
            board_id: event.board_id,
            dollar_amount,
            cycle_number: *count,
            capped,
        });
    }

    BoardCommissionResult {
        earnings,
        updated_cycle_counts: counts,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_uuid(n: u8) -> Uuid {
        Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
    }

    fn test_config() -> BoardPlanConfig {
        BoardPlanConfig {
            cycle_commission: 500.0,
            re_entry_enabled: true,
            re_entry_position: crate::config::board_plan::ReEntryPosition::Bottom,
            max_cycles_per_period: 3,
            max_cascade_depth: 10,
            stall_threshold_periods: 3,
            inactive_compression: false,
        }
    }

    fn make_cycle_event(member: Uuid) -> CycleEvent {
        CycleEvent {
            board_id: Uuid::new_v4(),
            cycled_member: member,
            earned_commission: true,
            new_boards: vec![],
            re_entry_board: None,
        }
    }

    #[test]
    fn basic_cycle_earning() {
        let config = test_config();
        let member = test_uuid(1);
        let events = vec![make_cycle_event(member)];
        let counts = HashMap::new();

        let result = calculate_board_commissions(&events, &counts, &config);

        assert_eq!(result.earnings.len(), 1);
        assert_eq!(result.earnings[0].earner_id, member);
        assert_eq!(result.earnings[0].dollar_amount, 500.0);
        assert_eq!(result.earnings[0].cycle_number, 1);
        assert!(!result.earnings[0].capped);
        assert_eq!(*result.updated_cycle_counts.get(&member).unwrap(), 1);
    }

    #[test]
    fn per_period_cap_zeroes_earnings() {
        let config = test_config(); // max_cycles_per_period = 3
        let member = test_uuid(1);
        let events: Vec<CycleEvent> = (0..5).map(|_| make_cycle_event(member)).collect();
        let counts = HashMap::new();

        let result = calculate_board_commissions(&events, &counts, &config);

        assert_eq!(result.earnings.len(), 5);

        // First 3 are paid.
        for i in 0..3 {
            assert_eq!(result.earnings[i].dollar_amount, 500.0);
            assert!(!result.earnings[i].capped);
            assert_eq!(result.earnings[i].cycle_number, (i + 1) as u32);
        }

        // Last 2 are capped.
        for i in 3..5 {
            assert_eq!(result.earnings[i].dollar_amount, 0.0);
            assert!(result.earnings[i].capped);
            assert_eq!(result.earnings[i].cycle_number, (i + 1) as u32);
        }

        assert_eq!(*result.updated_cycle_counts.get(&member).unwrap(), 5);
    }

    #[test]
    fn existing_cycle_counts_carried_forward() {
        let config = test_config(); // max_cycles_per_period = 3
        let member = test_uuid(1);
        let events = vec![make_cycle_event(member)];

        // Member already has 2 cycles from earlier in the period.
        let mut counts = HashMap::new();
        counts.insert(member, 2);

        let result = calculate_board_commissions(&events, &counts, &config);

        assert_eq!(result.earnings.len(), 1);
        // This is cycle #3 (2 prior + 1 new), still within cap.
        assert_eq!(result.earnings[0].cycle_number, 3);
        assert_eq!(result.earnings[0].dollar_amount, 500.0);
        assert!(!result.earnings[0].capped);
        assert_eq!(*result.updated_cycle_counts.get(&member).unwrap(), 3);
    }

    #[test]
    fn multiple_members_independent_counts() {
        let config = test_config();
        let alice = test_uuid(1);
        let bob = test_uuid(2);
        let events = vec![make_cycle_event(alice), make_cycle_event(bob)];
        let counts = HashMap::new();

        let result = calculate_board_commissions(&events, &counts, &config);

        assert_eq!(result.earnings.len(), 2);

        assert_eq!(result.earnings[0].earner_id, alice);
        assert_eq!(result.earnings[0].dollar_amount, 500.0);
        assert_eq!(result.earnings[0].cycle_number, 1);

        assert_eq!(result.earnings[1].earner_id, bob);
        assert_eq!(result.earnings[1].dollar_amount, 500.0);
        assert_eq!(result.earnings[1].cycle_number, 1);

        assert_eq!(*result.updated_cycle_counts.get(&alice).unwrap(), 1);
        assert_eq!(*result.updated_cycle_counts.get(&bob).unwrap(), 1);
    }

    #[test]
    fn empty_events_returns_empty() {
        let config = test_config();
        let events: Vec<CycleEvent> = vec![];
        let counts = HashMap::new();

        let result = calculate_board_commissions(&events, &counts, &config);

        assert!(result.earnings.is_empty());
        assert!(result.updated_cycle_counts.is_empty());
    }
}
