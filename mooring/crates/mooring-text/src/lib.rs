//! Pinned Yrs text boundary and conservative rejection recovery.
//! Authentication, authoritative permission checks, causal dependency extraction,
//! durable receipts and transport scheduling remain outside these primitives.
use yrs::encoding::read::Read;
use yrs::sync::{Message, SyncMessage};
use yrs::updates::decoder::{Decode, Decoder, DecoderV1};
use yrs::{Doc, OffsetKind, Options, ReadTxn, Transact, Update};

pub use yrs;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Access {
    Reader,
    Writer,
}

#[derive(Debug, PartialEq, Eq)]
pub enum InputError {
    TooLarge,
    Malformed,
    TrailingData,
    Forbidden,
    Unsupported,
}

/// Decode one bounded Y-sync message without applying any effects. The host
/// supplies `access`; values sent by the remote participant are never authority.
/// Responses containing document data still require the delivery lease gate.
/// Raw awareness/auth/custom messages are not accepted: authenticated presence
/// must use a separate session-bound path, not arbitrary Yjs client identities.
pub fn decode_input(data: &[u8], access: Access, max_bytes: usize) -> Result<Message, InputError> {
    if data.len() > max_bytes {
        return Err(InputError::TooLarge);
    }
    let mut decoder = DecoderV1::from(data);
    let tag: u8 = decoder.read_var().map_err(|_| InputError::Malformed)?;
    if tag != 0 {
        return Err(InputError::Unsupported);
    }
    let kind: u8 = decoder.read_var().map_err(|_| InputError::Malformed)?;
    if kind > 2 {
        return Err(InputError::Unsupported);
    }
    let payload = decoder
        .read_buf()
        .map_err(|_| InputError::Malformed)?
        .to_vec();
    if !decoder
        .read_to_end()
        .map_err(|_| InputError::Malformed)?
        .is_empty()
    {
        return Err(InputError::TrailingData);
    }
    let message = match kind {
        0 => {
            // Each state-vector entry needs at least two bytes. Check declared
            // count before upstream allocation, even though the raw frame is small.
            let mut preflight = DecoderV1::from(payload.as_slice());
            let entries: u32 = preflight.read_var().map_err(|_| InputError::Malformed)?;
            if entries as usize > payload.len() / 2 {
                return Err(InputError::Malformed);
            }
            let mut vector_decoder = DecoderV1::from(payload.as_slice());
            let vector =
                yrs::StateVector::decode(&mut vector_decoder).map_err(|_| InputError::Malformed)?;
            if !vector_decoder
                .read_to_end()
                .map_err(|_| InputError::Malformed)?
                .is_empty()
            {
                return Err(InputError::TrailingData);
            }
            Message::Sync(SyncMessage::SyncStep1(vector))
        }
        1 | 2 => {
            if access != Access::Writer {
                return Err(InputError::Forbidden);
            }
            Message::Sync(if kind == 1 {
                SyncMessage::SyncStep2(payload)
            } else {
                SyncMessage::Update(payload)
            })
        }
        _ => unreachable!(),
    };
    Ok(message)
}

/// Browser interop uses UTF-16 offsets; adapters must convert UTF-8 byte ranges
/// explicitly and must persist paragraph IDs and the CRDT state across reopen.
pub fn new_document() -> Doc {
    Doc::with_options(Options {
        offset_kind: OffsetKind::Utf16,
        ..Options::default()
    })
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct UnsavedDraft {
    pub op_id: String,
    pub text: String,
    pub reason: String,
}

pub struct Recovery {
    pub document: Doc,
    pub unsaved: Vec<UnsavedDraft>,
}

#[derive(Debug)]
pub struct RecoveryError {
    pub unsaved: Vec<UnsavedDraft>,
    pub reason: String,
}

/// Rebuild from host-authoritative state, keeping every pending intent as an
/// unsaved draft. Never blindly replay updates that may depend on rejected IDs.
/// Decode/apply into a fresh document so failure cannot partially replace the
/// caller's current replica. The caller MUST supply a host-authenticated,
/// size-bounded snapshot and verify scope/revision/receipt before calling.
/// This helper is not an endpoint for arbitrary remote update bytes.
pub fn recover(
    authoritative: &[u8],
    unsaved: Vec<UnsavedDraft>,
) -> Result<Recovery, RecoveryError> {
    let mut decoder = DecoderV1::from(authoritative);
    let update = match Update::decode(&mut decoder) {
        Ok(update) => update,
        Err(error) => {
            return Err(RecoveryError {
                unsaved,
                reason: error.to_string(),
            })
        }
    };
    if !decoder
        .read_to_end()
        .map_err(|error| RecoveryError {
            unsaved: unsaved.clone(),
            reason: error.to_string(),
        })?
        .is_empty()
    {
        return Err(RecoveryError {
            unsaved,
            reason: "trailing snapshot data".into(),
        });
    }
    let document = new_document();
    if let Err(error) = document.transact_mut().apply_update(update) {
        return Err(RecoveryError {
            unsaved,
            reason: error.to_string(),
        });
    }
    if document.transact().has_missing_updates() {
        return Err(RecoveryError {
            unsaved,
            reason: "snapshot has missing causal dependencies".into(),
        });
    }
    Ok(Recovery { document, unsaved })
}

#[cfg(test)]
mod visibility_probe;
