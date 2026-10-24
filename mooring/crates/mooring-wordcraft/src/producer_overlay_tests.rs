//! Actual Yrs producer mechanics; all source/host contexts and ACKs are synthetic.
use super::*;
use crate::adapter::WordCraftAdapter;
use mooring_core::adapter::PrepareAdapter;
use mooring_protocol::{
    AtomicChange, Counter, PrepareContextReadPolicy, PreparedOperationRevision, TargetVersion,
};
use mooring_text::{
    yrs::{updates::decoder::Decode, ClientID, Update},
    CheckedClientOrigin,
};
use serde_json::{json, Value};
fn scope() -> Scope {
    Scope {
        workspace_id: "synthetic-workspace".into(),
        artifact_id: "synthetic-document".into(),
        application_id: "wordcraft".into(),
    }
}
fn source() -> ProducerOverlay {
    let native: Value =
        serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json")).unwrap();
    let ids = (0..native["body"].as_array().unwrap().len())
        .map(|i| format!("stable-{i}"))
        .collect();
    let baseline = Document::import_with_origin(native, ids, CheckedClientOrigin::new(17).unwrap())
        .unwrap()
        .checkpoint()
        .unwrap();
    let doc = Document::restore_with_fresh_origin(&baseline, CheckedClientOrigin::new(31).unwrap())
        .unwrap();
    ProducerOverlay::from_owned_document(scope(), doc)
        .unwrap_or_else(|_| panic!("source construction"))
}
fn packet(id: &str, text: &str, revision: u64) -> Vec<u8> {
    let value = json!({"change_id":format!("change-{id}"),"membership_hash":"0".repeat(64),"operations":[{"op_id":id,"fingerprint":"0".repeat(64),"participant_ref":"synthetic-source","base_revision":revision.to_string(),"dependencies":[],"read_preconditions":[{"target_id":"stable-0","revision":revision.to_string(),"structure_revision":"0"}],"payload":{"kind":"text.insert","target_id":"stable-0","at":{"kind":"end"},"text":text,"properties":{"bold":false,"italic":true}}}]});
    let bound = fingerprint_atomic(&scope(), &serde_json::to_vec(&value).unwrap())
        .unwrap()
        .with_fingerprints();
    let mut bytes = b" \n".to_vec();
    bytes.extend(serde_json::to_vec_pretty(&bound).unwrap());
    bytes
}
fn context(overlay: &ProducerOverlay, packet: &[u8], revision: u64) -> PrepareContext {
    let change: AtomicChange = serde_json::from_slice(packet).unwrap();
    let checkpoint = overlay.latest().unwrap().checkpoint().unwrap();
    PrepareContext {
        read_policy: PrepareContextReadPolicy::AgentConfirmedCurrent,
        document_revision: Counter(revision),
        targets: WordCraftAdapter
            .validate_checkpoint(&checkpoint)
            .unwrap()
            .into_iter()
            .map(|t| TargetVersion {
                target_id: t.target_id,
                revision: Counter(revision),
                structure_revision: Counter(0),
                deleted: t.deleted,
            })
            .collect(),
        operation_revisions: change
            .operations
            .iter()
            .enumerate()
            .map(|(index, op)| PreparedOperationRevision {
                op_id: op.op_id.clone(),
                revision: Counter(revision + index as u64 + 1),
            })
            .collect(),
    }
}
fn append(overlay: &mut ProducerOverlay, id: &str, rev: u64) -> (u64, Vec<u8>) {
    let packet = packet(id, id, rev);
    let ctx = context(overlay, &packet, rev);
    (overlay.append_original(&packet, &ctx).unwrap(), packet)
}
fn clock(doc: &Document, id: u64) -> u32 {
    doc.doc.transact().state_vector().get(&ClientID::new(id))
}
#[test]
fn ack_a_only_settles_its_cut_without_rewinding_actual_producer_b() {
    let mut overlay = source();
    let baseline = overlay.confirmed_checkpoint().to_vec();
    let (a, original_a) = append(&mut overlay, "A", 0);
    let cut_a = overlay.pending().front().unwrap().checkpoint().to_vec();
    let a_clock = clock(overlay.latest().unwrap(), 31);
    let (_, original_b) = append(&mut overlay, "B", 1);
    let b = overlay.pending().back().unwrap();
    let cut_b = b.checkpoint().to_vec();
    let delta_b = b.delta().to_vec();
    let before_b = b.authored_frontier().to_vec();
    assert_eq!(overlay.pending().front().unwrap().original(), original_a);
    assert_eq!(b.original(), original_b);
    let latest_b = overlay.latest().unwrap().native().unwrap();
    let b_clock = clock(overlay.latest().unwrap(), 31);
    assert!(b_clock > a_clock);
    // Original B is an actual authored delta, not a command reexecution.
    let restored_a = Document::restore(&cut_a).unwrap();
    restored_a
        .doc
        .transact_mut()
        .apply_update(Update::decode_v1(&delta_b).unwrap())
        .unwrap();
    assert_eq!(restored_a.native().unwrap(), latest_b);
    // Unknown leaves both immutable cuts and the actual source unchanged.
    assert_eq!(overlay.pending().len(), 2);
    assert_eq!(overlay.confirmed_checkpoint(), baseline);
    let settled = overlay.acknowledge_front(a, &cut_a).unwrap();
    assert_eq!(settled.original(), original_a);
    let global_confirmed = Document::restore(&cut_a).unwrap();
    assert_ne!(global_confirmed.native().unwrap(), latest_b);
    assert_eq!(overlay.confirmed_checkpoint(), cut_a);
    assert_eq!(overlay.latest().unwrap().doc.client_id().get(), 31);
    assert_eq!(overlay.latest().unwrap().native().unwrap(), latest_b);
    assert_eq!(clock(overlay.latest().unwrap(), 31), b_clock);
    let b = overlay.pending().front().unwrap();
    assert_eq!(b.original(), original_b);
    assert_eq!(b.checkpoint(), cut_b);
    assert_eq!(b.delta(), delta_b);
    assert_eq!(b.authored_frontier(), before_b);
}
#[test]
fn rejected_b_retires_overlay_and_recovery_uses_fresh_origin_without_reusing_b_ids() {
    let mut overlay = source();
    let (a, _) = append(&mut overlay, "A", 0);
    let cut_a = overlay.pending().front().unwrap().checkpoint().to_vec();
    let a_clock = clock(overlay.latest().unwrap(), 31);
    let (_, original_b) = append(&mut overlay, "B", 1);
    let b_clock = clock(overlay.latest().unwrap(), 31);
    overlay.acknowledge_front(a, &cut_a).unwrap();
    overlay.retire();
    let c = packet("C", "C", 2);
    let ctx = context(&overlay, &c, 2);
    assert_eq!(overlay.append_original(&c, &ctx), Err(Error::Inactive));
    let recovery = overlay.into_recovery();
    assert_eq!(recovery.pending[0].original(), original_b);
    assert!(clock(recovery.latest.as_ref().unwrap(), 31) > a_clock);
    assert!(Document::restore_with_fresh_origin(
        &recovery.confirmed_checkpoint,
        CheckedClientOrigin::new(31).unwrap()
    )
    .is_err());
    let fresh = Document::restore_with_fresh_origin(
        &recovery.confirmed_checkpoint,
        CheckedClientOrigin::new(47).unwrap(),
    )
    .unwrap();
    let mut new_source = ProducerOverlay::from_owned_document(scope(), fresh)
        .unwrap_or_else(|_| panic!("fresh source"));
    append(&mut new_source, "C", 1);
    let doc = new_source.latest().unwrap();
    assert_eq!(doc.doc.client_id().get(), 47);
    assert!(clock(doc, 47) > 0);
    assert_eq!(clock(doc, 31), a_clock);
    assert!(clock(doc, 31) < b_clock);
    assert_eq!(recovery.pending[0].original(), original_b);
}
#[test]
fn consuming_prepare_failure_keeps_prior_cuts_and_the_exact_current_original() {
    let mut overlay = source();
    let (_, a) = append(&mut overlay, "A", 0);
    let b = packet("B", "B", 1);
    let mut ctx = context(&overlay, &b, 1);
    ctx.targets.pop();
    assert_eq!(overlay.append_original(&b, &ctx), Err(Error::Preparation));
    assert!(overlay.is_blocked());
    let recovery = overlay.into_recovery();
    assert!(recovery.latest.is_none());
    assert_eq!(recovery.pending[0].original(), a);
    assert_eq!(recovery.attempted_original.unwrap(), b);
}
#[test]
fn capacity_and_correlated_cut_failure_preserve_existing_originals() {
    let mut overlay = source();
    for (n, id) in ["A", "B", "C", "D"].iter().enumerate() {
        append(&mut overlay, id, n as u64);
    }
    let before = overlay.latest().unwrap().checkpoint().unwrap();
    let packet = packet("E", "E", 4);
    let ctx = context(&overlay, &packet, 4);
    assert_eq!(overlay.append_original(&packet, &ctx), Err(Error::Limit));
    assert_eq!(overlay.latest().unwrap().checkpoint().unwrap(), before);
    assert!(!overlay.is_blocked());
    assert!(matches!(
        overlay.acknowledge_front(2, b"wrong"),
        Err(Error::Correlation)
    ));
    assert!(overlay.is_blocked());
    let recovery = overlay.into_recovery();
    assert_eq!(recovery.pending.len(), 4);
    assert_eq!(recovery.latest.unwrap().checkpoint().unwrap(), before);
}

#[test]
fn original_identity_replay_tampering_and_oversized_metadata_precede_mutation() {
    let mut overlay = source();
    let (a, original_a) = append(&mut overlay, "A", 0);
    let cut_a = overlay.pending().front().unwrap().checkpoint().to_vec();
    overlay.acknowledge_front(a, &cut_a).unwrap();
    let before = overlay.latest().unwrap().checkpoint().unwrap();
    let retry_ctx = context(&overlay, &original_a, 0);
    assert_eq!(
        overlay.append_original(&original_a, &retry_ctx),
        Err(Error::InvalidOriginal)
    );
    let conflicting = packet("A", "different original", 1);
    let ctx = context(&overlay, &conflicting, 1);
    assert_eq!(
        overlay.append_original(&conflicting, &ctx),
        Err(Error::InvalidOriginal)
    );
    let valid = packet("B", "B", 1);
    let mut bad: Value = serde_json::from_slice(&valid).unwrap();
    bad["operations"][0]["payload"]["text"] = json!("changed after fingerprint");
    assert_eq!(
        overlay.append_original(&serde_json::to_vec(&bad).unwrap(), &ctx),
        Err(Error::InvalidOriginal)
    );
    let mut huge = context(&overlay, &valid, 1);
    huge.targets[0].target_id = "x".repeat(METADATA + 1);
    assert_eq!(overlay.append_original(&valid, &huge), Err(Error::Limit));
    assert_eq!(
        overlay.append_original(&vec![b' '; SMALL + 1], &ctx),
        Err(Error::Limit)
    );
    assert_eq!(overlay.latest().unwrap().checkpoint().unwrap(), before);
    assert!(!overlay.is_blocked());
    assert!(overlay.pending().is_empty());
}
#[test]
fn postprepare_checkpoint_limit_retires_without_losing_current_original_or_older_cuts() {
    let mut overlay = source();
    let text = "x".repeat(60000);
    let mut attempted = None;
    for rev in 0..MAX_CUTS {
        // Four distinct paragraphs keep each command <=64KiB and the full
        // immutable atomic packet <=262KiB, without stale same-target reads.
        let mut value: Value =
            serde_json::from_slice(&packet(&format!("large-{rev}"), &text, (rev * 4) as u64))
                .unwrap();
        let first = value["operations"][0].clone();
        value["operations"] = json!((0..4)
            .map(|n| {
                let mut op = first.clone();
                op["op_id"] = json!(format!("large-{rev}-{n}"));
                op["payload"]["target_id"] = json!(format!("stable-{n}"));
                op["read_preconditions"][0]["target_id"] = json!(format!("stable-{n}"));
                op
            })
            .collect::<Vec<_>>());
        let bound = fingerprint_atomic(&scope(), &serde_json::to_vec(&value).unwrap())
            .unwrap()
            .with_fingerprints();
        let bytes = serde_json::to_vec(&bound).unwrap();
        let ctx = context(&overlay, &bytes, (rev * 4) as u64);
        match overlay.append_original(&bytes, &ctx) {
            Ok(_) => {}
            Err(Error::Limit) => {
                attempted = Some(bytes);
                break;
            }
            other => panic!("unexpected {other:?}"),
        }
    }
    let original = attempted.expect("fixture must exceed local checkpoint budget");
    assert!(overlay.is_blocked());
    assert!(overlay.latest().is_some());
    assert!(!overlay.pending().is_empty());
    let draft_native = overlay.latest().unwrap().native().unwrap();
    assert!(
        draft_native["body"]
            .as_array()
            .unwrap()
            .iter()
            .filter_map(|p| p["text"].as_str())
            .map(str::len)
            .sum::<usize>()
            > 500000
    );
    let recovery = overlay.into_recovery();
    assert_eq!(recovery.attempted_original.unwrap(), original);
    assert_eq!(recovery.pending.len(), 2);
    assert!(recovery.latest.unwrap().checkpoint().unwrap().len() > CHECKPOINT);
}
