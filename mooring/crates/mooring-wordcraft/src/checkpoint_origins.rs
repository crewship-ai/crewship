//! Read-only app opt-in; no issuance, reservation or participant authority.
use crate::{
    adapter::{PrepareError, WordCraftAdapter},
    AdapterError,
};
use mooring_core::checkpoint_origins::{
    AllocatedOrigin, CheckpointOrigins, InspectCheckpointOrigins,
};
impl InspectCheckpointOrigins for WordCraftAdapter {
    fn inspect_checkpoint_origins(
        &self,
        checkpoint: &[u8],
    ) -> Result<CheckpointOrigins, PrepareError> {
        // The app restores and qualifies the EXACT bytes before exporting any
        // identities; no default/partial inventory or wire label is a witness.
        let inspected = crate::origin_inventory::inspect(checkpoint)
            .map_err(|_| PrepareError::State(AdapterError::InvalidCheckpoint))?;
        let allocated = inspected
            .allocated()
            .iter()
            .map(|&(origin, clock)| AllocatedOrigin { origin, clock })
            .collect();
        // This app profile independently qualifies at most4096 allocated clients.
        CheckpointOrigins::from_sorted(allocated, 4096)
            .map_err(|_| PrepareError::State(AdapterError::InvalidCheckpoint))
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use crate::{origin_inventory, Document};
    fn source() -> Document {
        let native: serde_json::Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json"))
                .unwrap();
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("inspector_{i}"))
            .collect();
        Document::import(native, ids).unwrap()
    }
    #[test]
    fn app_trait_returns_complete_exact_sorted_inventory_without_mutation() {
        let document = source();
        let checkpoint = document.checkpoint().unwrap();
        let direct = origin_inventory::inspect(&checkpoint).unwrap();
        let inspected = WordCraftAdapter
            .inspect_checkpoint_origins(&checkpoint)
            .unwrap();
        let actual: Vec<_> = inspected
            .allocated()
            .iter()
            .map(|entry| (entry.origin, entry.clock))
            .collect();
        assert_eq!(actual, direct.allocated());
        assert!(!actual.is_empty());
        assert_eq!(
            Document::restore(&checkpoint)
                .unwrap()
                .checkpoint()
                .unwrap(),
            checkpoint
        );
        assert_eq!(document.checkpoint().unwrap(), checkpoint);
    }
    #[test]
    fn readonly_and_malformed_state_never_return_partial_or_empty_fallback() {
        let document = source();
        let mut native = document.native().unwrap();
        native["settings"]["trackChanges"] = serde_json::json!(true);
        let readonly = Document::import(native, document.block_ids().unwrap()).unwrap();
        assert!(readonly.is_read_only());
        for checkpoint in [readonly.checkpoint().unwrap(), b"malformed".to_vec()] {
            assert!(matches!(
                WordCraftAdapter.inspect_checkpoint_origins(&checkpoint),
                Err(PrepareError::State(AdapterError::InvalidCheckpoint))
            ));
        }
        let mut substituted: serde_json::Value =
            serde_json::from_slice(&document.checkpoint().unwrap()).unwrap();
        substituted["native"]["core"]["title"] = serde_json::json!("unbound native");
        assert!(WordCraftAdapter
            .inspect_checkpoint_origins(&serde_json::to_vec(&substituted).unwrap())
            .is_err());
    }
}
