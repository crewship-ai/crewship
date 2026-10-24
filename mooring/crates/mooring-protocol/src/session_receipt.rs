//! Exact host-result binding for the explicitly selected inherited session lane.
//! The caller supplies verified transport and retained pending membership. This
//! value cannot establish transport provenance, participant rights, or Saved.
use crate::{
    boundary::{self, BoundaryError, CommitBinding, HostReceipt, PendingAtomic, Transport},
    canonical, checkpoint, control, session_control, Counter, Fence, Scope, SessionCommitResult,
    Versions,
};
use std::collections::BTreeSet;

pub struct Binding<'a> {
    pub request_id: &'a str,
    pub scope: &'a Scope,
    pub session_id: &'a str,
    pub runtime_ref: &'a str,
    pub fence: &'a Fence,
    pub versions: &'a Versions,
    pub changes: &'a [PendingAtomic],
}

/// Checked committed bytes, not a speculative preparation candidate. The app
/// must still restore them and validate complete actual target inventory before
/// replacing its confirmed live document. Digest here is the private control
/// opaque-content SHA256 profile, not a generic full-tuple checkpoint digest.
pub struct Verified {
    result: SessionCommitResult,
    receipt: HostReceipt,
    checkpoint: Vec<u8>,
}
impl Verified {
    pub fn result(&self) -> &SessionCommitResult {
        &self.result
    }
    pub fn receipt(&self) -> &HostReceipt {
        &self.receipt
    }
    pub fn checkpoint_bytes(&self) -> &[u8] {
        &self.checkpoint
    }
}

pub fn validate(
    raw: &[u8],
    transport: &Transport<'_>,
    expected: &Binding<'_>,
) -> Result<Verified, BoundaryError> {
    if !matches!(transport, Transport::AuthenticatedHost { .. }) {
        return Err(BoundaryError::Forbidden);
    }
    let normalized = session_control::validate(raw, control::Peer::InheritedHost)
        .map_err(BoundaryError::Wire)?;
    if normalized.value()["type"] != "session_commit_result" {
        return Err(BoundaryError::Binding);
    }
    let result: SessionCommitResult =
        serde_json::from_value(normalized.value().clone()).map_err(|_| BoundaryError::Binding)?;
    if result.request_id != expected.request_id
        || &result.scope != expected.scope
        || result.session_id != expected.session_id
        || result.runtime_ref != expected.runtime_ref
        || &result.fence != expected.fence
        || &result.versions != expected.versions
    {
        return Err(BoundaryError::Binding);
    }
    // Change order is operational here and must match the exact sent batch;
    // operation order inside each immutable atomic member is checked below.
    let receipt_value = &normalized.value()["receipt"];
    let changes = receipt_value["changes"]
        .as_array()
        .ok_or(BoundaryError::Receipt)?;
    if changes.len() != expected.changes.len()
        || changes
            .iter()
            .zip(expected.changes)
            .any(|(change, pending)| change["change_id"] != pending.change_id)
    {
        return Err(BoundaryError::Binding);
    }
    for change in changes {
        for operation in change["operations"]
            .as_array()
            .ok_or(BoundaryError::Receipt)?
        {
            if operation["reason"]["code"] == "unknown_outcome"
                || operation["reason"]["root_cause"]["code"] == "unknown_outcome"
            {
                // Uncertainty cannot consume a pending immutable intent.
                return Err(BoundaryError::Receipt);
            }
        }
    }
    let receipt_raw = serde_json::to_vec(receipt_value).map_err(|_| BoundaryError::Receipt)?;
    // Only this known inner grammar obtains the negotiated session budget.
    let receipt_normalized =
        canonical::normalize_with_limit(&receipt_raw, control::MAX_CONTROL_BYTES)
            .map_err(|_| BoundaryError::Receipt)?;
    let receipt = boundary::validate_checked_session_receipt(
        receipt_normalized,
        transport,
        &CommitBinding {
            request_id: expected.request_id,
            scope: expected.scope,
            generation: expected.fence.generation,
            changes: expected.changes,
        },
    )?;
    let cp = &result.checkpoint;
    if &cp.scope != expected.scope
        || &cp.versions != expected.versions
        || cp.revision != receipt.revision()
        || cp.generation == Counter(0)
    {
        return Err(BoundaryError::Binding);
    }
    let checkpoint = checkpoint::verify(
        cp,
        checkpoint::Limits {
            decoded_bytes: control::MAX_CHECKPOINT_BYTES,
            preimage_bytes: control::MAX_CONTROL_BYTES,
        },
    )
    .map_err(|_| BoundaryError::Encoding)?
    .state()
    .to_vec();
    let mut targets = BTreeSet::new();
    for target in &result.targets {
        if target.structure_revision > target.revision
            || target.revision > cp.revision
            || !targets.insert(target.target_id.as_str())
        {
            return Err(BoundaryError::Binding);
        }
    }
    Ok(Verified {
        result,
        receipt,
        checkpoint,
    })
}
