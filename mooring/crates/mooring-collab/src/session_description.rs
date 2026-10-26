//! Private application description: no live document access or installation.
use crate::dispatch::invalid;
use mooring_core::checkpoint_description::DescribeCheckpoint;
use mooring_protocol::{control, session_control, *};
use mooring_wordcraft::adapter::WordCraftAdapter;
use std::{collections::BTreeSet, io};

pub(crate) fn respond(raw: &[u8], open: &SessionOpen) -> io::Result<Vec<u8>> {
    let normalized = session_control::validate(raw, control::Peer::InheritedHost)
        .map_err(|_| invalid("invalid checkpoint description request"))?;
    let request: SessionDescribeCheckpoint = serde_json::from_value(normalized.value().clone())
        .map_err(|_| invalid("not checkpoint description request"))?;
    if request.scope != open.scope
        || request.session_id != open.session_id
        || request.runtime_ref != open.runtime_ref
        || request.versions != open.versions
    {
        return Err(invalid("checkpoint description binding"));
    }
    let bytes = control::decode_checkpoint(&request.checkpoint)
        .map_err(|_| invalid("checkpoint description input"))?;
    let metadata = WordCraftAdapter
        .describe_checkpoint(&bytes)
        .map_err(|_| invalid("unqualified checkpoint description"))?;
    if metadata.versions != open.versions {
        return Err(invalid("checkpoint description tuple"));
    }
    let mut assets = BTreeSet::new();
    if metadata
        .assets
        .iter()
        .any(|asset| !assets.insert(asset.asset_id.as_str()))
    {
        return Err(invalid("checkpoint description duplicate asset"));
    }
    let response = SessionCheckpointDescribed {
        r#type: SessionCheckpointDescribedType::SessionCheckpointDescribed,
        request_id: request.request_id,
        control_profile: SessionCheckpointDescribedControlProfile::MooringSessionCausalV1,
        scope: request.scope,
        session_id: request.session_id,
        runtime_ref: request.runtime_ref,
        versions: request.versions,
        input_digest: request.checkpoint.digest,
        description: CheckpointDescription {
            format_version: metadata.format_version,
            versions: metadata.versions,
            assets: metadata.assets,
        },
    };
    let raw = serde_json::to_vec(&response).map_err(|_| invalid("description encoding"))?;
    // BOTH final encoded and canonical profile limits, before host retention.
    let validated = session_control::validate(&raw, control::Peer::InheritedRuntime)
        .map_err(|_| invalid("description output capacity"))?;
    Ok(validated.bytes().to_vec())
}
