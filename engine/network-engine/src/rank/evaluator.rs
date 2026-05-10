//! Bottom-up walk driver and ladder ascent loop.

use std::collections::HashMap;

use uuid::Uuid;

use crate::commission::types::VolumeSource;

/// Per-distributor CV total, derived from `EvaluationInputs.volume_sources`.
///
/// Built once per `evaluate_ranks` call and reused across predicate evaluations.
#[allow(dead_code)] // Wired up by predicates in a later task.
pub(crate) struct VolumeIndex {
    by_source: HashMap<Uuid, f64>,
}

#[allow(dead_code)] // Wired up by predicates in a later task.
impl VolumeIndex {
    pub(crate) fn build(sources: &[VolumeSource]) -> Self {
        let mut by_source: HashMap<Uuid, f64> = HashMap::new();
        for src in sources {
            *by_source.entry(src.source_id).or_insert(0.0) += src.cv_amount;
        }
        Self { by_source }
    }

    pub(crate) fn cv_for(&self, user_id: Uuid) -> f64 {
        self.by_source.get(&user_id).copied().unwrap_or(0.0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::types::VolumeSource;
    use uuid::Uuid;

    #[test]
    fn volume_index_sums_cv_per_source() {
        let a = Uuid::from_u128(1);
        let b = Uuid::from_u128(2);
        let sources = vec![
            VolumeSource {
                source_id: a,
                cv_amount: 100.0,
            },
            VolumeSource {
                source_id: a,
                cv_amount: 50.0,
            },
            VolumeSource {
                source_id: b,
                cv_amount: 200.0,
            },
        ];
        let idx = VolumeIndex::build(&sources);
        assert!((idx.cv_for(a) - 150.0).abs() < 1e-9);
        assert!((idx.cv_for(b) - 200.0).abs() < 1e-9);
        assert_eq!(idx.cv_for(Uuid::from_u128(99)), 0.0);
    }
}
