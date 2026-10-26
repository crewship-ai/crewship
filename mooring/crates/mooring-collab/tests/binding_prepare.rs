//! Experimental preparation only; these are not durable host/session tests.
use mooring_collab::{
    dispatch::Registration,
    experimental_binding::{BindingAdapter, WriteScope},
};
use mooring_core::adapter::PrepareAdapter;
use mooring_protocol::{
    canonical::fingerprint_atomic, control, AtomicChange, Counter, Operation, PrepareContext,
    PrepareContextReadPolicy, PreparedOperationRevision, ReadPrecondition, Scope, TargetVersion,
    Versions,
};
use mooring_wordcraft::experimental_binding::Binding;
use serde_json::{json, Value};

fn adapter() -> BindingAdapter {
    BindingAdapter::new(202, WriteScope::WholeDocument).unwrap()
}
fn versions() -> Versions {
    Versions {
        protocol_version: "0.4.0".into(),
        model_version: "0.3.0".into(),
        engine_version: "0.10.0".into(),
    }
}
fn registration() -> Registration<BindingAdapter> {
    Registration {
        application_id: "wordcraft".into(),
        selected: versions(),
        adapter: adapter(),
    }
}
fn native() -> Value {
    let mut native: Value = serde_json::from_str(include_str!(
        "../../mooring-wordcraft/tests/fixtures/d1-engine-canonical.json"
    ))
    .unwrap();
    native["body"] = json!([{"kind":"para","text":"Ahoj","runs":[{"len":4}]},{"kind":"para","text":"světe","runs":[{"len":6}]}]);
    native
}
fn fixture(split: bool) -> (Vec<u8>, Value) {
    let initial = native();
    let ids = vec!["left".into(), "peer".into()];
    let cp = Binding::import(initial.clone(), ids.clone(), 101)
        .unwrap()
        .checkpoint()
        .unwrap();
    let mut client = Binding::restore(&cp, 202).unwrap();
    let mut after = initial;
    let after_ids = if split {
        after["body"] = json!([{"kind":"para","text":"Ah","runs":[{"len":2}]},{"kind":"para","text":"oj","runs":[{"len":2}]},{"kind":"para","text":"světe","runs":[{"len":6}]}]);
        vec!["left".into(), "new".into(), "peer".into()]
    } else {
        after["body"][0] = json!({"kind":"para","text":"Ahoj!","runs":[{"len":5}]});
        ids
    };
    let update = client.reconcile(&after, after_ids).unwrap();
    let scope = Scope {
        application_id: "wordcraft".into(),
        workspace_id: "workspace".into(),
        artifact_id: "artifact".into(),
    };
    let targets = adapter().validate_checkpoint(&cp).unwrap();
    let change=AtomicChange { change_id:"change".into(),task_id:None,membership_hash:format!("sha256:{}","0".repeat(64)),operations:vec![Operation {
        op_id:"operation".into(),fingerprint:format!("sha256:{}","0".repeat(64)),participant_ref:"host-bound-agent".into(),base_revision:Counter(0),dependencies:vec![],
        read_preconditions:targets.iter().filter(|t|!t.deleted).map(|t|ReadPrecondition{target_id:t.target_id.clone(),revision:Counter(0),structure_revision:Some(Counter(0))}).collect(),
        payload:json!({"kind":"wordcraft.binding.update","update":mooring_protocol::boundary::encode_base64(&update.update,262144).unwrap(),"basis_state":update.basis_state,"basis_structure":update.basis_structure,"basis_ids":update.basis_ids}).as_object().unwrap().clone()
    }]};
    let change = fingerprint_atomic(&scope, &serde_json::to_vec(&change).unwrap())
        .unwrap()
        .with_fingerprints();
    let context = PrepareContext {
        read_policy: PrepareContextReadPolicy::AgentConfirmedCurrent,
        document_revision: Counter(0),
        targets: targets
            .into_iter()
            .map(|t| TargetVersion {
                target_id: t.target_id,
                deleted: t.deleted,
                revision: Counter(0),
                structure_revision: Counter(0),
            })
            .collect(),
        operation_revisions: vec![PreparedOperationRevision {
            op_id: "operation".into(),
            revision: Counter(1),
        }],
    };
    let body = json!({"type":"prepare_atomic","request_id":"prepare","scope":scope,"selected":versions(),"checkpoint":control::encode_checkpoint(&cp).unwrap(),"context":context,"change":change,"control_profile":control::PROFILE});
    (cp, body)
}
fn respond(body: &Value) -> Value {
    respond_as(body, 202)
}
fn respond_as(body: &Value, origin: u64) -> Value {
    let mut registration = registration();
    registration.adapter = BindingAdapter::new(origin, WriteScope::WholeDocument).unwrap();
    serde_json::from_slice(
        &registration
            .respond(&serde_json::to_vec(body).unwrap())
            .unwrap(),
    )
    .unwrap()
}
fn resign(body: &mut Value) {
    let scope = serde_json::from_value(body["scope"].clone()).unwrap();
    body["change"] = serde_json::to_value(
        fingerprint_atomic(&scope, &serde_json::to_vec(&body["change"]).unwrap())
            .unwrap()
            .with_fingerprints(),
    )
    .unwrap();
}
fn assert_direct_failure(response: Value, code: &str) {
    // Existing strict preparation wire grammar uses kind=failure, not the
    // public committed change status=rejected. A prepared failure is not Saved.
    assert_eq!(response["outcome"]["kind"], "failure");
    assert_eq!(response["outcome"]["op_id"], "operation");
    assert_eq!(response["outcome"]["reason"]["code"], code);
    assert!(response["outcome"].get("checkpoint").is_none());
}

#[test]
fn raw_update_prepares_isolated_candidate_with_conservative_whole_document_effects() {
    let (cp, body) = fixture(false);
    let response = respond(&body);
    assert_eq!(response["outcome"]["kind"], "candidate");
    let effects = response["outcome"]["effects"][0]["targets"]
        .as_array()
        .unwrap();
    assert!(
        effects
            .iter()
            .any(|e| e["target_id"] == "peer" && e["content_changed"] == true),
        "unchanged peer must be fenced conservatively"
    );
    assert!(effects
        .iter()
        .any(|e| e["target_id"] == "wordcraft:document-structure"));
    let checkpoint = control::decode_checkpoint(
        &serde_json::from_value(response["outcome"]["checkpoint"].clone()).unwrap(),
    )
    .unwrap();
    assert_eq!(
        Binding::restore(&checkpoint, 404)
            .unwrap()
            .native()
            .unwrap()["body"][0]["text"],
        "Ahoj!"
    );
    assert_eq!(
        Binding::restore(&cp, 404).unwrap().native().unwrap(),
        native()
    );
    assert!(mooring_wordcraft::Document::restore(&checkpoint).is_err());
    assert!(mooring_collab::dispatch::wordcraft_registration()
        .respond(&serde_json::to_vec(&body).unwrap())
        .is_err());
    assert!(response.get("durability").is_none());
}

#[test]
fn split_and_join_keep_precise_identity_tombstones_with_conservative_content_effects() {
    let (_, body) = fixture(true);
    let response = respond(&body);
    let effects = response["outcome"]["effects"][0]["targets"]
        .as_array()
        .unwrap();
    assert!(effects.iter().any(|e| e["target_id"] == "new"
        && e["created"] == true
        && e["structure_changed"] == true));
    let checkpoint = control::decode_checkpoint(
        &serde_json::from_value(response["outcome"]["checkpoint"].clone()).unwrap(),
    )
    .unwrap();
    let mut client = Binding::restore(&checkpoint, 505).unwrap();
    let update = client
        .reconcile(&native(), vec!["left".into(), "peer".into()])
        .unwrap();
    let mut next = body.clone();
    next["checkpoint"] =
        serde_json::to_value(control::encode_checkpoint(&checkpoint).unwrap()).unwrap();
    let inventory = adapter().validate_checkpoint(&checkpoint).unwrap();
    next["context"]["targets"] = json!(inventory
        .iter()
        .map(|t| TargetVersion {
            target_id: t.target_id.clone(),
            deleted: t.deleted,
            revision: Counter(0),
            structure_revision: Counter(0)
        })
        .collect::<Vec<_>>());
    next["change"]["operations"][0]["read_preconditions"] = json!(inventory
        .iter()
        .filter(|t| !t.deleted)
        .map(|t| ReadPrecondition {
            target_id: t.target_id.clone(),
            revision: Counter(0),
            structure_revision: Some(Counter(0))
        })
        .collect::<Vec<_>>());
    next["change"]["operations"][0]["payload"] = json!({"kind":"wordcraft.binding.update","update":mooring_protocol::boundary::encode_base64(&update.update,262144).unwrap(),"basis_state":update.basis_state,"basis_structure":update.basis_structure,"basis_ids":update.basis_ids});
    resign(&mut next);
    let joined = respond_as(&next, 505);
    assert!(joined["outcome"]["inventory"]
        .as_array()
        .unwrap()
        .iter()
        .any(|i| i["target_id"] == "new" && i["deleted"] == true));
    assert!(joined["outcome"]["effects"][0]["targets"]
        .as_array()
        .unwrap()
        .iter()
        .any(|i| i["target_id"] == "new" && i["deleted"] == true));
}

#[test]
fn partial_scope_and_missing_or_stale_conservative_reads_never_prepare() {
    assert!(BindingAdapter::new(202, WriteScope::Blocks(vec!["left".into()])).is_err());
    let (_, body) = fixture(false);
    assert_direct_failure(respond_as(&body, 999), "invalid_request");
    let (_, mut missing) = fixture(false);
    missing["change"]["operations"][0]["read_preconditions"]
        .as_array_mut()
        .unwrap()
        .retain(|r| r["target_id"] != "peer");
    resign(&mut missing);
    assert_direct_failure(respond(&missing), "invalid_request");
    let (_, mut stale) = fixture(false);
    stale["change"]["operations"][0]["read_preconditions"][0]["revision"] = json!("1");
    resign(&mut stale);
    assert_direct_failure(respond(&stale), "precondition_failed");
    let (_, mut malformed) = fixture(false);
    malformed["change"]["operations"][0]["payload"]["update"] = json!("AA==");
    resign(&mut malformed);
    assert_direct_failure(respond(&malformed), "invalid_request");
    let (_, mut broken_host_context) = fixture(false);
    broken_host_context["context"]["targets"] = json!([]);
    assert!(
        registration()
            .respond(&serde_json::to_vec(&broken_host_context).unwrap())
            .is_err(),
        "incomplete HOST inventory is nonterminal protocol failure, not an author rejection"
    );
}
