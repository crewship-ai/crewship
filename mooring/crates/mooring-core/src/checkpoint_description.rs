//! Application-owned complete checkpoint metadata. No host revision or authority.
use crate::adapter::PrepareAdapter;
use mooring_protocol::{Asset, Versions};

/// Describes the exact opaque checkpoint bytes that were validated/restored.
/// Versions and format belong to the actual qualified application source, not
/// a client claim or the generic collaboration crate's package version.
///
/// Assets are the COMPLETE external checkpoint manifest. Embedded data already
/// covered by checkpoint bytes needs no separate asset only when the app has
/// qualified that it is self-contained. Unknown/external reference graphs must
/// fail nonterminally; writability alone does not prove an empty manifest.
#[derive(Debug)]
pub struct CheckpointDescription {
    pub format_version: String,
    pub versions: Versions,
    pub assets: Vec<Asset>,
}

/// Explicit opt-in; there is no default empty manifest or inferred format.
/// The caller enforces negotiated byte/resource bounds before restoration and
/// after serialization, authenticates the inherited channel, checks exact app
/// registration/versions, and validates the manifest/full-tuple digest before
/// retaining metadata in the same authorized host transaction as the state.
/// Historical unknown metadata must never be guessed from the current adapter.
/// This method cannot install state, issue Saved or grant content access.
pub trait DescribeCheckpoint: PrepareAdapter {
    fn describe_checkpoint(&self, checkpoint: &[u8]) -> Result<CheckpointDescription, Self::Error>;
}
