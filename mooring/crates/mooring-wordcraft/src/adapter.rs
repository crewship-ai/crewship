//! Stateless confirmed-read agent preparation. Go owns all durable decisions.
use crate::{
    candidate::{self, CandidateError},
    command::{self, Command},
    AdapterError, Document,
};
use mooring_core::adapter::{PrepareAdapter, Prepared, PreparedCandidate, PreparedFailure};
use mooring_core::prepare::{ConfirmedContext, ContextError};
use mooring_protocol::{
    AtomicChange, ErrorCode, PrepareContext, PreparedOperationEffects, TargetEffect, TargetIdentity,
};

pub const STRUCTURE_TARGET: &str = "wordcraft:document-structure";
pub const METADATA_TARGET: &str = "wordcraft:document-metadata";
pub struct WordCraftAdapter;
#[derive(Debug)]
pub enum PrepareError {
    State(AdapterError),
    Context,
}
pub struct Candidate {
    checkpoint: Vec<u8>,
    effects: Vec<PreparedOperationEffects>,
    inventory: Vec<TargetIdentity>,
}
impl PreparedCandidate for Candidate {
    fn checkpoint_bytes(&self) -> &[u8] {
        &self.checkpoint
    }
    fn effects(&self) -> &[PreparedOperationEffects] {
        &self.effects
    }
    fn inventory(&self) -> &[TargetIdentity] {
        &self.inventory
    }
}
fn inventory(document: &Document) -> Result<Vec<TargetIdentity>, PrepareError> {
    let mut identities = document
        .block_ids()
        .map_err(PrepareError::State)?
        .into_iter()
        .map(|target_id| TargetIdentity {
            target_id,
            deleted: false,
        })
        .collect::<Vec<_>>();
    identities.extend(
        [STRUCTURE_TARGET, METADATA_TARGET].map(|id| TargetIdentity {
            target_id: id.into(),
            deleted: false,
        }),
    );
    identities.sort_by(|a, b| a.target_id.cmp(&b.target_id));
    Ok(identities)
}
impl PrepareAdapter for WordCraftAdapter {
    type Candidate = Candidate;
    type Error = PrepareError;
    fn validate_checkpoint(&self, checkpoint: &[u8]) -> Result<Vec<TargetIdentity>, Self::Error> {
        inventory(&Document::restore(checkpoint).map_err(PrepareError::State)?)
    }
    fn prepare_atomic(
        &self,
        checkpoint: &[u8],
        change: &AtomicChange,
        context: &PrepareContext,
    ) -> Result<Prepared<Candidate>, PrepareError> {
        // Restore once into owned state. No external state is touched, and any
        // failure below drops the entire prefix rather than returning it.
        let mut document = Document::restore(checkpoint).map_err(PrepareError::State)?;
        let identities = inventory(&document)?;
        let confirmed = match ConfirmedContext::qualify(context, change, &identities) {
            Ok(value) => value,
            Err(error) => return context_failure(error),
        };
        let mut working = confirmed.working();
        let mut effects = Vec::new();
        for operation in &change.operations {
            if let Err(error) = working.check_current_reads(&operation.op_id) {
                return context_failure(error);
            }
            let command = match command::decode(&operation.payload) {
                Ok(command) => command,
                Err(_) => {
                    return Ok(reject(
                        &operation.op_id,
                        ErrorCode::InvalidRequest,
                        "unsupported or invalid WordCraft command",
                    ))
                }
            };
            let target = match &command {
                Command::Insert { target_id, .. }
                | Command::Delete { target_id, .. }
                | Command::Format { target_id, .. } => target_id,
            };
            // Every command depends on its existing paragraph, including start
            // and end anchors. A global base or relative item is not an
            // implicitly declared confirmed read observation.
            if !operation
                .read_preconditions
                .iter()
                .any(|read| read.target_id == *target)
            {
                return Ok(reject(
                    &operation.op_id,
                    ErrorCode::InvalidRequest,
                    "WordCraft paragraph read observation required",
                ));
            }
            if let Err(error) = candidate::apply(&mut document, &command) {
                let (code, message) = match error {
                    CandidateError::InvalidState => {
                        return Err(PrepareError::State(AdapterError::InvalidCheckpoint))
                    }
                    CandidateError::InvalidCommand | CandidateError::InvalidRange => (
                        ErrorCode::InvalidRequest,
                        "invalid WordCraft command or range",
                    ),
                    CandidateError::ReadOnly => (
                        ErrorCode::Forbidden,
                        "unqualified WordCraft document is read-only",
                    ),
                    CandidateError::MissingTarget => (
                        ErrorCode::TargetDeleted,
                        "WordCraft paragraph target unavailable",
                    ),
                    CandidateError::UnresolvedAnchor => (
                        ErrorCode::PreconditionFailed,
                        "WordCraft relative anchor unavailable or invalid",
                    ),
                };
                return Ok(reject(&operation.op_id, code, message));
            }
            let targets = vec![TargetEffect {
                target_id: target.clone(),
                content_changed: true,
                structure_changed: false,
                created: false,
                deleted: false,
            }];
            if let Err(error) = working.advance_effects(&operation.op_id, &targets) {
                return context_failure(error);
            }
            effects.push(PreparedOperationEffects {
                op_id: operation.op_id.clone(),
                targets,
            });
        }
        let identities = inventory(&document)?;
        if !working.complete() || working.inventory() != identities {
            return Err(PrepareError::Context);
        }
        Ok(Prepared::Candidate(Candidate {
            checkpoint: document.checkpoint().map_err(PrepareError::State)?,
            effects,
            inventory: identities,
        }))
    }
}
fn reject(operation_id: &str, code: ErrorCode, message: &str) -> Prepared<Candidate> {
    Prepared::Rejected(PreparedFailure {
        operation_id: operation_id.into(),
        code,
        message: message.into(),
    })
}
fn context_failure(error: ContextError) -> Result<Prepared<Candidate>, PrepareError> {
    match error {
        ContextError::Invalid => Err(PrepareError::Context),
        ContextError::Read(failure) => Ok(Prepared::Rejected(failure)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_protocol::{
        Counter, ErrorCode, Operation, PrepareContextReadPolicy, PreparedOperationRevision,
        ReadPrecondition, TargetVersion,
    };
    use serde_json::{json, Value};
    fn checkpoint() -> Vec<u8> {
        let native: Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json"))
                .unwrap();
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("stable-{i}"))
            .collect();
        Document::import(native, ids).unwrap().checkpoint().unwrap()
    }
    fn operation(id: &str, payload: Value) -> Operation {
        Operation {
            op_id: id.into(),
            fingerprint: "a".repeat(64),
            participant_ref: "agent".into(),
            base_revision: Counter(10),
            dependencies: vec![],
            read_preconditions: vec![ReadPrecondition {
                target_id: "stable-0".into(),
                revision: Counter(10),
                structure_revision: Some(Counter(5)),
            }],
            payload: payload.as_object().unwrap().clone(),
        }
    }
    fn insert(id: &str) -> Operation {
        operation(
            id,
            json!({"kind":"text.insert","target_id":"stable-0","at":{"kind":"end"},"text":" + agent","properties":{"italic":true,"bold":false}}),
        )
    }
    fn change(operations: Vec<Operation>) -> AtomicChange {
        AtomicChange {
            change_id: "atomic".into(),
            operations,
            membership_hash: "b".repeat(64),
            task_id: None,
        }
    }
    fn context(checkpoint: &[u8], change: &AtomicChange) -> PrepareContext {
        PrepareContext {
            read_policy: PrepareContextReadPolicy::AgentConfirmedCurrent,
            document_revision: Counter(10),
            targets: WordCraftAdapter
                .validate_checkpoint(checkpoint)
                .unwrap()
                .into_iter()
                .map(|id| TargetVersion {
                    target_id: id.target_id,
                    revision: Counter(10),
                    structure_revision: Counter(5),
                    deleted: id.deleted,
                })
                .collect(),
            operation_revisions: change
                .operations
                .iter()
                .enumerate()
                .map(|(i, op)| PreparedOperationRevision {
                    op_id: op.op_id.clone(),
                    revision: Counter(11 + i as u64),
                })
                .collect(),
        }
    }
    #[test]
    fn actual_d1_candidate_has_full_native_state_effects_and_complete_inventory() {
        let before = checkpoint();
        let change = change(vec![insert("first")]);
        let ctx = context(&before, &change);
        let Prepared::Candidate(candidate) = WordCraftAdapter
            .prepare_atomic(&before, &change, &ctx)
            .unwrap()
        else {
            panic!("unexpected rejection")
        };
        let mut expected = Document::restore(&before).unwrap().native().unwrap();
        let text = expected["body"][0]["text"].as_str().unwrap().to_owned() + " + agent";
        let old = expected["body"][0]["runs"].as_array_mut().unwrap();
        old.push(json!({"len":8,"props":{"italic":true,"bold":false}}));
        expected["body"][0]["text"] = json!(text);
        assert_eq!(
            Document::restore(candidate.checkpoint_bytes())
                .unwrap()
                .native()
                .unwrap(),
            expected
        );
        assert_eq!(
            candidate.inventory(),
            WordCraftAdapter.validate_checkpoint(&before).unwrap()
        );
        assert_eq!(candidate.effects().len(), 1);
        assert_eq!(candidate.effects()[0].op_id, "first");
        assert_eq!(candidate.effects()[0].targets[0].target_id, "stable-0");
        assert!(candidate.effects()[0].targets[0].content_changed);
        assert!(!candidate.effects()[0].targets[0].structure_changed);
    }
    #[test]
    fn stale_after_prefix_returns_actual_second_failure_without_partial_candidate() {
        let before = checkpoint();
        let change = change(vec![insert("first"), insert("second")]);
        let ctx = context(&before, &change);
        let Prepared::Rejected(failure) = WordCraftAdapter
            .prepare_atomic(&before, &change, &ctx)
            .unwrap()
        else {
            panic!("stale accepted")
        };
        assert_eq!(failure.operation_id, "second");
        assert_eq!(failure.code, ErrorCode::PreconditionFailed);
        assert!(failure.qualify(&change).is_ok());
        assert_eq!(before, checkpoint_of_same(&before));
    }
    fn checkpoint_of_same(bytes: &[u8]) -> Vec<u8> {
        Document::restore(bytes).unwrap().checkpoint().unwrap()
    }
    #[test]
    fn missing_trusted_inventory_is_nonterminal_even_with_valid_command() {
        let before = checkpoint();
        let change = change(vec![insert("first")]);
        let mut ctx = context(&before, &change);
        ctx.targets.pop();
        assert!(matches!(
            WordCraftAdapter.prepare_atomic(&before, &change, &ctx),
            Err(PrepareError::Context)
        ));
    }
    #[test]
    fn missing_or_foreign_only_paragraph_read_set_is_not_implicit_authority() {
        let before = checkpoint();
        for target in [None, Some(METADATA_TARGET)] {
            let mut operation = insert("actual");
            operation.read_preconditions = target
                .into_iter()
                .map(|id| ReadPrecondition {
                    target_id: id.into(),
                    revision: Counter(10),
                    structure_revision: Some(Counter(5)),
                })
                .collect();
            let change = change(vec![operation]);
            let ctx = context(&before, &change);
            let Prepared::Rejected(failure) = WordCraftAdapter
                .prepare_atomic(&before, &change, &ctx)
                .unwrap()
            else {
                panic!("missing paragraph read accepted")
            };
            assert_eq!(failure.operation_id, "actual");
            assert_eq!(failure.code, ErrorCode::InvalidRequest);
        }
    }
    #[test]
    fn invalid_second_command_discards_prefix_and_identifies_its_real_member() {
        let before = checkpoint();
        let mut invalid = operation(
            "invalid-second",
            json!({"kind":"crdt.update","update":"AA=="}),
        );
        invalid.read_preconditions[0].target_id = "stable-1".into();
        let change = change(vec![insert("first"), invalid]);
        let ctx = context(&before, &change);
        let Prepared::Rejected(failure) = WordCraftAdapter
            .prepare_atomic(&before, &change, &ctx)
            .unwrap()
        else {
            panic!("unsupported human raw update accepted")
        };
        assert_eq!(failure.operation_id, "invalid-second");
        assert_eq!(failure.code, ErrorCode::InvalidRequest);
        assert_eq!(checkpoint_of_same(&before), before);
    }
    #[test]
    fn unsupported_native_document_remains_read_only_with_complete_inventory() {
        let before = checkpoint();
        let doc = Document::restore(&before).unwrap();
        let mut native = doc.native().unwrap();
        native["comments"] = json!({"unqualified":{"id":"unqualified"}});
        let readonly = Document::import(native, doc.block_ids().unwrap()).unwrap();
        assert!(readonly.is_read_only());
        let before = readonly.checkpoint().unwrap();
        let change = change(vec![insert("readonly-op")]);
        let ctx = context(&before, &change);
        assert_eq!(ctx.targets.len(), 10);
        let Prepared::Rejected(failure) = WordCraftAdapter
            .prepare_atomic(&before, &change, &ctx)
            .unwrap()
        else {
            panic!("read-only write accepted")
        };
        assert_eq!(failure.operation_id, "readonly-op");
        assert_eq!(failure.code, ErrorCode::Forbidden);
    }
    #[test]
    fn tampered_checkpoint_is_nonterminal_not_a_guessed_operation_rejection() {
        let before = checkpoint();
        let change = change(vec![insert("first")]);
        let ctx = context(&before, &change);
        let mut tampered: Value = serde_json::from_slice(&before).unwrap();
        tampered["native"]["body"][0]["text"] = json!("forged");
        assert!(matches!(
            WordCraftAdapter.prepare_atomic(&serde_json::to_vec(&tampered).unwrap(), &change, &ctx),
            Err(PrepareError::State(_))
        ));
    }
}
