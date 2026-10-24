//! Stateless confirmed-read agent preparation. Go owns all durable decisions.
use crate::{
    candidate::{self, CandidateError},
    command::{self, Command},
    AdapterError, Document,
};
use mooring_core::adapter::{
    CausalPrepareAdapter, PrepareAdapter, Prepared, PreparedCandidate, PreparedFailure,
};
use mooring_core::causal::{CausalContext, CausalPrepareContext, CausalWorkingTargets};
use mooring_core::prepare::{ConfirmedContext, ContextError, WorkingTargets};
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
    /// No complete self-contained asset description is qualified for these bytes.
    UnqualifiedDescription,
}
pub struct Candidate {
    checkpoint: Vec<u8>,
    effects: Vec<PreparedOperationEffects>,
    inventory: Vec<TargetIdentity>,
    native_writer_origin: Option<u64>,
}
impl Candidate {
    /// DATA only: actual typed-agent Doc client ID, never an origin grant or
    /// original author. Human updates use the separately host-bound source.
    pub fn native_writer_origin(&self) -> Option<u64> {
        self.native_writer_origin
    }
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
impl WordCraftAdapter {
    /// Session-only human preparation. Authority is a borrowed host live-store
    /// input, not a client wire claim. A whole immutable atomic is never split.
    pub fn prepare_human_atomic(
        &self,
        checkpoint: &[u8],
        change: &AtomicChange,
        context: &CausalPrepareContext,
        authority: crate::experimental_human_basis::Authority<'_>,
    ) -> Result<Prepared<Candidate>, PrepareError> {
        use crate::experimental_human_basis as human;
        let human::Authority::Writer(host) = authority else {
            return Err(PrepareError::Context);
        };
        if change.operations.len() != 1 || host.current_checkpoint() != checkpoint {
            return Err(PrepareError::Context);
        }
        let operation = &change.operations[0];
        let proposal = host.original_proposal();
        let authored_digest = operation
            .payload
            .get("authored_checkpoint_state_digest")
            .and_then(serde_json::Value::as_str)
            .ok_or(PrepareError::Context)?;
        let encode = |bytes: &[u8]| {
            mooring_protocol::boundary::encode_base64(bytes, 262144)
                .map_err(|_| PrepareError::Context)
        };
        let expected = serde_json::json!({
            "kind": "humancrdt.update",
            "operation_id": proposal.operation_id,
            "origin": proposal.origin.to_string(),
            "update": encode(&proposal.update)?,
            "authored_frontier": encode(&proposal.authored_frontier)?,
            "targets": proposal.targets,
            "authored_checkpoint_state_digest": authored_digest,
        });
        if operation.op_id != proposal.operation_id
            || Some(&operation.payload) != expected.as_object()
        {
            return Err(PrepareError::Context);
        }
        let document = Document::restore(checkpoint).map_err(PrepareError::State)?;
        let identities = inventory(&document)?;
        let qualified = match CausalContext::qualify(context, change, &identities) {
            Ok(value) => value,
            Err(error) => return context_failure(error),
        };
        let mut working = qualified.working();
        if let Err(error) = working.check_current_reads(&operation.op_id) {
            return context_failure(error);
        }
        let prepared = human::prepare_confirmed_basis_with_authored_cut(
            human::Authority::Writer(host),
            authored_digest,
        )
        .map_err(|_| PrepareError::Context)?;
        let targets = prepared
            .affected_targets()
            .iter()
            .map(|id| TargetEffect {
                target_id: id.clone(),
                content_changed: true,
                structure_changed: false,
                created: false,
                deleted: false,
            })
            .collect::<Vec<_>>();
        if let Err(error) = working.advance_effects(&operation.op_id, &targets) {
            return context_failure(error);
        }
        let checkpoint = prepared.checkpoint().map_err(PrepareError::State)?;
        let final_document = Document::restore(&checkpoint).map_err(PrepareError::State)?;
        let identities = inventory(&final_document)?;
        if !working.complete() || working.inventory() != identities {
            return Err(PrepareError::Context);
        }
        Ok(Prepared::Candidate(Candidate {
            checkpoint,
            inventory: identities,
            native_writer_origin: None,
            effects: vec![PreparedOperationEffects {
                op_id: operation.op_id.clone(),
                targets,
            }],
        }))
    }
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
        let document = Document::restore(checkpoint).map_err(PrepareError::State)?;
        let identities = inventory(&document)?;
        let confirmed = match ConfirmedContext::qualify(context, change, &identities) {
            Ok(value) => value,
            Err(error) => return context_failure(error),
        };
        prepare_commands(document, change, Working::Strict(confirmed.working()))
    }
}
impl CausalPrepareAdapter for WordCraftAdapter {
    fn prepare_atomic_causal(
        &self,
        checkpoint: &[u8],
        change: &AtomicChange,
        context: &CausalPrepareContext,
    ) -> Result<Prepared<Candidate>, PrepareError> {
        let document = Document::restore(checkpoint).map_err(PrepareError::State)?;
        let identities = inventory(&document)?;
        let qualified = match CausalContext::qualify(context, change, &identities) {
            Ok(value) => value,
            Err(error) => return context_failure(error),
        };
        prepare_commands(document, change, Working::Causal(qualified.working()))
    }
}
// Different read policies share ONLY application mechanics. Each policy is
// explicitly qualified above against the complete restored inventory; neither
// rewrites immutable observations or forwards to the other policy.
enum Working<'a> {
    Strict(WorkingTargets<'a>),
    Causal(CausalWorkingTargets<'a>),
}
impl Working<'_> {
    fn check_current_reads(&mut self, op_id: &str) -> Result<(), ContextError> {
        match self {
            Self::Strict(value) => value.check_current_reads(op_id),
            Self::Causal(value) => value.check_current_reads(op_id),
        }
    }
    fn advance_effects(
        &mut self,
        op_id: &str,
        effects: &[TargetEffect],
    ) -> Result<(), ContextError> {
        match self {
            Self::Strict(value) => value.advance_effects(op_id, effects),
            Self::Causal(value) => value.advance_effects(op_id, effects),
        }
    }
    fn inventory(&self) -> Vec<TargetIdentity> {
        match self {
            Self::Strict(value) => value.inventory(),
            Self::Causal(value) => value.inventory(),
        }
    }
    fn complete(&self) -> bool {
        match self {
            Self::Strict(value) => value.complete(),
            Self::Causal(value) => value.complete(),
        }
    }
}
fn fresh_native_writer(document: &Document) -> Result<u64, PrepareError> {
    use mooring_text::yrs::{ReadTxn, Transact};
    let origin = document.doc.client_id();
    let vector = document.doc.transact().state_vector();
    if origin.get() == 0
        || origin.get() > crate::origin_inventory::MAX_ORIGIN
        || vector.get(&origin) != 0
        || vector.len() >= 4096
    {
        return Err(PrepareError::Context);
    }
    Ok(origin.get())
}
fn prepare_commands(
    mut document: Document,
    change: &AtomicChange,
    mut working: Working<'_>,
) -> Result<Prepared<Candidate>, PrepareError> {
    // A random collision is a failure, never permission to resume an old
    // namespace. Full checkpoint recovery above includes deleted/GC clocks.
    let writer_origin = fresh_native_writer(&document)?;
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
        native_writer_origin: Some(writer_origin),
    }))
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
    fn causal_context(before: &[u8], change: &AtomicChange, revision: u64) -> CausalPrepareContext {
        use mooring_core::causal::{EntryVersion, QualifiedRead};
        let mut strict = context(before, change);
        strict.document_revision = Counter(revision);
        for target in &mut strict.targets {
            target.revision = Counter(revision);
        }
        for (index, label) in strict.operation_revisions.iter_mut().enumerate() {
            label.revision = Counter(revision + index as u64 + 1);
        }
        let qualified_reads = change
            .operations
            .iter()
            .flat_map(|op| {
                op.read_preconditions
                    .iter()
                    .enumerate()
                    .map(|(index, read)| QualifiedRead {
                        op_id: op.op_id.clone(),
                        read_index: index as u32,
                        target_id: read.target_id.clone(),
                        observed_revision: read.revision,
                        observed_structure_revision: read.structure_revision,
                        entry: Some(EntryVersion {
                            revision: Counter(revision),
                            structure_revision: Counter(5),
                            deleted: false,
                        }),
                        covered: true,
                    })
            })
            .collect();
        CausalPrepareContext {
            document_revision: strict.document_revision,
            targets: strict.targets,
            operation_revisions: strict.operation_revisions,
            qualified_reads,
        }
    }
    #[test]
    fn causal_external_host_coverage_preserves_original_observations_and_full_native() {
        let before = checkpoint();
        let mut op = insert("covered");
        op.dependencies.push("committed-external-effect".into());
        let change = change(vec![op]);
        let immutable = change.clone();
        let ctx = causal_context(&before, &change, 20);
        let Prepared::Candidate(candidate) = WordCraftAdapter
            .prepare_atomic_causal(&before, &change, &ctx)
            .unwrap()
        else {
            panic!("qualified covered command rejected")
        };
        let mut expected = Document::restore(&before).unwrap().native().unwrap();
        expected["body"][0]["text"] =
            json!(expected["body"][0]["text"].as_str().unwrap().to_owned() + " + agent");
        expected["body"][0]["runs"]
            .as_array_mut()
            .unwrap()
            .push(json!({"len":8,"props":{"italic":true,"bold":false}}));
        assert_eq!(
            Document::restore(candidate.checkpoint_bytes())
                .unwrap()
                .native()
                .unwrap(),
            expected
        );
        assert_eq!(change, immutable);
        assert_eq!(candidate.effects()[0].op_id, "covered");
        assert_eq!(
            candidate.inventory(),
            WordCraftAdapter.validate_checkpoint(&before).unwrap()
        );
        let mut strict = context(&before, &change);
        strict.document_revision = ctx.document_revision;
        strict.targets = ctx.targets.clone();
        strict.operation_revisions = ctx.operation_revisions.clone();
        let Prepared::Rejected(failure) = WordCraftAdapter
            .prepare_atomic(&before, &change, &strict)
            .unwrap()
        else {
            panic!("strict policy silently became causal")
        };
        assert_eq!(failure.code, ErrorCode::PreconditionFailed);
    }
    #[test]
    fn causal_internal_effects_require_exact_immutable_dependency_closure() {
        let before = checkpoint();
        for covered in [false, true] {
            let mut second = insert("second");
            if covered {
                second.dependencies.push("first".into());
            }
            let change = change(vec![insert("first"), second]);
            let immutable = change.clone();
            let ctx = causal_context(&before, &change, 10);
            let result = WordCraftAdapter
                .prepare_atomic_causal(&before, &change, &ctx)
                .unwrap();
            match result {
                Prepared::Candidate(candidate) if covered => {
                    let native = Document::restore(candidate.checkpoint_bytes())
                        .unwrap()
                        .native()
                        .unwrap();
                    let original = Document::restore(&before).unwrap().native().unwrap();
                    assert_eq!(
                        native["body"][0]["text"],
                        json!(
                            original["body"][0]["text"].as_str().unwrap().to_owned()
                                + " + agent + agent"
                        )
                    );
                    assert_eq!(
                        candidate
                            .effects()
                            .iter()
                            .map(|effect| effect.op_id.as_str())
                            .collect::<Vec<_>>(),
                        vec!["first", "second"]
                    );
                }
                Prepared::Rejected(failure) if !covered => {
                    assert_eq!(failure.operation_id, "second");
                    assert_eq!(failure.code, ErrorCode::PreconditionFailed);
                }
                _ => panic!("causal prefix policy mismatch"),
            }
            assert_eq!(change, immutable);
            assert_eq!(before, checkpoint_of_same(&before));
        }
    }
    #[test]
    fn causal_malformed_bindings_and_missing_inventory_are_nonterminal() {
        let before = checkpoint();
        let change = change(vec![insert("bound")]);
        for mode in 0..5 {
            let mut ctx = causal_context(&before, &change, 20);
            match mode {
                0 => ctx.qualified_reads.clear(),
                1 => ctx.qualified_reads[0].observed_revision = Counter(11),
                2 => ctx.qualified_reads[0].entry.as_mut().unwrap().revision = Counter(19),
                3 => {
                    ctx.targets.pop();
                }
                _ => ctx.qualified_reads[0].op_id = "foreign".into(),
            }
            assert!(matches!(
                WordCraftAdapter.prepare_atomic_causal(&before, &change, &ctx),
                Err(PrepareError::Context)
            ));
        }
    }
    #[test]
    fn causal_uncovered_read_and_omitted_paragraph_read_fail_actual_member() {
        let before = checkpoint();
        let change = change(vec![insert("uncovered")]);
        let mut ctx = causal_context(&before, &change, 20);
        ctx.qualified_reads[0].covered = false;
        let Prepared::Rejected(failure) = WordCraftAdapter
            .prepare_atomic_causal(&before, &change, &ctx)
            .unwrap()
        else {
            panic!("uncovered stale read accepted")
        };
        assert_eq!(failure.operation_id, "uncovered");
        assert_eq!(failure.code, ErrorCode::PreconditionFailed);
        let mut operation = insert("omitted");
        operation.read_preconditions.clear();
        let change = self::change(vec![operation]);
        let ctx = causal_context(&before, &change, 10);
        let Prepared::Rejected(failure) = WordCraftAdapter
            .prepare_atomic_causal(&before, &change, &ctx)
            .unwrap()
        else {
            panic!("missing application read accepted")
        };
        assert_eq!(failure.operation_id, "omitted");
        assert_eq!(failure.code, ErrorCode::InvalidRequest);
    }
    #[test]
    fn causal_unsupported_human_command_discards_successful_prefix() {
        let before = checkpoint();
        let mut second = operation("human", json!({"kind":"crdt.update","update":"AA=="}));
        second.dependencies.push("first".into());
        let change = change(vec![insert("first"), second]);
        let ctx = causal_context(&before, &change, 10);
        let Prepared::Rejected(failure) = WordCraftAdapter
            .prepare_atomic_causal(&before, &change, &ctx)
            .unwrap()
        else {
            panic!("human updates admitted by agent capability")
        };
        assert_eq!(failure.operation_id, "human");
        assert_eq!(failure.code, ErrorCode::InvalidRequest);
        assert_eq!(before, checkpoint_of_same(&before));
    }
    #[test]
    fn original_human_atomic_binds_payload_cut_and_reads_without_splitting() {
        use crate::experimental_draft::{DraftAccess, DraftReplica};
        use crate::experimental_human_basis::{Authority, HostConfirmedBasis, HostSealedOriginal};
        let before = checkpoint();
        let mut local = DraftReplica::restore(&before, 17, DraftAccess::Writer).unwrap();
        let proposal = local
            .edit(
                "human-op",
                serde_json::from_value(serde_json::json!({
                    "kind":"text.insert", "target_id":"stable-0", "at":{"kind":"end"},
                    "text":" human😀", "properties":null
                }))
                .unwrap(),
            )
            .unwrap();
        let authored = local.optimistic_checkpoint().unwrap();
        let digest = mooring_protocol::control::encode_checkpoint(&authored)
            .unwrap()
            .digest;
        let encode =
            |bytes: &[u8]| mooring_protocol::boundary::encode_base64(bytes, 262144).unwrap();
        let mut op = operation(
            "human-op",
            serde_json::json!({
                "kind":"humancrdt.update", "operation_id":"human-op", "origin":"17",
                "update":encode(&proposal.update), "authored_frontier":encode(&proposal.authored_frontier),
                "targets":proposal.targets, "authored_checkpoint_state_digest":digest,
            }),
        );
        op.read_preconditions.clear();
        let original_change = change(vec![op]);
        for mode in [
            "valid",
            "wrong-cut",
            "extra-field",
            "changed-update",
            "wrong-op",
            "multi-op",
            "explicit-read",
        ] {
            let mut change = original_change.clone();
            match mode {
                "wrong-cut" => {
                    change.operations[0].payload["authored_checkpoint_state_digest"] = serde_json::json!(
                        mooring_protocol::control::encode_checkpoint(&before)
                            .unwrap()
                            .digest
                    )
                }
                "extra-field" => {
                    change.operations[0]
                        .payload
                        .insert("authority".into(), serde_json::json!(true));
                }
                "changed-update" => {
                    change.operations[0].payload["update"] = serde_json::json!("AA==")
                }
                "wrong-op" => change.operations[0].op_id = "substituted".into(),
                "multi-op" => change
                    .operations
                    .push(operation("second", serde_json::json!({}))),
                "explicit-read" => change.operations[0]
                    .read_preconditions
                    .push(ReadPrecondition {
                        target_id: "stable-0".into(),
                        revision: Counter(0),
                        structure_revision: Some(Counter(0)),
                    }),
                _ => {}
            }
            let mut ctx = causal_context(&before, &change, 20);
            if mode == "explicit-read" {
                ctx.qualified_reads[0].covered = false;
            }
            let host = HostConfirmedBasis::from_trusted_confirmed_store(
                &before,
                &before,
                17,
                HostSealedOriginal::from_trusted_original_store(&proposal, b"synthetic-slot", 1)
                    .unwrap(),
                1,
                &[],
            )
            .unwrap();
            let result = WordCraftAdapter.prepare_human_atomic(
                &before,
                &change,
                &ctx,
                Authority::Writer(host),
            );
            if mode == "valid" {
                let Prepared::Candidate(candidate) = result.unwrap() else {
                    panic!("valid original rejected")
                };
                let restored = Document::restore(candidate.checkpoint_bytes()).unwrap();
                assert_eq!(restored.native().unwrap(), local.native().unwrap());
                assert_eq!(candidate.effects()[0].op_id, "human-op");
                assert_eq!(candidate.effects()[0].targets[0].target_id, "stable-0");
            } else {
                assert!(
                    !matches!(result, Ok(Prepared::Candidate(_))),
                    "accepted {mode}"
                );
            }
            assert_eq!(local.optimistic_checkpoint().unwrap(), authored);
        }
        assert!(WordCraftAdapter
            .prepare_human_atomic(
                b"invalid",
                &original_change,
                &causal_context(&before, &original_change, 20),
                Authority::Reader
            )
            .is_err());
    }
    #[test]
    fn actual_agent_candidate_delta_preserves_ids_between_original_human_a_and_b() {
        use crate::experimental_draft::{DraftAccess, DraftReplica};
        use crate::experimental_human_basis::{
            self as human, HostConfirmedBasis, HostSavedDelta, HostSavedOwnPrefix,
            HostSealedOriginal,
        };
        use crate::experimental_human_current as current;
        use mooring_text::yrs::{updates::decoder::Decode, Update};
        for peer_first in [false, true] {
            let base = checkpoint();
            let mut local = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
            let cmd = |text: &str| {
                serde_json::from_value(json!({"kind":"text.insert","target_id":"stable-0","at":{"kind":"end"},"text":text,"properties":null})).unwrap()
            };
            let a = local.edit("A", cmd(" ownA")).unwrap();
            let authored_a = local.optimistic_checkpoint().unwrap();
            let b = local.edit("B", cmd(" ownB😀")).unwrap();
            let authored_b = local.optimistic_checkpoint().unwrap();
            let original_b = b.clone();
            let saved_a = current::prepare_current(
                current::Authority::Writer(current::HostWriter::current(&base, 17).unwrap()),
                &a.authored_frontier,
                &a.update,
                &a.targets,
            )
            .unwrap()
            .checkpoint()
            .unwrap();
            let mut format = operation(
                "agent-format",
                json!({"kind":"text.format","target_id":"stable-0","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":true}}),
            );
            format.dependencies.push("agent-insert".into());
            let agent = change(vec![insert("agent-insert"), format]);
            let agent_basis = if peer_first { &base } else { &saved_a };
            let Prepared::Candidate(agent_candidate) = WordCraftAdapter
                .prepare_atomic_causal(
                    agent_basis,
                    &agent,
                    &causal_context(agent_basis, &agent, 20),
                )
                .unwrap()
            else {
                panic!("actual agent candidate rejected")
            };
            let agent_checkpoint = agent_candidate.checkpoint_bytes();
            let delta = Document::restore(agent_checkpoint)
                .unwrap()
                .update_since(agent_basis)
                .unwrap();
            let decoded = Update::decode_v1(&delta).unwrap();
            let origins = decoded
                .insertions(true)
                .iter()
                .map(|(client, _)| client.get())
                .collect::<Vec<_>>();
            assert_eq!(
                origins.len(),
                1,
                "actual agent delta must have one allocated origin"
            );
            assert_ne!(origins[0], 17);
            assert_eq!(agent_candidate.native_writer_origin(), Some(origins[0]));
            crate::origin_inventory::inspect(agent_basis)
                .unwrap()
                .check_unused(origins[0])
                .unwrap();
            let targets = vec!["stable-0".to_string()];
            let saved_a = if peer_first {
                let peer_history = [HostSavedDelta::from_trusted_committed_store(
                    b"actual-saved-agent",
                    origins[0],
                    &delta,
                    &targets,
                )
                .unwrap()];
                let context = HostConfirmedBasis::from_trusted_confirmed_store(
                    &base,
                    agent_checkpoint,
                    17,
                    HostSealedOriginal::from_trusted_original_store(&a, b"actual-saved-A", 1)
                        .unwrap(),
                    1,
                    &peer_history,
                )
                .unwrap();
                human::prepare_confirmed_basis(human::Authority::Writer(context))
                    .unwrap()
                    .checkpoint()
                    .unwrap()
            } else {
                saved_a
            };
            let global = if peer_first {
                saved_a.as_slice()
            } else {
                agent_checkpoint
            };
            let verified_new = Document::restore(global).unwrap();
            let mut transcript = [
                HostSavedDelta::from_trusted_committed_store(
                    b"actual-saved-A",
                    17,
                    &a.update,
                    &a.targets,
                )
                .unwrap(),
                HostSavedDelta::from_trusted_committed_store(
                    b"actual-saved-agent",
                    origins[0],
                    &delta,
                    &targets,
                )
                .unwrap(),
            ];
            if peer_first {
                transcript.reverse();
            }
            let digest = |bytes: &[u8]| {
                mooring_protocol::control::encode_checkpoint(bytes)
                    .unwrap()
                    .digest
            };
            let authored_a_digest = digest(&authored_a);
            let saved_a_digest = digest(&saved_a);
            let own = [HostSavedOwnPrefix::from_trusted_live_slot(
                HostSealedOriginal::from_trusted_original_store(&a, b"actual-saved-A", 1).unwrap(),
                &authored_a_digest,
                &saved_a_digest,
            )
            .unwrap()];
            let host = HostConfirmedBasis::from_trusted_live_store_with_own_prefix(
                &base,
                global,
                17,
                HostSealedOriginal::from_trusted_original_store(&b, b"original-B", 2).unwrap(),
                2,
                &transcript,
                &own,
            )
            .unwrap();
            let candidate = human::prepare_confirmed_basis_with_authored_cut(
                human::Authority::Writer(host),
                &digest(&authored_b),
            )
            .unwrap();
            let data = crate::document_update::encode(&delta, &targets).unwrap();
            let before_delivery = local.optimistic_checkpoint().unwrap();
            assert!(local
                .apply_verified_document_update("{\"authority\":true}")
                .is_err());
            assert_eq!(local.optimistic_checkpoint().unwrap(), before_delivery);
            local.apply_verified_document_update(&data).unwrap();
            assert_eq!(
                crate::document_update::allocation_origins(&delta).unwrap(),
                origins
            );
            assert_eq!(candidate.native().unwrap(), local.native().unwrap());
            assert_eq!(
                Document::restore(&candidate.checkpoint().unwrap())
                    .unwrap()
                    .block_ids()
                    .unwrap(),
                verified_new.block_ids().unwrap()
            );
            assert_eq!(b, original_b);
            let merged_native = local.native().unwrap();
            local.undo_e3("undo-B").unwrap();
            assert_eq!(
                local.native().unwrap(),
                verified_new.native().unwrap(),
                "own insertion undo must preserve FULL actual agent formatting"
            );
            local.redo_e3("redo-B").unwrap();
            assert_eq!(local.native().unwrap(), merged_native);
            assert!(
                Document::restore(&base)
                    .unwrap()
                    .update_since(global)
                    .is_err(),
                "cannot replay newer basis into older state"
            );
        }
    }
    #[test]
    fn native_writer_collision_and_zero_fail_before_any_new_allocation() {
        use mooring_text::yrs::{
            updates::decoder::Decode, ClientID, Doc, OffsetKind, Options, ReadTxn, StateVector,
            Transact, Update,
        };
        let bytes = checkpoint();
        let original = Document::restore(&bytes).unwrap();
        let raw = original
            .doc
            .transact()
            .encode_state_as_update_v1(&StateVector::default());
        let occupied = original
            .doc
            .transact()
            .state_vector()
            .iter()
            .next()
            .unwrap()
            .0
            .get();
        for origin in [0, occupied] {
            let forced = Doc::with_options(Options {
                client_id: ClientID::new(origin),
                offset_kind: OffsetKind::Utf16,
                ..Default::default()
            });
            forced
                .transact_mut()
                .apply_update(Update::decode_v1(&raw).unwrap())
                .unwrap();
            let document = Document {
                doc: forced,
                read_only: original.read_only,
                project_text: original.project_text,
                model: original.model,
            };
            let before = document.checkpoint().unwrap();
            assert!(fresh_native_writer(&document).is_err());
            assert_eq!(document.checkpoint().unwrap(), before);
        }
    }
    #[test]
    fn delete_only_agent_delta_has_no_allocations_and_retains_actual_writer_data() {
        let before = checkpoint();
        let change = change(vec![operation(
            "agent-delete",
            json!({"kind":"text.delete","target_id":"stable-0","start":{"kind":"start"},"end":{"kind":"end"}}),
        )]);
        let Prepared::Candidate(candidate) = WordCraftAdapter
            .prepare_atomic_causal(&before, &change, &causal_context(&before, &change, 20))
            .unwrap()
        else {
            panic!("actual delete candidate rejected")
        };
        let actual_writer = candidate.native_writer_origin().unwrap();
        crate::origin_inventory::inspect(&before)
            .unwrap()
            .check_unused(actual_writer)
            .unwrap();
        let restored = Document::restore(candidate.checkpoint_bytes()).unwrap();
        let delta = restored.update_since(&before).unwrap();
        assert!(!delta.is_empty());
        assert!(
            crate::document_update::allocation_origins(&delta)
                .unwrap()
                .is_empty(),
            "delete-only must not invent allocation provenance"
        );
        assert_eq!(restored.native().unwrap()["body"][0]["text"], json!(""));
        let data = crate::document_update::encode(&delta, &["stable-0".into()]).unwrap();
        assert_eq!(crate::document_update::decode(&data).unwrap().update, delta);
    }
}
