//! Closed application payload for an authenticated host DocumentUpdate frame.
//! Parsing is DATA validation only; this module verifies no host authority.
use crate::{checkpoint_wire, AdapterError, MAX_BLOCKS};
use serde::{Deserialize, Serialize};

const KIND: &str = "wordcraft.yrs-update.v1";
const MAX_DATA_BYTES: usize = 262144;

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Wire {
    kind: String,
    update: String,
    targets: Vec<String>,
}
/// Untrusted parsed application data, never a receipt or permission grant.
pub struct DocumentUpdateData {
    pub update: Vec<u8>,
    pub targets: Vec<String>,
}
fn targets(ids: &[String]) -> Result<(), AdapterError> {
    if ids.is_empty()
        || ids.len() > MAX_BLOCKS
        || ids.windows(2).any(|pair| pair[0] >= pair[1])
        || ids.iter().any(|id| {
            id.is_empty()
                || id.len() > 128
                || !id
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
        })
    {
        return Err(AdapterError::InvalidIdentity);
    }
    Ok(())
}
/// The transport must ALSO enforce its complete outer frame byte bound.
/// Targets are the sorted union of actual host-Saved application effects.
pub fn encode(update: &[u8], affected_targets: &[String]) -> Result<String, AdapterError> {
    if update.len() > MAX_DATA_BYTES {
        return Err(AdapterError::Limit);
    }
    targets(affected_targets)?;
    checkpoint_wire::preflight(update)?;
    let wire = Wire {
        kind: KIND.into(),
        update: mooring_protocol::boundary::encode_base64(update, MAX_DATA_BYTES)
            .map_err(|_| AdapterError::Limit)?,
        targets: affected_targets.to_vec(),
    };
    let data = serde_json::to_string(&wire).map_err(|_| AdapterError::InvalidCheckpoint)?;
    if data.len() > MAX_DATA_BYTES {
        return Err(AdapterError::Limit);
    }
    Ok(data)
}
/// Closed/bounded syntax only. Caller must verify the actual host connection,
/// scope, sequence, permission and checkpoint bindings BEFORE applying content.
pub fn decode(data: &str) -> Result<DocumentUpdateData, AdapterError> {
    if data.len() > MAX_DATA_BYTES {
        return Err(AdapterError::Limit);
    }
    let wire: Wire = serde_json::from_str(data).map_err(|_| AdapterError::InvalidCheckpoint)?;
    if wire.kind != KIND {
        return Err(AdapterError::InvalidCheckpoint);
    }
    targets(&wire.targets)?;
    let update = mooring_protocol::boundary::decode_base64(&wire.update, MAX_DATA_BYTES)
        .map_err(|_| AdapterError::InvalidCheckpoint)?;
    checkpoint_wire::preflight(&update)?;
    Ok(DocumentUpdateData {
        update,
        targets: wire.targets,
    })
}

/// Actual encoded allocation origins, not an authenticated source identity.
/// Delete-only updates return an empty set. No synthetic zero origin is added.
pub fn allocation_origins(update: &[u8]) -> Result<Vec<u64>, AdapterError> {
    use mooring_text::yrs::{updates::decoder::Decode, Update};
    if update.len() > MAX_DATA_BYTES {
        return Err(AdapterError::Limit);
    }
    checkpoint_wire::preflight(update)?;
    let decoded = Update::decode_v1(update).map_err(|_| AdapterError::InvalidCheckpoint)?;
    let mut origins = decoded
        .insertions(true)
        .iter()
        .map(|(client, _)| client.get())
        .collect::<Vec<_>>();
    origins.sort_unstable();
    origins.dedup();
    Ok(origins)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn bounded_closed_data_roundtrips_original_bytes_without_peer_identity() {
        let raw = [0, 0]; // valid empty Yrs update, not a durable outcome
        let targets = vec!["p_one".into()];
        let data = encode(&raw, &targets).unwrap();
        let parsed = decode(&data).unwrap();
        assert_eq!(parsed.update, raw);
        assert_eq!(parsed.targets, targets);
        assert!(!data.contains("origin"));
        assert!(allocation_origins(&raw).unwrap().is_empty());
        for invalid in [
            data.replace("wordcraft.yrs-update.v1", "unknown"),
            data.replace("\"AAA=\"", "\"AAB=\""), // noncanonical pad bits
            data.replace("\"targets\":[\"p_one\"]", "\"targets\":[]"),
            data.replace(
                "\"targets\":[\"p_one\"]",
                "\"targets\":[\"p_one\",\"p_one\"]",
            ),
            data.replace("\"targets\":[\"p_one\"]", "\"targets\":[\"z\",\"a\"]"),
            data.replacen('{', "{\"authority\":true,", 1),
            data.replacen('{', "{\"kind\":\"wordcraft.yrs-update.v1\",", 1),
            " ".repeat(MAX_DATA_BYTES + 1),
        ] {
            assert!(decode(&invalid).is_err(), "accepted invalid closed data");
        }
    }
}
