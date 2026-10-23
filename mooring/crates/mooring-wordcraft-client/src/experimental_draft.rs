//! Experimental local draft facade. No host human admission or clock witness.
use super::{encode, parse, state_error, MAX_CHECKPOINT};
use mooring_wordcraft::{
    command::Command,
    experimental_draft::{ClientError, DraftAccess, DraftReplica, Proposal, RecoveryDraft},
};
use wasm_bindgen::prelude::*;

fn error(error: ClientError) -> String {
    match error {
        ClientError::Forbidden => "forbidden",
        ClientError::InvalidOrigin => "invalid_origin",
        ClientError::InvalidState => "invalid_state",
        ClientError::InvalidCommand => "invalid_command",
        ClientError::InvalidOperation => "invalid_operation",
        ClientError::OperationConflict => "op_id_conflict",
        ClientError::RetiredOperation => "retired_operation",
        ClientError::NoChange => "no_change",
        ClientError::RecoveryRequired => "recovery_required",
        ClientError::Limit => "quota_exceeded",
    }
    .into()
}
fn origin(raw: &str) -> Result<u64, String> {
    if raw.len() > 16 {
        return Err("invalid_origin".into());
    }
    let value = mooring_protocol::Counter::try_from(raw.to_owned())
        .map_err(|_| "invalid_origin")?
        .0;
    if value == 0 || value >= (1 << 53) {
        return Err("invalid_origin".into());
    }
    Ok(value)
}
fn access(raw: &str) -> Result<DraftAccess, String> {
    match raw {
        "reader" => Ok(DraftAccess::Reader),
        "writer" => Ok(DraftAccess::Writer),
        _ => Err("invalid_request".into()),
    }
}

/// This local role is a UX gate, not host authority. The host independently
/// authenticates and binds origins before admitting ANY update-bearing frame.
#[wasm_bindgen]
pub struct WordCraftLocalDraft {
    client: DraftReplica,
}
#[wasm_bindgen]
impl WordCraftLocalDraft {
    #[wasm_bindgen(constructor)]
    pub fn new(
        checkpoint: &[u8],
        assigned_origin: &str,
        role: &str,
    ) -> Result<WordCraftLocalDraft, String> {
        if checkpoint.len() > MAX_CHECKPOINT {
            return Err("quota_exceeded".into());
        }
        Ok(Self {
            client: DraftReplica::restore(checkpoint, origin(assigned_origin)?, access(role)?)
                .map_err(error)?,
        })
    }
    pub fn native_json(&self) -> Result<String, String> {
        encode(&self.client.native().map_err(state_error)?)
    }
    pub fn stable_ids_json(&self) -> Result<String, String> {
        encode(&self.client.stable_ids().map_err(state_error)?)
    }
    pub fn optimistic_checkpoint(&self) -> Result<Vec<u8>, String> {
        let bytes = self.client.optimistic_checkpoint().map_err(state_error)?;
        if bytes.len() > MAX_CHECKPOINT {
            return Err("quota_exceeded".into());
        }
        Ok(bytes)
    }
    /// Local command execution generates original CRDT bytes ONCE; the server
    /// must validate/apply those bytes, never reexecute this command grammar.
    pub fn edit(
        &mut self,
        operation_id: &str,
        command_json: &str,
    ) -> Result<WordCraftLocalProposal, String> {
        let command: Command = parse(command_json)?;
        Ok(WordCraftLocalProposal {
            proposal: self.client.edit(operation_id, command).map_err(error)?,
        })
    }
    pub fn undo(&mut self, operation_id: &str) -> Result<WordCraftLocalProposal, String> {
        Ok(WordCraftLocalProposal {
            proposal: self.client.undo(operation_id).map_err(error)?,
        })
    }
    pub fn redo(&mut self, operation_id: &str) -> Result<WordCraftLocalProposal, String> {
        Ok(WordCraftLocalProposal {
            proposal: self.client.redo(operation_id).map_err(error)?,
        })
    }
    /// Only already authenticated host deltas; this method itself does NOT
    /// authenticate a peer, scope, permission, epoch or revision.
    pub fn apply_verified_host_delta(
        &mut self,
        assigned_origin: &str,
        update: &[u8],
        targets_json: &str,
    ) -> Result<(), String> {
        self.client
            .apply_verified_host_delta(
                origin(assigned_origin)?,
                update,
                &parse::<Vec<String>>(targets_json)?,
            )
            .map_err(error)
    }
    pub fn recover(
        &mut self,
        checkpoint: &[u8],
        fresh_origin: &str,
        role: &str,
    ) -> Result<WordCraftLocalRecoveryDraft, String> {
        if checkpoint.len() > MAX_CHECKPOINT {
            return Err("quota_exceeded".into());
        }
        Ok(WordCraftLocalRecoveryDraft {
            draft: self
                .client
                .recover(checkpoint, origin(fresh_origin)?, access(role)?)
                .map_err(error)?,
        })
    }
}
#[wasm_bindgen]
pub struct WordCraftLocalProposal {
    proposal: Proposal,
}
#[wasm_bindgen]
impl WordCraftLocalProposal {
    pub fn operation_id(&self) -> String {
        self.proposal.operation_id.clone()
    }
    pub fn state(&self) -> String {
        "unconfirmed".into()
    }
    pub fn origin(&self) -> String {
        self.proposal.origin.to_string()
    }
    pub fn update(&self) -> Vec<u8> {
        self.proposal.update.clone()
    }
    pub fn targets_json(&self) -> Result<String, String> {
        encode(&self.proposal.targets)
    }
}
#[wasm_bindgen]
pub struct WordCraftLocalRecoveryDraft {
    draft: RecoveryDraft,
}
#[wasm_bindgen]
impl WordCraftLocalRecoveryDraft {
    pub fn native_json(&self) -> Result<String, String> {
        encode(&self.draft.native)
    }
    pub fn proposal_count(&self) -> usize {
        self.draft.proposals.len()
    }
}
