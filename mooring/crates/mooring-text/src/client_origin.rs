use crate::{Recovery, RecoveryError, UnsavedDraft};
use yrs::{ClientID, Doc, OffsetKind, Options};

pub const MAX_CLIENT_ORIGIN: u64 = (1_u64 << 53) - 1;

/// Validated numeric data only. This value proves neither host issuance nor
/// permission, uniqueness, an unburned identity or an authenticated source.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct CheckedClientOrigin(u64);

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct InvalidClientOrigin;

impl std::fmt::Display for InvalidClientOrigin {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("CRDT origin must be in 1..=2^53-1")
    }
}
impl std::error::Error for InvalidClientOrigin {}

impl CheckedClientOrigin {
    pub fn new(value: u64) -> Result<Self, InvalidClientOrigin> {
        if value == 0 || value > MAX_CLIENT_ORIGIN {
            Err(InvalidClientOrigin)
        } else {
            Ok(Self(value))
        }
    }

    pub fn get(self) -> u64 {
        self.0
    }
}

/// Construct a UTF-16 document using explicit numeric data, not issuance proof.
pub fn new_document_with_origin(origin: CheckedClientOrigin) -> Doc {
    Doc::with_options(Options {
        client_id: ClientID::new(origin.get()),
        offset_kind: OffsetKind::Utf16,
        ..Options::default()
    })
}

/// Recover with a fresh explicit origin, retaining every unsaved draft.
/// Inherits [`crate::recover`]'s caller-verified, host-authenticated, size-bounded
/// snapshot precondition; this is not an arbitrary remote-update decoder.
/// Rejects an origin already present in the complete snapshot history, including
/// deleted/GC'ed allocations. This is not a continuation constructor.
/// Original authority and the complete host burned-ID union remain caller duties.
pub fn recover_with_origin(
    authoritative: &[u8],
    unsaved: Vec<UnsavedDraft>,
    origin: CheckedClientOrigin,
) -> Result<Recovery, RecoveryError> {
    crate::recover_using(
        authoritative,
        unsaved,
        || new_document_with_origin(origin),
        Some(origin),
    )
}
