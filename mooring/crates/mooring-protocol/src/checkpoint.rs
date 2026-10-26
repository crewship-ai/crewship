//! Generic checkpoint identity, distinct from opaque prepared-content hashes.
//! Pure bounded computation: no application interpretation or host authority.
use crate::{boundary, canonical, shape, wire, Checkpoint, CheckpointEncoding};
use serde_json::json;
use std::collections::BTreeSet;

pub const MAX_DECODED_BYTES: usize = 8 * 1024 * 1024;
pub const MAX_PREIMAGE_BYTES: usize = 16 * 1024 * 1024;
#[derive(Clone, Copy)]
pub struct Limits {
    pub decoded_bytes: usize,
    pub preimage_bytes: usize,
}
#[derive(Debug, PartialEq, Eq)]
pub enum Error {
    Capacity,
    Shape,
    Encoding,
    NonCanonicalJson,
    DuplicateAsset,
    Digest,
}
/// `full_digest` binds scope/revision/creator generation/model/assets as well as
/// state. `content_digest` binds only exact decoded bytes and is never a wire
/// checkpoint identity. Neither authenticates an arbitrary imported checkpoint.
#[derive(Debug)]
pub struct Identity {
    full_digest: String,
    content_digest: String,
    state: Vec<u8>,
}
impl Identity {
    pub fn full_digest(&self) -> &str {
        &self.full_digest
    }
    pub fn content_digest(&self) -> &str {
        &self.content_digest
    }
    pub fn state(&self) -> &[u8] {
        &self.state
    }
}
fn digest_shape(text: &str) -> bool {
    text.len() == 71
        && text.starts_with("sha256:")
        && text.as_bytes()[7..]
            .iter()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(b))
}
/// Computes the SPEC v0.4 full-tuple digest, ignoring the input digest field so
/// a host can construct a new checkpoint. Call `verify` to check an existing
/// identity. Limits are trusted caller policy, not supplied by incoming JSON.
/// JSON-encoded state also uses the explicit bounded JSON implementation profile
/// (depth64/32768 structures); oversized models must use a qualified encoding/path.
pub fn compute(checkpoint: &Checkpoint, limits: Limits) -> Result<Identity, Error> {
    if limits.decoded_bytes > MAX_DECODED_BYTES
        || limits.preimage_bytes == 0
        || limits.preimage_bytes > MAX_PREIMAGE_BYTES
    {
        return Err(Error::Capacity);
    }
    if ![
        &checkpoint.scope.application_id,
        &checkpoint.scope.workspace_id,
        &checkpoint.scope.artifact_id,
        &checkpoint.format_version,
    ]
    .into_iter()
    .all(|x| wire::valid_identifier(x))
        || ![
            &checkpoint.versions.protocol_version,
            &checkpoint.versions.model_version,
            &checkpoint.versions.engine_version,
        ]
        .into_iter()
        .all(|x| shape::version(x))
        || checkpoint.assets.len() > 4096
    {
        return Err(Error::Shape);
    }
    let mut ids = BTreeSet::new();
    for asset in &checkpoint.assets {
        if !wire::valid_identifier(&asset.asset_id) || !digest_shape(&asset.digest) {
            return Err(Error::Shape);
        }
        if !ids.insert(asset.asset_id.as_str()) {
            return Err(Error::DuplicateAsset);
        }
    }
    let state = match checkpoint.encoding {
        CheckpointEncoding::Base64 => {
            boundary::decode_base64(&checkpoint.state, limits.decoded_bytes)
                .map_err(|_| Error::Encoding)?
        }
        CheckpointEncoding::Json => {
            wire::preflight_with_limit(checkpoint.state.as_bytes(), limits.decoded_bytes)
                .map_err(|_| Error::Capacity)?;
            let normalized =
                canonical::normalize_with_limit(checkpoint.state.as_bytes(), limits.decoded_bytes)
                    .map_err(|_| Error::NonCanonicalJson)?;
            if normalized.bytes() != checkpoint.state.as_bytes() {
                return Err(Error::NonCanonicalJson);
            }
            checkpoint.state.as_bytes().to_vec()
        }
    };
    let state_base64 =
        boundary::encode_base64(&state, limits.decoded_bytes).map_err(|_| Error::Capacity)?;
    if state_base64.len() > limits.preimage_bytes {
        return Err(Error::Capacity);
    }
    let mut assets: Vec<_> = checkpoint.assets.iter().collect();
    assets.sort_by(|left, right| {
        left.asset_id
            .encode_utf16()
            .cmp(right.asset_id.encode_utf16())
    });
    let preimage = json!({"domain":"mooring.checkpoint.v0.4","scope":checkpoint.scope,"revision":checkpoint.revision,"generation":checkpoint.generation,"format_version":checkpoint.format_version,"versions":checkpoint.versions,"state_base64":state_base64,"assets":assets});
    let raw = serde_json::to_vec(&preimage).map_err(|_| Error::Shape)?;
    let canonical = canonical::normalize_with_limit(&raw, limits.preimage_bytes)
        .map_err(|_| Error::Capacity)?;
    Ok(Identity {
        full_digest: canonical.digest(),
        content_digest: canonical::digest(&state),
        state,
    })
}
pub fn verify(checkpoint: &Checkpoint, limits: Limits) -> Result<Identity, Error> {
    if !digest_shape(&checkpoint.digest) {
        return Err(Error::Digest);
    }
    let identity = compute(checkpoint, limits)?;
    if identity.full_digest != checkpoint.digest {
        return Err(Error::Digest);
    }
    Ok(identity)
}
