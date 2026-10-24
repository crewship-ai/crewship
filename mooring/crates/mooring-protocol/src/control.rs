//! Negotiated inherited-channel control profile, never a participant profile.
//! The caller must establish channel provenance and explicit profile selection
//! before constructing InheritedHost/InheritedRuntime. JSON cannot attest it.
use crate::{
    boundary::canonical_base64_decoded_len,
    canonical::{self, CanonicalValue},
    wire::{self, WireError},
    OpaquePreparedCheckpoint, OpaquePreparedCheckpointEncoding,
};

pub const PROFILE: &str = "mooring.prepare.v1";
/// Shape contract only; no application capability is enabled by this constant.
pub const CAUSAL_PROFILE: &str = "mooring.prepare-causal.v1";
pub const MAX_CONTROL_BYTES: usize = 2_097_152;
pub const MAX_CHECKPOINT_BYTES: usize = 1_572_864;

pub enum Peer {
    InheritedHost,
    InheritedRuntime,
    Participant,
}

/// The private bootstrap still uses the public 262144 resource bound. Its
/// control offer/selection fields are not legal on participant transports.
pub fn validate_bootstrap(raw: &[u8], peer: Peer) -> Result<CanonicalValue, WireError> {
    if matches!(peer, Peer::Participant) {
        return Err(WireError::InvalidShape);
    }
    let normalized = wire::normalize_bounded(raw)?;
    let expected = match peer {
        Peer::InheritedHost => "hello",
        Peer::InheritedRuntime => "runtime_ready",
        Peer::Participant => unreachable!(),
    };
    if normalized.value()["type"] != expected || !crate::shape::envelope(normalized.value()) {
        return Err(WireError::InvalidShape);
    }
    Ok(normalized)
}

pub fn is_control_type(value: &str) -> bool {
    crate::session_control::is_private_type(value)
        || matches!(
            value,
            "validate_checkpoint"
                | "checkpoint_validated"
                | "prepare_atomic"
                | "atomic_prepared"
                | "validate_checkpoint_causal"
                | "checkpoint_validated_causal"
                | "prepare_atomic_causal"
                | "atomic_prepared_causal"
        )
}

/// Exactly four control envelopes, on an explicitly selected private channel.
/// This validates shape/resources/direction, not scope, revision, fingerprint,
/// checkpoint digest or adapter effects. Those checks precede app interpretation.
pub fn validate(raw: &[u8], peer: Peer) -> Result<CanonicalValue, WireError> {
    validate_selected(raw, peer, false)
}

/// Separate explicitly selected causal contract, never a fallback for strict
/// requests. Caller-verified inherited channel provenance and profile selection
/// are required before use; this does not qualify SQL history or app effects.
pub fn validate_causal(raw: &[u8], peer: Peer) -> Result<CanonicalValue, WireError> {
    validate_selected(raw, peer, true)
}

fn validate_selected(raw: &[u8], peer: Peer, causal: bool) -> Result<CanonicalValue, WireError> {
    if matches!(peer, Peer::Participant) {
        return Err(WireError::InvalidShape);
    }
    let value = normalize(raw)?;
    let message_type = value.value()["type"]
        .as_str()
        .ok_or(WireError::InvalidShape)?;
    let allowed = match (peer, causal) {
        (Peer::InheritedHost, false) => {
            matches!(message_type, "validate_checkpoint" | "prepare_atomic")
        }
        (Peer::InheritedRuntime, false) => {
            matches!(message_type, "checkpoint_validated" | "atomic_prepared")
        }
        (Peer::InheritedHost, true) => matches!(
            message_type,
            "validate_checkpoint_causal" | "prepare_atomic_causal"
        ),
        (Peer::InheritedRuntime, true) => matches!(
            message_type,
            "checkpoint_validated_causal" | "atomic_prepared_causal"
        ),
        (Peer::Participant, _) => false,
    };
    if !allowed || !crate::shape::envelope(value.value()) {
        return Err(WireError::InvalidShape);
    }
    Ok(value)
}

/// Resource-only preflight for a previously selected inherited control channel.
/// This is not syntax, schema, authority, or profile negotiation validation.
pub fn preflight(raw: &[u8]) -> Result<(), WireError> {
    wire::preflight_with_limit(raw, MAX_CONTROL_BYTES)
}

fn normalize(raw: &[u8]) -> Result<CanonicalValue, WireError> {
    preflight(raw)?;
    canonical::normalize_with_limit(raw, MAX_CONTROL_BYTES).map_err(|error| match error {
        canonical::CanonicalError::Size => WireError::ResourceLimit,
        _ => WireError::InvalidJson,
    })
}

/// Preflight canonical padding and decoded length BEFORE allocating. The digest
/// covers exact opaque checkpoint bytes, not the outer normalized JSON value.
pub fn decode_checkpoint(checkpoint: &OpaquePreparedCheckpoint) -> Result<Vec<u8>, WireError> {
    let length = canonical_base64_decoded_len(&checkpoint.state, MAX_CHECKPOINT_BYTES)
        .map_err(|_| WireError::InvalidShape)?;
    let mut bytes = Vec::with_capacity(length);
    for quartet in checkpoint.state.as_bytes().chunks_exact(4) {
        let a = sextet(quartet[0]);
        let b = sextet(quartet[1]);
        bytes.push((a << 2) | (b >> 4));
        if quartet[2] != b'=' {
            let c = sextet(quartet[2]);
            bytes.push((b << 4) | (c >> 2));
            if quartet[3] != b'=' {
                bytes.push((c << 6) | sextet(quartet[3]));
            }
        }
    }
    if canonical::digest(&bytes) != checkpoint.digest {
        return Err(WireError::InvalidShape);
    }
    Ok(bytes)
}

pub fn encode_checkpoint(bytes: &[u8]) -> Result<OpaquePreparedCheckpoint, WireError> {
    if bytes.len() > MAX_CHECKPOINT_BYTES {
        return Err(WireError::ResourceLimit);
    }
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut state = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        state.push(ALPHABET[(chunk[0] >> 2) as usize] as char);
        state.push(
            ALPHABET[(((chunk[0] & 3) << 4) | (chunk.get(1).copied().unwrap_or(0) >> 4)) as usize]
                as char,
        );
        state.push(if chunk.len() > 1 {
            ALPHABET[(((chunk[1] & 15) << 2) | (chunk.get(2).copied().unwrap_or(0) >> 6)) as usize]
                as char
        } else {
            '='
        });
        state.push(if chunk.len() > 2 {
            ALPHABET[(chunk[2] & 63) as usize] as char
        } else {
            '='
        });
    }
    Ok(OpaquePreparedCheckpoint {
        encoding: OpaquePreparedCheckpointEncoding::Base64,
        state,
        digest: canonical::digest(bytes),
    })
}

// Input is already proven canonical standard base64 by the preflight above.
fn sextet(byte: u8) -> u8 {
    match byte {
        b'A'..=b'Z' => byte - b'A',
        b'a'..=b'z' => byte - b'a' + 26,
        b'0'..=b'9' => byte - b'0' + 52,
        b'+' => 62,
        b'/' => 63,
        _ => unreachable!("base64 preflight"),
    }
}
