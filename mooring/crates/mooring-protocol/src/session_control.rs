//! Closed experimental session shape profile, never an admission decision.
//! The caller must prove inherited-channel provenance and explicit successful
//! profile negotiation. Shape validation does not qualify host seals, leases,
//! FIFO barriers, receipts, chunk assembly or application checkpoint state.
use crate::{
    canonical,
    canonical::CanonicalValue,
    control,
    wire::{self, WireError},
};

pub const PROFILE: &str = "mooring.session-causal.v1";
pub const CHECKPOINT_IDENTITY_CAPABILITY: &str = "checkpoint_identity_v1";
/// Fixed initial profile constants; the live driver must use these exact caps.
pub const BATCH_DELAY_MS: u64 = 100;
pub const MAX_BATCH_OPERATIONS: usize = 256;
pub const MAX_OUTSTANDING: usize = 4;
pub const MAX_OUTSTANDING_BYTES: usize = 1024 * 1024;
pub const MAX_UNSENT_BYTES: usize = 262_144;
pub const MAX_PARTICIPANTS: usize = 100;
pub const MAX_GATE_INPUT_BYTES: usize = 262_144;
pub const MAX_GLOBAL_INPUT_BYTES: usize = 4 * 1024 * 1024;
pub const DELIVERY_LEASE_MS: u64 = 2000;
pub const MAX_REASSEMBLY_BYTES: usize = 4 * 1024 * 1024;
pub const MAX_QUEUED_CONTROL_BYTES: usize = 4 * 1024 * 1024;
pub const MAX_QUEUED_CONTROLS: usize = 128;
pub const MAX_BATCH_BYTES: usize = 262_144;
pub const MAX_TRANSFER_BYTES: usize = 2 * 1024 * 1024;
pub const CHUNK_BYTES: usize = 65_536;

pub fn is_private_type(value: &str) -> bool {
    matches!(
        value,
        "session_open"
            | "session_restore"
            | "session_restored"
            | "session_opened"
            | "session_submit_batch"
            | "session_commit_result"
            | "session_participant_open"
            | "session_participant_input"
            | "session_participant_output"
            | "session_status_request"
            | "session_status_result"
            | "session_describe_checkpoint"
            | "session_checkpoint_described"
    )
}

/// Validates only the new session envelopes. Existing causal preparation stays
/// on its separate exact validator; the session driver explicitly composes it.
/// Unknown types cannot obtain a larger resource budget through this function.
pub fn validate(raw: &[u8], peer: control::Peer) -> Result<CanonicalValue, WireError> {
    if matches!(peer, control::Peer::Participant) {
        return Err(WireError::InvalidShape);
    }
    control::preflight(raw)?;
    let value =
        canonical::normalize_with_limit(raw, control::MAX_CONTROL_BYTES).map_err(|error| {
            match error {
                canonical::CanonicalError::Size => WireError::ResourceLimit,
                _ => WireError::InvalidJson,
            }
        })?;
    let message_type = value.value()["type"]
        .as_str()
        .ok_or(WireError::InvalidShape)?;
    let allowed = match peer {
        control::Peer::InheritedHost => matches!(
            message_type,
            "session_open"
                | "session_restore"
                | "session_commit_result"
                | "session_participant_open"
                | "session_participant_input"
                | "session_status_result"
                | "session_describe_checkpoint"
        ),
        control::Peer::InheritedRuntime => matches!(
            message_type,
            "session_restored"
                | "session_opened"
                | "session_submit_batch"
                | "session_participant_output"
                | "session_status_request"
                | "session_checkpoint_described"
        ),
        control::Peer::Participant => false,
    };
    if !allowed || !crate::shape::envelope(value.value()) {
        return Err(WireError::InvalidShape);
    }
    // Only complete restoration and committed checkpoint replies need the
    // larger private frame. Ordinary wrappers include their metadata in the
    // small raw/canonical budget, so normalization cannot hide escaped raw
    // inner bytes. Drivers reject over-budget ingress before sealing it.
    if !matches!(
        message_type,
        "session_restore"
            | "session_commit_result"
            | "session_describe_checkpoint"
            | "session_checkpoint_described"
    ) {
        wire::preflight_with_limit(raw, MAX_BATCH_BYTES)?;
        if value.bytes().len() > MAX_BATCH_BYTES {
            return Err(WireError::ResourceLimit);
        }
    }
    // Wrapping an ordinary participant input cannot relax its public profile.
    // Explicit chunk output remains separately bound and negotiated by driver.
    if matches!(
        message_type,
        "session_participant_input"
            | "session_participant_output"
            | "session_status_request"
            | "session_status_result"
    ) {
        let inner =
            serde_json::to_vec(&value.value()["message"]).map_err(|_| WireError::InvalidJson)?;
        wire::validate_bounded_envelope(&inner)?;
    }
    Ok(value)
}
