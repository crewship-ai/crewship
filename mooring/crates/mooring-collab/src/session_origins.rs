//! Read-only inventory on the independent prepare lane. Never an origin grant.
use crate::dispatch::invalid;
use mooring_core::{
    checkpoint_description::DescribeCheckpoint, checkpoint_origins::InspectCheckpointOrigins,
};
use mooring_protocol::{checkpoint, control, session_control, *};
use std::{collections::BTreeMap, io};

pub(crate) fn respond<A: DescribeCheckpoint + InspectCheckpointOrigins>(
    raw: &[u8],
    open: &SessionOpen,
    adapter: &A,
) -> io::Result<Vec<u8>> {
    // Check the actually selected opening before decoding any checkpoint.
    if !open
        .required_capabilities
        .contains(&SessionOpenRequiredCapabilitiesItem::OriginInventoryV1)
    {
        return Err(invalid("unselected origin inventory"));
    }
    let value = session_control::validate(raw, control::Peer::InheritedHost)
        .map_err(|_| invalid("invalid origin inventory request"))?;
    let request: SessionInspectOrigins = serde_json::from_value(value.value().clone())
        .map_err(|_| invalid("not origin inventory request"))?;
    if request.scope != open.scope
        || request.session_id != open.session_id
        || request.runtime_ref != open.runtime_ref
        || request.versions != open.versions
        || request.checkpoint.scope != open.scope
        || request.checkpoint.versions != open.versions
    {
        return Err(invalid("origin inventory binding"));
    }
    let identity = checkpoint::verify(
        &request.checkpoint,
        checkpoint::Limits {
            decoded_bytes: control::MAX_CHECKPOINT_BYTES,
            preimage_bytes: control::MAX_CONTROL_BYTES,
        },
    )
    .map_err(|_| invalid("origin checkpoint identity"))?;
    let metadata = adapter
        .describe_checkpoint(identity.state())
        .map_err(|_| invalid("unqualified origin checkpoint"))?;
    // Complete asset manifest equality ignores array ordering, consistently with
    // the generic full identity. No app metadata is inferred from writability.
    let described: BTreeMap<_, _> = metadata
        .assets
        .iter()
        .map(|asset| (&asset.asset_id, asset))
        .collect();
    let declared: BTreeMap<_, _> = request
        .checkpoint
        .assets
        .iter()
        .map(|asset| (&asset.asset_id, asset))
        .collect();
    if metadata.versions != request.versions
        || metadata.format_version != request.checkpoint.format_version
        || described.len() != metadata.assets.len()
        || described != declared
    {
        return Err(invalid("origin checkpoint metadata"));
    }
    let inventory = adapter
        .inspect_checkpoint_origins(identity.state())
        .map_err(|_| invalid("unqualified origin inventory"))?;
    if inventory.allocated().len() > 4096 {
        return Err(invalid("origin inventory capacity"));
    }
    let response = SessionOriginsInspected {
        r#type: SessionOriginsInspectedType::SessionOriginsInspected,
        request_id: request.request_id,
        control_profile: SessionOriginsInspectedControlProfile::MooringSessionCausalV1,
        scope: request.scope,
        session_id: request.session_id,
        runtime_ref: request.runtime_ref,
        // Correlated host metadata, not native proof of current SQL ownership.
        // Host must check its actual owner/source before and after this call.
        fence: request.fence,
        versions: request.versions,
        checkpoint_basis: ConfirmedCheckpointBasis {
            revision: request.checkpoint.revision,
            digest: identity.full_digest().into(),
        },
        checkpoint_generation: request.checkpoint.generation,
        content_digest: identity.content_digest().into(),
        allocated: inventory
            .allocated()
            .iter()
            .map(|entry| AllocatedCrdtOrigin {
                origin: Counter(entry.origin),
                clock: Counter(u64::from(entry.clock)),
            })
            .collect(),
    };
    let raw = serde_json::to_vec(&response).map_err(|_| invalid("origin inventory encoding"))?;
    let checked = session_control::validate(&raw, control::Peer::InheritedRuntime)
        .map_err(|_| invalid("origin inventory output capacity"))?;
    Ok(checked.bytes().to_vec())
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_wordcraft::{adapter::WordCraftAdapter, Document};
    use serde_json::{json, Value};

    fn fixture() -> (SessionOpen, Value) {
        let native: Value = serde_json::from_str(include_str!(
            "../../mooring-wordcraft/tests/fixtures/d1-engine-canonical.json"
        ))
        .unwrap();
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("origin-{i}"))
            .collect();
        let bytes = Document::import(native, ids).unwrap().checkpoint().unwrap();
        let opaque = control::encode_checkpoint(&bytes).unwrap();
        let versions = crate::dispatch::wordcraft_versions();
        let scope = json!({"workspace_id":"w","artifact_id":"a","application_id":"wordcraft"});
        let open: SessionOpen = serde_json::from_value(json!({"type":"session_open","request_id":"open","control_profile":session_control::PROFILE,"scope":scope,"session_id":"session","runtime_ref":"runtime","versions":versions,"required_capabilities":["checkpoint_identity_v1","origin_inventory_v1"]})).unwrap();
        let mut full: Checkpoint = serde_json::from_value(json!({"scope":scope,"revision":"0","generation":"1","format_version":mooring_wordcraft::profile::CHECKPOINT_FORMAT,"versions":versions,"encoding":"base64","state":opaque.state,"digest":opaque.digest,"assets":[]})).unwrap();
        full.digest = checkpoint::compute(
            &full,
            checkpoint::Limits {
                decoded_bytes: control::MAX_CHECKPOINT_BYTES,
                preimage_bytes: control::MAX_CONTROL_BYTES,
            },
        )
        .unwrap()
        .full_digest()
        .into();
        let request = json!({"type":"session_inspect_origins","request_id":"inspect","control_profile":session_control::PROFILE,"scope":scope,"session_id":"session","runtime_ref":"runtime","fence":{"generation":"2","token":"x".repeat(64)},"versions":versions,"checkpoint":full});
        (open, request)
    }
    #[test]
    fn identity_only_cannot_reach_checkpoint_decode_or_inspection() {
        let (mut open, mut request) = fixture();
        open.required_capabilities =
            vec![SessionOpenRequiredCapabilitiesItem::CheckpointIdentityV1];
        request["checkpoint"]["state"] = json!("invalid");
        assert_eq!(
            respond(
                &serde_json::to_vec(&request).unwrap(),
                &open,
                &WordCraftAdapter
            )
            .unwrap_err()
            .to_string(),
            "unselected origin inventory"
        );
    }
    #[test]
    fn outer_and_full_checkpoint_transplants_never_return_an_inventory() {
        let (open, request) = fixture();
        for path in [
            vec!["scope", "artifact_id"],
            vec!["session_id"],
            vec!["runtime_ref"],
            vec!["versions", "model_version"],
            vec!["checkpoint", "scope", "artifact_id"],
            vec!["checkpoint", "versions", "model_version"],
            vec!["checkpoint", "generation"],
            vec!["checkpoint", "digest"],
            vec!["checkpoint", "state"],
        ] {
            let mut bad = request.clone();
            let mut target = &mut bad;
            for key in path {
                target = &mut target[key];
            }
            *target = json!("different");
            assert!(respond(&serde_json::to_vec(&bad).unwrap(), &open, &WordCraftAdapter).is_err());
        }
        // Rehashing a false format/asset declaration cannot change what the
        // concrete application proved about its retained original bytes.
        for assets in [false, true] {
            let mut bad = request.clone();
            if assets {
                bad["checkpoint"]["assets"] = json!([{"asset_id":"external","size_bytes":"1","digest":format!("sha256:{}","1".repeat(64))}]);
            } else {
                bad["checkpoint"]["format_version"] = json!("invented");
            }
            let mut full: Checkpoint = serde_json::from_value(bad["checkpoint"].clone()).unwrap();
            full.digest = checkpoint::compute(
                &full,
                checkpoint::Limits {
                    decoded_bytes: control::MAX_CHECKPOINT_BYTES,
                    preimage_bytes: control::MAX_CONTROL_BYTES,
                },
            )
            .unwrap()
            .full_digest()
            .into();
            bad["checkpoint"] = serde_json::to_value(full).unwrap();
            assert!(respond(&serde_json::to_vec(&bad).unwrap(), &open, &WordCraftAdapter).is_err());
        }
    }
    #[test]
    fn actual_readonly_d3_description_does_not_qualify_its_origins() {
        let (open, mut request) = fixture();
        let native: Value = serde_json::from_str(include_str!(
            "../../mooring-wordcraft/tests/fixtures/d3-engine-canonical.json"
        ))
        .unwrap();
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("d3-{i}"))
            .collect();
        let document = Document::import(native, ids).unwrap();
        assert!(document.is_read_only());
        let bytes = document.checkpoint().unwrap();
        assert!(WordCraftAdapter.describe_checkpoint(&bytes).is_ok());
        let opaque = control::encode_checkpoint(&bytes).unwrap();
        let mut full: Checkpoint = serde_json::from_value(request["checkpoint"].clone()).unwrap();
        full.state = opaque.state;
        full.digest = checkpoint::compute(
            &full,
            checkpoint::Limits {
                decoded_bytes: control::MAX_CHECKPOINT_BYTES,
                preimage_bytes: control::MAX_CONTROL_BYTES,
            },
        )
        .unwrap()
        .full_digest()
        .into();
        request["checkpoint"] = serde_json::to_value(full).unwrap();
        assert_eq!(
            respond(
                &serde_json::to_vec(&request).unwrap(),
                &open,
                &WordCraftAdapter
            )
            .unwrap_err()
            .to_string(),
            "unqualified origin inventory"
        );
        assert_eq!(document.checkpoint().unwrap(), bytes);
    }
}
