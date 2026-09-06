//! Walk index assignment.
//!
//! Design 029 requires walk indexes to come from a defined total order across
//! the whole response, never from the order walks happened to be emitted in.
//! For streamline that emission order is a `HashMap` iteration, so two runs
//! over identical input would otherwise produce different indexes and the
//! persisted audit record would inherit the difference.
//!
//! The emitters stamp a collector id into `Walk::index`. This module sorts the
//! response's walks into the total order, overwrites each `index`, and returns
//! the map a caller needs to translate its earnings' collector ids into final
//! indexes.

use std::collections::HashMap;

use super::types::{
    CommissionCalculationResult, CommissionEarning, PlanIdentity, VolumeSource, Walk, WalkKind,
};

/// Sorts `walks` into the total order, writes each `index`, and returns a map
/// from the collector id a walk carried to the index it now has.
///
/// The order, per the ticket:
///
/// 1. `kind`, level before generation.
/// 2. `stream_id` ascending. Absent sorts first.
/// 3. rank by `(ordinal, name)` ascending. Absent sorts first.
/// 4. the source's position in the input `volume` slice.
/// 5. the collector id.
///
/// Key 5 is not in the ticket and is what makes the order total. Keys 1 to 4
/// tie whenever one source appears twice in `volume`, which `validate_source`
/// permits: both walks resolve to the same position, because position is
/// looked up by `source_id` and a lookup cannot tell two identical entries
/// apart. Falling through to the collector id resolves that deterministically,
/// because within any group sharing keys 1 to 4 the walks came from a single
/// traversal loop in `volume` order.
///
/// That holds for streamline too, even though its collector ids come from a
/// `HashMap` iteration. Key 2 groups walks by stream before key 5 is reached,
/// and within one stream the ids are assigned in `volume` order. The absolute
/// ids may differ between runs; their order within a stream does not.
///
/// A source in `walks` that is absent from `volume` sorts last rather than
/// panicking. That should not happen, and if it does the walk is still
/// ordered deterministically rather than by chance.
pub(crate) fn assign_indexes(
    walks: &mut [Walk],
    volume: &[VolumeSource],
    rank_ordinals: &HashMap<&str, u16>,
) -> HashMap<u32, u32> {
    let mut position: HashMap<uuid::Uuid, usize> = HashMap::with_capacity(volume.len());
    for (i, v) in volume.iter().enumerate() {
        // First occurrence wins, so a duplicated source resolves to one
        // position and key 5 separates its walks.
        position.entry(v.source_id).or_insert(i);
    }

    walks.sort_by(|a, b| {
        kind_key(a.kind)
            .cmp(&kind_key(b.kind))
            .then_with(|| a.stream_id.cmp(&b.stream_id))
            .then_with(|| rank_key(a, rank_ordinals).cmp(&rank_key(b, rank_ordinals)))
            .then_with(|| source_position(a, &position).cmp(&source_position(b, &position)))
            .then_with(|| a.index.cmp(&b.index))
    });

    let mut remap = HashMap::with_capacity(walks.len());
    for (new_index, walk) in walks.iter_mut().enumerate() {
        let collector_id = walk.index;
        let new_index = new_index as u32;
        walk.index = new_index;
        remap.insert(collector_id, new_index);
    }
    remap
}

/// Assembles a calculator's result: orders the walks, remaps every earning's
/// collector id to its final index, sorts the earnings, and attaches the plan
/// identity.
///
/// All five calculators go through here so the ordering and the remap cannot
/// drift between them. Doing it per-calculator is what would let one of them
/// take an index from emission order, which design 029 forbids.
pub(crate) fn assemble(
    mut earnings: Vec<CommissionEarning>,
    mut walks: Vec<Walk>,
    volume: &[VolumeSource],
    rank_ordinals: &HashMap<&str, u16>,
    plan_identity: &PlanIdentity,
) -> CommissionCalculationResult {
    let remap = assign_indexes(&mut walks, volume, rank_ordinals);

    for earning in earnings.iter_mut() {
        // `if let` rather than a filter: a None walk is stairstep Walk 2's
        // recorded gap and must survive as None, not be skipped or defaulted.
        if let Some(collector_id) = earning.walk {
            earning.walk = Some(*remap.get(&collector_id).unwrap_or_else(|| {
                panic!("earning references collector id {collector_id}, which no walk carried")
            }));
        }
    }

    // After the remap, so the walk tiebreaker sorts on final indexes.
    super::walk::sort_earnings(&mut earnings);

    CommissionCalculationResult {
        earnings,
        walks,
        plan: plan_identity.clone(),
    }
}

fn kind_key(kind: WalkKind) -> u8 {
    match kind {
        WalkKind::Level => 0,
        WalkKind::Generation => 1,
    }
}

/// `(ordinal, name)` for a walk's rank context, or `None` when it carries no
/// rank. Sorting on the pair rather than the ordinal alone keeps the order
/// stable when two ranks share an ordinal, which HEU-646 tracks as unvalidated.
fn rank_key<'a>(walk: &'a Walk, rank_ordinals: &HashMap<&str, u16>) -> Option<(u16, &'a str)> {
    walk.rank.as_deref().map(|name| {
        let ordinal = rank_ordinals.get(name).copied().unwrap_or(0);
        (ordinal, name)
    })
}

fn source_position(walk: &Walk, position: &HashMap<uuid::Uuid, usize>) -> usize {
    position.get(&walk.source_id).copied().unwrap_or(usize::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers::uuid_from_index as uuid;
    use crate::commission::types::WalkStop;

    fn walk_with(
        collector_id: u32,
        kind: WalkKind,
        stream_id: Option<u32>,
        rank: Option<&str>,
        source: usize,
    ) -> Walk {
        Walk {
            index: collector_id,
            source_id: uuid(source),
            kind,
            stream_id,
            rank: rank.map(str::to_string),
            steps: Vec::new(),
            stop: WalkStop::RootReached,
            stopped_at: None,
        }
    }

    fn one_source(n: usize) -> Vec<VolumeSource> {
        (0..n)
            .map(|i| VolumeSource {
                source_id: uuid(i),
                cv_amount: 100.0,
            })
            .collect()
    }

    #[test]
    fn level_walks_sort_before_generation_walks() {
        let mut walks = vec![
            walk_with(0, WalkKind::Generation, None, None, 0),
            walk_with(1, WalkKind::Level, None, None, 0),
        ];
        assign_indexes(&mut walks, &one_source(1), &HashMap::new());
        assert_eq!(walks[0].kind, WalkKind::Level);
        assert_eq!(walks[0].index, 0);
        assert_eq!(walks[1].index, 1);
    }

    #[test]
    fn the_remap_survives_a_sort_that_reorders() {
        // Emission order is deliberately the reverse of the sorted order. A
        // test where they already match proves nothing about the remap.
        let mut walks = vec![
            walk_with(0, WalkKind::Generation, None, None, 0),
            walk_with(1, WalkKind::Level, None, None, 1),
            walk_with(2, WalkKind::Level, None, None, 0),
        ];
        let remap = assign_indexes(&mut walks, &one_source(2), &HashMap::new());

        // Sorted: level/source0 (collector 2), level/source1 (collector 1),
        // generation (collector 0).
        assert_eq!(remap.get(&2), Some(&0));
        assert_eq!(remap.get(&1), Some(&1));
        assert_eq!(remap.get(&0), Some(&2));

        // Every final index is reachable from exactly one collector id.
        let mut reached: Vec<u32> = remap.values().copied().collect();
        reached.sort_unstable();
        assert_eq!(
            reached,
            (0..walks.len() as u32).collect::<Vec<_>>(),
            "the remap must cover every index exactly once"
        );
    }

    #[test]
    fn streams_sort_ascending_within_a_kind() {
        let mut walks = vec![
            walk_with(0, WalkKind::Level, Some(3), None, 0),
            walk_with(1, WalkKind::Level, Some(1), None, 0),
            walk_with(2, WalkKind::Level, Some(2), None, 0),
        ];
        assign_indexes(&mut walks, &one_source(1), &HashMap::new());
        assert_eq!(
            walks.iter().map(|w| w.stream_id).collect::<Vec<_>>(),
            vec![Some(1), Some(2), Some(3)]
        );
    }

    #[test]
    fn ranks_sort_by_ordinal_then_name() {
        // "bronze" has the lower ordinal despite sorting later by name, so an
        // implementation comparing names alone fails here.
        let ordinals = HashMap::from([("bronze", 1u16), ("actinium", 2u16)]);
        let mut walks = vec![
            walk_with(0, WalkKind::Generation, None, Some("actinium"), 0),
            walk_with(1, WalkKind::Generation, None, Some("bronze"), 0),
        ];
        assign_indexes(&mut walks, &one_source(1), &ordinals);
        assert_eq!(walks[0].rank.as_deref(), Some("bronze"));
        assert_eq!(walks[1].rank.as_deref(), Some("actinium"));
    }

    #[test]
    fn sources_sort_by_position_in_volume_not_by_uuid() {
        // Volume order is deliberately not uuid order, so an implementation
        // sorting on source_id fails here.
        let volume = vec![
            VolumeSource {
                source_id: uuid(9),
                cv_amount: 100.0,
            },
            VolumeSource {
                source_id: uuid(2),
                cv_amount: 100.0,
            },
        ];
        let mut walks = vec![
            walk_with(0, WalkKind::Level, None, None, 2),
            walk_with(1, WalkKind::Level, None, None, 9),
        ];
        assign_indexes(&mut walks, &volume, &HashMap::new());
        assert_eq!(
            walks[0].source_id,
            uuid(9),
            "the source first in volume sorts first"
        );
    }

    #[test]
    fn duplicate_sources_fall_through_to_the_collector_id() {
        // validate_source permits a repeated VolumeSource, and a position
        // lookup cannot tell the two entries apart. Without key 5 these tie
        // and the order depends on sort stability.
        let volume = vec![
            VolumeSource {
                source_id: uuid(1),
                cv_amount: 100.0,
            },
            VolumeSource {
                source_id: uuid(1),
                cv_amount: 50.0,
            },
        ];
        let mut walks = vec![
            walk_with(7, WalkKind::Level, None, None, 1),
            walk_with(3, WalkKind::Level, None, None, 1),
        ];
        let remap = assign_indexes(&mut walks, &volume, &HashMap::new());
        assert_eq!(remap.get(&3), Some(&0), "lower collector id sorts first");
        assert_eq!(remap.get(&7), Some(&1));
    }

    #[test]
    fn a_source_absent_from_volume_sorts_last_rather_than_panicking() {
        let mut walks = vec![
            walk_with(0, WalkKind::Level, None, None, 5),
            walk_with(1, WalkKind::Level, None, None, 0),
        ];
        assign_indexes(&mut walks, &one_source(1), &HashMap::new());
        assert_eq!(walks[0].source_id, uuid(0));
        assert_eq!(walks[1].source_id, uuid(5));
    }

    #[test]
    fn the_remap_covers_every_walk() {
        let mut walks = vec![
            walk_with(4, WalkKind::Level, None, None, 0),
            walk_with(9, WalkKind::Generation, None, None, 0),
            walk_with(1, WalkKind::Level, Some(2), None, 0),
        ];
        let remap = assign_indexes(&mut walks, &one_source(1), &HashMap::new());
        assert_eq!(remap.len(), 3, "a caller's expect() must never miss");
        for id in [4u32, 9, 1] {
            assert!(remap.contains_key(&id), "collector id {id} is not remapped");
        }
    }
}
