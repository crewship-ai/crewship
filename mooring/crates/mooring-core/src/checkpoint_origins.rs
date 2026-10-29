//! Application-owned allocated CRDT identity inventory, never an origin grant.
use crate::adapter::PrepareAdapter;

/// Exact numeric domain of the qualified Yjs/Yrs client identity profile.
/// Never mask, truncate or hash a wider identifier into this domain.
pub const MAX_ORIGIN: u64 = (1_u64 << 53) - 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AllocatedOrigin {
    pub origin: u64,
    /// Materialized allocated clock, including deleted content. Zero is absent.
    pub clock: u32,
}

#[derive(Debug, PartialEq, Eq)]
pub enum InventoryError {
    Capacity,
    InvalidOrigin,
    ZeroClock,
    NotStrictlySorted,
}

/// Numerically validated representation only. Construction is not proof of
/// completeness, authenticated source, permission, or an allocation reservation.
#[derive(Debug)]
pub struct CheckpointOrigins {
    allocated: Vec<AllocatedOrigin>,
}
impl CheckpointOrigins {
    /// The explicit limit is an implementation qualification, not a protocol-
    /// global document lifetime limit. The caller must also bound restoration
    /// and serialization; this check cannot undo an earlier large allocation.
    pub fn from_sorted(
        allocated: Vec<AllocatedOrigin>,
        maximum: usize,
    ) -> Result<Self, InventoryError> {
        if allocated.len() > maximum {
            return Err(InventoryError::Capacity);
        }
        let mut previous = None;
        for entry in &allocated {
            if entry.origin > MAX_ORIGIN {
                return Err(InventoryError::InvalidOrigin);
            }
            if entry.clock == 0 {
                return Err(InventoryError::ZeroClock);
            }
            if previous.is_some_and(|origin| origin >= entry.origin) {
                return Err(InventoryError::NotStrictlySorted);
            }
            previous = Some(entry.origin);
        }
        Ok(Self { allocated })
    }

    pub fn allocated(&self) -> &[AllocatedOrigin] {
        &self.allocated
    }
}

/// Explicit application opt-in, with no default inventory. The app qualifies
/// the exact original checkpoint bytes and reports ALL materialized identities,
/// including deleted authors and baseline zero. It must reject unqualified
/// state rather than return a partial or guessed-empty inventory.
///
/// The host independently authenticates the concrete process/session and full
/// checkpoint identity, bounds input/output, and rechecks original source and
/// owner in its writer before using the result. Issuance additionally requires
/// the durable union with issued-uncommitted, rejected and burned identities.
/// This method grants nothing and cannot install state, issue Saved, construct
/// an authoritative writer or authorize participant access.
pub trait InspectCheckpointOrigins: PrepareAdapter {
    fn inspect_checkpoint_origins(
        &self,
        checkpoint: &[u8],
    ) -> Result<CheckpointOrigins, Self::Error>;
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry(origin: u64, clock: u32) -> AllocatedOrigin {
        AllocatedOrigin { origin, clock }
    }

    #[test]
    fn baseline_zero_high_exact_id_and_full_clock_are_preserved() {
        let origins = vec![entry(0, 1), entry(MAX_ORIGIN, u32::MAX)];
        let inventory = CheckpointOrigins::from_sorted(origins.clone(), 2).unwrap();
        assert_eq!(inventory.allocated(), origins);
        assert!(CheckpointOrigins::from_sorted(Vec::new(), 0)
            .unwrap()
            .allocated()
            .is_empty());
    }

    #[test]
    fn capacity_wide_ids_zero_clocks_and_ambiguous_order_are_rejected() {
        for (origins, maximum, expected) in [
            (vec![entry(1, 1)], 0, InventoryError::Capacity),
            (
                vec![entry(MAX_ORIGIN + 1, 1)],
                1,
                InventoryError::InvalidOrigin,
            ),
            (vec![entry(1, 0)], 1, InventoryError::ZeroClock),
            (
                vec![entry(1, 1), entry(1, 2)],
                2,
                InventoryError::NotStrictlySorted,
            ),
            (
                vec![entry(2, 1), entry(1, 1)],
                2,
                InventoryError::NotStrictlySorted,
            ),
        ] {
            assert_eq!(
                CheckpointOrigins::from_sorted(origins, maximum).unwrap_err(),
                expected
            );
        }
    }
}
