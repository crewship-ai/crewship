//! Read-only application qualification, never an origin grant or authority.
use crate::{format, Document};
use mooring_text::yrs::{ReadTxn, Transact};

pub const MAX_ORIGIN: u64 = (1_u64 << 53) - 1;
const MAX_ALLOCATED_ORIGINS: usize = 4096;

#[derive(Debug, PartialEq, Eq)]
pub enum Error {
    InvalidCheckpoint,
    UnqualifiedCheckpoint,
    InvalidOrigin,
    OriginInUse,
    Capacity,
}

/// Complete allocated-client clocks from one exact qualified checkpoint.
/// Deleted content still occupies its origin. This is not a list of active
/// participants, an admission proof, or an allocation reservation.
pub struct CheckpointOrigins<'a> {
    checkpoint: &'a [u8],
    allocated: Vec<(u64, u32)>,
}

impl CheckpointOrigins<'_> {
    /// Exact borrowed source bytes; the host separately verifies full wire identity.
    pub fn checkpoint(&self) -> &[u8] {
        self.checkpoint
    }

    pub fn allocated(&self) -> &[(u64, u32)] {
        &self.allocated
    }

    /// Checks a candidate before constructing any upstream ClientID. The host
    /// must ALSO exclude all previously issued origins, including origins with
    /// no committed content yet, atomically inside its original-author writer.
    pub fn check_unused(&self, candidate: u64) -> Result<(), Error> {
        if candidate == 0 || candidate > MAX_ORIGIN {
            return Err(Error::InvalidOrigin);
        }
        if self
            .allocated
            .binary_search_by_key(&candidate, |(origin, _)| *origin)
            .is_ok()
        {
            return Err(Error::OriginInUse);
        }
        if self.allocated.len() >= MAX_ALLOCATED_ORIGINS {
            return Err(Error::Capacity);
        }
        Ok(())
    }
}

/// The caller must first authorize the exact checkpoint scope/revision and bound
/// the input according to its negotiated transport. Restore independently
/// verifies the model/CRDT/native binding with the existing closed preflight.
/// The returned inventory grants no permission to use or mutate this document.
pub fn inspect(checkpoint: &[u8]) -> Result<CheckpointOrigins<'_>, Error> {
    let document = Document::restore(checkpoint).map_err(|_| Error::InvalidCheckpoint)?;
    if document.is_read_only() || document.model != format::Model::V2 {
        return Err(Error::UnqualifiedCheckpoint);
    }
    let txn = document.doc.transact();
    let frontier = txn.state_vector();
    if frontier.len() > MAX_ALLOCATED_ORIGINS {
        return Err(Error::Capacity);
    }
    let mut allocated = frontier
        .iter()
        .map(|(origin, clock)| (origin.get(), *clock))
        .collect::<Vec<_>>();
    if allocated
        .iter()
        .any(|(origin, clock)| *origin > MAX_ORIGIN || *clock == 0)
    {
        return Err(Error::InvalidCheckpoint);
    }
    allocated.sort_unstable_by_key(|(origin, _)| *origin);
    Ok(CheckpointOrigins {
        checkpoint,
        allocated,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_text::yrs::{
        updates::decoder::Decode, ClientID, Doc, Map, OffsetKind, Options, Out, StateVector, Text,
        Update,
    };

    fn source() -> Document {
        let native: serde_json::Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json"))
                .unwrap();
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("origin_{i}"))
            .collect();
        Document::import(native, ids).unwrap()
    }

    #[test]
    fn exact_checkpoint_inventory_retains_sorted_frontier_and_unchanged_bytes() {
        let document = source();
        let bytes = document.checkpoint().unwrap();
        let inventory = inspect(&bytes).unwrap();
        assert!(!inventory.allocated().is_empty());
        assert!(inventory
            .allocated()
            .windows(2)
            .all(|pair| pair[0].0 < pair[1].0));
        assert_eq!(inventory.checkpoint(), bytes);
        for (origin, clock) in inventory.allocated() {
            assert!(*clock > 0);
            if *origin != 0 {
                assert_eq!(inventory.check_unused(*origin), Err(Error::OriginInUse));
            }
        }
        assert_eq!(document.checkpoint().unwrap(), bytes);
        assert_eq!(
            Document::restore(&bytes).unwrap().checkpoint().unwrap(),
            bytes
        );
    }

    #[test]
    fn candidates_cannot_alias_upstream_masked_client_ids_or_panic() {
        let bytes = source().checkpoint().unwrap();
        let inventory = inspect(&bytes).unwrap();
        for invalid in [0, MAX_ORIGIN + 1, (1_u64 << 63) + 17, u64::MAX] {
            assert_eq!(inventory.check_unused(invalid), Err(Error::InvalidOrigin));
        }
        let unused = (1..=MAX_ORIGIN)
            .find(|candidate| inventory.check_unused(*candidate).is_ok())
            .unwrap();
        assert_eq!(inventory.check_unused(unused), Ok(()));
        // This check is deliberately not a reservation or a consumed grant.
        assert_eq!(inventory.check_unused(unused), Ok(()));
    }

    #[test]
    fn a_full_frontier_does_not_qualify_an_additional_client() {
        let inventory = CheckpointOrigins {
            checkpoint: b"synthetic capacity-only fixture",
            allocated: (0..MAX_ALLOCATED_ORIGINS as u64)
                .map(|id| (id, 1))
                .collect(),
        };
        assert_eq!(inventory.check_unused(MAX_ORIGIN), Err(Error::Capacity));
        assert_eq!(inventory.check_unused(17), Err(Error::OriginInUse));
        assert_eq!(inventory.check_unused(u64::MAX), Err(Error::InvalidOrigin));
    }

    #[test]
    fn a_fully_deleted_authors_origin_stays_occupied_after_cold_restore() {
        let mut document = source();
        let before = document.native().unwrap();
        let before_bytes = document.checkpoint().unwrap();
        let inventory = inspect(&before_bytes).unwrap();
        let origin = (1..4096)
            .find(|candidate| inventory.check_unused(*candidate).is_ok())
            .unwrap();
        let update = document
            .doc
            .transact()
            .encode_state_as_update_v1(&StateVector::default());
        let replica = Doc::with_options(Options {
            client_id: ClientID::new(origin),
            offset_kind: OffsetKind::Utf16,
            skip_gc: true,
            ..Options::default()
        });
        replica
            .transact_mut()
            .apply_update(Update::decode_v1(&update).unwrap())
            .unwrap();
        document.doc = replica;
        let id = &document.block_ids().unwrap()[0];
        let text = {
            let txn = document.doc.transact();
            let Some(Out::YText(text)) = txn.get_map("wordcraft.texts").unwrap().get(&txn, id)
            else {
                panic!("missing qualified text");
            };
            text
        };
        text.insert(&mut document.doc.transact_mut(), 0, "X");
        text.remove_range(&mut document.doc.transact_mut(), 0, 1);
        assert_eq!(document.native().unwrap(), before);
        let bytes = document.checkpoint().unwrap();
        let restored = inspect(&bytes).unwrap();
        assert_eq!(restored.check_unused(origin), Err(Error::OriginInUse));
        assert!(restored
            .allocated()
            .iter()
            .any(|(id, clock)| *id == origin && *clock > 0));
        assert_eq!(Document::restore(&bytes).unwrap().native().unwrap(), before);
    }

    #[test]
    fn unknown_readonly_legacy_and_substituted_checkpoints_are_not_origin_grants() {
        let document = source();
        let bytes = document.checkpoint().unwrap();
        let legacy = crate::Checkpoint {
            format: crate::profile::LEGACY_CHECKPOINT_FORMAT.into(),
            native: document.native().unwrap(),
            block_ids: document.block_ids().unwrap(),
            update: document
                .doc
                .transact()
                .encode_state_as_update_v1(&StateVector::default()),
        };
        assert!(matches!(
            inspect(&serde_json::to_vec(&legacy).unwrap()),
            Err(Error::UnqualifiedCheckpoint)
        ));
        for field in ["format", "native"] {
            let mut envelope: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
            if field == "format" {
                envelope[field] = serde_json::json!("unknown-format");
            } else {
                envelope[field]["core"]["title"] = serde_json::json!("substitution");
            }
            assert!(matches!(
                inspect(&serde_json::to_vec(&envelope).unwrap()),
                Err(Error::InvalidCheckpoint)
            ));
        }
        let mut native = document.native().unwrap();
        native["settings"]["trackChanges"] = serde_json::json!(true);
        let readonly = Document::import(native, document.block_ids().unwrap()).unwrap();
        assert!(readonly.is_read_only());
        assert!(matches!(
            inspect(&readonly.checkpoint().unwrap()),
            Err(Error::UnqualifiedCheckpoint)
        ));
    }
}
