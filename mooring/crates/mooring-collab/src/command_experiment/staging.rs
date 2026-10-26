//! Trusted host/SQL callback boundary, not a user protocol or a second store.
use super::*;
use sha2::{Digest, Sha256};

/// An immutable candidate for ONE atomic host SQL transaction. Private fields
/// prevent a caller from substituting native bytes, receipts, or original binds.
/// This value is NOT a commit receipt and must never be advertised as Saved.
/// It is deliberately not Deserialize: a client cannot mint a commit handle.
pub struct StagedCommit {
    next: Checkpoint,
    checkpoint: Vec<u8>,
    receipt: Value,
    base_digest: String,
    base_state_digest: String,
    digest: String,
    input: Vec<u8>,
    base_revision: u64,
    epoch: u64,
    participant: String,
    author: String,
    command_time: String,
    op_id: String,
}
impl StagedCommit {
    pub fn checkpoint(&self) -> &[u8] {
        &self.checkpoint
    }
    pub fn candidate_receipt(&self) -> &Value {
        &self.receipt
    }
    pub fn fingerprint(&self) -> &str {
        std::str::from_utf8(&self.input).expect("host-generated SHA-256 fingerprint")
    }
    pub fn checkpoint_digest(&self) -> &str {
        &self.digest
    }
    pub fn base_checkpoint_digest(&self) -> &str {
        &self.base_digest
    }
    pub fn format(&self) -> &str {
        &self.next.format
    }
    pub fn engine_identity(&self) -> &Value {
        &self.next.engine_identity
    }
    pub fn document(&self) -> &str {
        &self.next.document
    }
    pub fn generation(&self) -> &str {
        &self.next.generation
    }
    pub fn op_id(&self) -> &str {
        &self.op_id
    }
    pub fn base_revision(&self) -> u64 {
        self.base_revision
    }
    pub fn revision(&self) -> u64 {
        self.next.revision
    }
    fn qualified(&self, admission: &Admission) -> bool {
        admission.writer
            && admission.epoch == self.epoch
            && admission.participant == self.participant
            && admission.author == self.author
    }
}
pub(super) fn digest(bytes: &[u8]) -> String {
    format!("sha256:{:x}", Sha256::digest(bytes))
}

impl<E: Engine> Host<E> {
    /// Build the exact candidate checkpoint AND candidate receipt without
    /// changing P, undo, seen operations, or executing another engine command.
    /// Only the host may write these bytes and receipt atomically to canonical
    /// SQL. Merely preparing/serializing/storing an uncommitted SQL transaction
    /// is NOT confirmation. Keep K on commit error or unknown outcome.
    pub fn stage_commit(&self, op_id: &str, current: &Admission) -> Result<StagedCommit> {
        let k = self.k.as_ref().ok_or("no_pending")?;
        if k.envelope.op_id != op_id {
            return Err("wrong_pending");
        }
        if !current.writer
            || current.participant != k.envelope.participant
            || current.epoch != k.epoch
            || current.author != k.author
        {
            return Err("commit_admission_denied");
        }
        let mut next = self.state.clone();
        next.revision = next.revision.checked_add(1).ok_or("revision_exhausted")?;
        if let Some(index) = k.undo {
            next.history
                .get_mut(index)
                .ok_or("undo_unavailable")?
                .undone = true;
        } else {
            next.history.push(Undo {
                participant: k.envelope.participant.clone(),
                op_id: op_id.into(),
                before: self.state.p.clone(),
                after: k.snapshot.clone(),
                undone: false,
            });
            while next.history.len() > HISTORY || history_size(&next.history) > HISTORY_BYTES {
                next.history.remove(0);
            }
        }
        next.p = k.snapshot.clone();
        let mut receipt = json!({"state":"host_committed","op_id":op_id,"revision":next.revision,
            "actor":k.envelope.participant,"author":k.author,"command_time":k.time,
            "fingerprint":String::from_utf8_lossy(&k.input)});
        if let Some(selection) = &k.selection {
            receipt["selection"] = selection.clone();
        }
        next.seen.insert(
            op_id.into(),
            Seen {
                input: k.input.clone(),
                result: receipt.clone(),
            },
        );
        let checkpoint = serde_json::to_vec(&next).map_err(|_| "checkpoint_encode")?;
        if checkpoint.len() > 32 * 1024 * 1024 {
            return Err("checkpoint_limit");
        }
        let base_digest = self.canonical_digest.clone();
        let base_state_digest = digest(&self.checkpoint()?);
        let digest = digest(&checkpoint);
        Ok(StagedCommit {
            next,
            checkpoint,
            receipt,
            base_digest,
            base_state_digest,
            digest,
            input: k.input.clone(),
            base_revision: self.state.revision,
            epoch: k.epoch,
            participant: k.envelope.participant.clone(),
            author: k.author.clone(),
            command_time: k.time.clone(),
            op_id: op_id.into(),
        })
    }

    /// TRUSTED SQL SUCCESS CALLBACK ONLY. The caller must have verified that
    /// canonical SQL actually committed this handle's exact checkpoint+receipt
    /// in ONE transaction under the original admitted authority. This method
    /// itself performs no SQL and establishes no SQL proof. Do not expose it to
    /// user JSON or call after a timeout/unknown outcome or a mere write attempt.
    ///
    /// Fresh admission and the original candidate/base are checked again here.
    /// A failed ACK leaves P and K untouched: SQL may already have committed,
    /// so the caller must reconcile canonical SQL instead of rejecting/retrying.
    /// A crash after SQL commit needs no journal: restore the stored checkpoint
    /// in a fresh generation; the original op_id then returns its known receipt.
    pub fn confirm_committed(
        &mut self,
        staged: &StagedCommit,
        current: &Admission,
    ) -> Result<Value> {
        if !staged.qualified(current) {
            return Err("commit_admission_denied");
        }
        if self.state.document != staged.next.document {
            return Err("commit_binding_mismatch");
        }
        if let Some(known) = self.state.seen.get(staged.op_id()) {
            return if known.input == staged.input && known.result == staged.receipt {
                Ok(known.result.clone())
            } else {
                Err("op_id_conflict")
            };
        }
        let k = self.k.as_ref().ok_or("no_pending")?;
        if self.state.generation != staged.next.generation
            || self.state.revision != staged.base_revision
            || self.canonical_digest != staged.base_digest
            || digest(&self.checkpoint()?) != staged.base_state_digest
            || k.envelope.op_id != staged.op_id
            || k.input != staged.input
            || k.epoch != staged.epoch
            || k.author != staged.author
            || k.time != staged.command_time
            || k.envelope.participant != staged.participant
            || k.snapshot != staged.next.p
            || k.selection.as_ref() != staged.receipt.get("selection")
        {
            return Err("commit_binding_mismatch");
        }
        self.state = staged.next.clone();
        self.canonical_digest = staged.digest.clone();
        self.k = None;
        Ok(staged.receipt.clone())
    }
}
