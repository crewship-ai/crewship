use mooring_collab::dispatch::wordcraft_registration;
use mooring_core::adapter::PrepareAdapter;
use mooring_protocol::{
    canonical::fingerprint_atomic, control, AtomicChange, Counter, Operation, PrepareContext,
    PrepareContextReadPolicy, PreparedOperationRevision, ReadPrecondition, Scope, TargetVersion,
};
use mooring_wordcraft::{adapter::WordCraftAdapter, Document};
use serde_json::{json, Value};

fn fixture() -> (Vec<u8>, Scope, AtomicChange, PrepareContext) {
    let native: Value = serde_json::from_str(include_str!(
        "../../mooring-wordcraft/tests/fixtures/d1-native.json"
    ))
    .unwrap();
    let ids = (0..native["body"].as_array().unwrap().len())
        .map(|index| format!("stable-{index}"))
        .collect();
    let checkpoint = Document::import(native, ids).unwrap().checkpoint().unwrap();
    let scope = Scope {
        application_id: "wordcraft".into(),
        workspace_id: "workspace".into(),
        artifact_id: "artifact".into(),
    };
    let change = AtomicChange {
        change_id:"change".into(), task_id:None, membership_hash:format!("sha256:{}", "0".repeat(64)),
        operations:vec![Operation {
            op_id:"operation".into(), fingerprint:format!("sha256:{}", "0".repeat(64)), participant_ref:"host-bound-agent".into(), base_revision:Counter(10), dependencies:vec![],
            read_preconditions:vec![ReadPrecondition { target_id:"stable-0".into(), revision:Counter(10), structure_revision:Some(Counter(5)) }],
            payload:json!({"kind":"text.insert","target_id":"stable-0","at":{"kind":"end"},"text":" + prepared","properties":{"italic":true}}).as_object().unwrap().clone(),
        }],
    };
    let change = fingerprint_atomic(&scope, &serde_json::to_vec(&change).unwrap())
        .unwrap()
        .with_fingerprints();
    let context = PrepareContext {
        read_policy: PrepareContextReadPolicy::AgentConfirmedCurrent,
        document_revision: Counter(10),
        targets: WordCraftAdapter
            .validate_checkpoint(&checkpoint)
            .unwrap()
            .into_iter()
            .map(|target| TargetVersion {
                target_id: target.target_id,
                deleted: target.deleted,
                revision: Counter(10),
                structure_revision: Counter(5),
            })
            .collect(),
        operation_revisions: vec![PreparedOperationRevision {
            op_id: "operation".into(),
            revision: Counter(11),
        }],
    };
    (checkpoint, scope, change, context)
}
fn request(
    checkpoint: &[u8],
    scope: &Scope,
    change: &AtomicChange,
    context: &PrepareContext,
) -> Value {
    json!({"type":"prepare_atomic","request_id":"request","scope":scope,"selected":mooring_collab::dispatch::wordcraft_versions(),"checkpoint":control::encode_checkpoint(checkpoint).unwrap(),"context":context,"change":change,"control_profile":control::PROFILE})
}

#[test]
fn actual_wordcraft_prepare_is_stateless_owned_and_recoverable_from_host_checkpoint() {
    let (checkpoint, scope, change, context) = fixture();
    let original = Document::restore(&checkpoint).unwrap().native().unwrap();
    let registration = wordcraft_registration();
    let body = request(&checkpoint, &scope, &change, &context);
    let response = registration
        .respond(&serde_json::to_vec(&body).unwrap())
        .unwrap();
    let response: Value = serde_json::from_slice(&response).unwrap();
    assert_eq!(response["type"], "atomic_prepared");
    assert_eq!(response["request_id"], "request");
    assert_eq!(response["scope"], serde_json::to_value(&scope).unwrap());
    assert_eq!(response["membership_hash"], change.membership_hash);
    assert_eq!(response["outcome"]["kind"], "candidate");
    for key in ["status", "issuer", "durability", "revision"] {
        assert!(response.get(key).is_none());
    }
    let encoded = serde_json::from_value(response["outcome"]["checkpoint"].clone()).unwrap();
    let prepared = control::decode_checkpoint(&encoded).unwrap();
    let native = Document::restore(&prepared).unwrap().native().unwrap();
    assert!(serde_json::to_string(&native)
        .unwrap()
        .contains(" + prepared"));
    assert_eq!(
        Document::restore(&checkpoint).unwrap().native().unwrap(),
        original
    );
    // Dropping one service and restoring its externally retained checkpoint is
    // recovery from host-owned state, not a Rust commit or durable acknowledgement.
    drop(registration);
    let registration = wordcraft_registration();
    let validate = json!({"type":"validate_checkpoint","request_id":"recover","scope":scope,"selected":registration.selected,"checkpoint":encoded,"control_profile":control::PROFILE});
    let validated: Value = serde_json::from_slice(
        &registration
            .respond(&serde_json::to_vec(&validate).unwrap())
            .unwrap(),
    )
    .unwrap();
    assert_eq!(validated["type"], "checkpoint_validated");
    assert_eq!(validated["inventory"], response["outcome"]["inventory"]);
}

#[test]
fn forged_binding_identity_checkpoint_and_host_context_never_produce_candidate() {
    let (checkpoint, scope, change, context) = fixture();
    let original = request(&checkpoint, &scope, &change, &context);
    for (path, value) in [
        ("/scope/application_id", json!("another-app")),
        ("/selected/engine_version", json!("0.1.0")),
        (
            "/checkpoint/digest",
            json!(format!("sha256:{}", "0".repeat(64))),
        ),
        (
            "/change/operations/0/payload/text",
            json!("fingerprint tamper"),
        ),
        ("/context/targets", json!([])),
        ("/context/operation_revisions/0/revision", json!("12")),
    ] {
        let mut body = original.clone();
        *body.pointer_mut(path).unwrap() = value;
        assert!(
            wordcraft_registration()
                .respond(&serde_json::to_vec(&body).unwrap())
                .is_err(),
            "{path}"
        );
    }
    let mut body = original;
    body["context"]["targets"][0]["revision"] = json!("9");
    let response: Value = serde_json::from_slice(
        &wordcraft_registration()
            .respond(&serde_json::to_vec(&body).unwrap())
            .unwrap(),
    )
    .unwrap();
    assert_eq!(response["outcome"]["kind"], "failure");
    assert_eq!(response["outcome"]["op_id"], "operation");
    assert_eq!(response["outcome"]["reason"]["code"], "precondition_failed");
    assert_eq!(
        response["outcome"]["reason"]["retry"],
        "new_operation_after_refresh"
    );
}

#[cfg(unix)]
#[test]
fn real_private_socket_negotiates_profile_then_prepares_actual_wordcraft() {
    use std::{
        io::{Read, Write},
        os::unix::net::UnixStream,
        thread,
        time::Duration,
    };
    let (mut host, child) = UnixStream::pair().unwrap();
    host.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
    host.set_write_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    let service = thread::spawn(move || {
        mooring_collab::private_rpc::serve_registered(
            child,
            Duration::from_secs(2),
            wordcraft_registration(),
        )
    });
    let hello = json!({"type":"hello","request_id":"host-start","supported":[mooring_collab::dispatch::wordcraft_versions()],"control_profiles":[control::PROFILE]});
    mooring_collab::write_frame(&mut host, &serde_json::to_vec(&hello).unwrap()).unwrap();
    let ready: Value =
        serde_json::from_slice(&mooring_collab::read_frame(&mut host).unwrap().unwrap()).unwrap();
    assert_eq!(ready["selected_control_profile"], control::PROFILE);
    let (checkpoint, scope, change, context) = fixture();
    let mut body = serde_json::to_vec(&request(&checkpoint, &scope, &change, &context)).unwrap();
    body.resize(300_000, b' '); // Only selected private control may exceed public cap.
    host.write_all(&(body.len() as u32).to_be_bytes()).unwrap();
    host.write_all(&body).unwrap();
    let mut header = [0; 4];
    host.read_exact(&mut header).unwrap();
    let length = u32::from_be_bytes(header) as usize;
    assert!(length <= control::MAX_CONTROL_BYTES);
    let mut response = vec![0; length];
    host.read_exact(&mut response).unwrap();
    let response = control::validate(&response, control::Peer::InheritedRuntime).unwrap();
    assert_eq!(response.value()["outcome"]["kind"], "candidate");
    drop(host);
    assert!(service.join().unwrap().is_ok());
}

#[cfg(unix)]
#[test]
fn causal_offer_cannot_enable_or_relabel_strict_runtime_capability() {
    use std::{os::unix::net::UnixStream, thread, time::Duration};
    for profiles in [
        json!([control::CAUSAL_PROFILE]),
        json!([control::CAUSAL_PROFILE, control::PROFILE]),
    ] {
        let (mut host, child) = UnixStream::pair().unwrap();
        host.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
        host.set_write_timeout(Some(Duration::from_secs(3)))
            .unwrap();
        let service = thread::spawn(move || {
            mooring_collab::private_rpc::serve_registered(
                child,
                Duration::from_secs(2),
                wordcraft_registration(),
            )
        });
        let hello = json!({"type":"hello","request_id":"causal-offer","supported":[mooring_collab::dispatch::wordcraft_versions()],"control_profiles":profiles});
        mooring_collab::write_frame(&mut host, &serde_json::to_vec(&hello).unwrap()).unwrap();
        let ready: Value =
            serde_json::from_slice(&mooring_collab::read_frame(&mut host).unwrap().unwrap())
                .unwrap();
        if profiles.as_array().unwrap().len() == 1 {
            assert!(ready.get("selected_control_profile").is_none());
        } else {
            assert_eq!(ready["selected_control_profile"], control::PROFILE);
        }
        // A schema declaration never enables an unimplemented application path.
        mooring_collab::write_frame(&mut host, br#"{"type":"prepare_atomic_causal"}"#).unwrap();
        assert!(service.join().unwrap().is_err());
    }
}

#[test]
fn valid_checkpoint_component_that_cannot_fit_response_never_escapes_as_candidate() {
    use mooring_core::adapter::{Prepared, PreparedCandidate};
    use mooring_protocol::{PreparedOperationEffects, TargetIdentity};
    struct LargeAdapter(Vec<u8>);
    struct LargeCandidate {
        checkpoint: Vec<u8>,
        effects: Vec<PreparedOperationEffects>,
        inventory: Vec<TargetIdentity>,
    }
    impl PreparedCandidate for LargeCandidate {
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
    impl PrepareAdapter for LargeAdapter {
        type Candidate = LargeCandidate;
        type Error = ();
        fn validate_checkpoint(&self, bytes: &[u8]) -> Result<Vec<TargetIdentity>, ()> {
            WordCraftAdapter.validate_checkpoint(bytes).map_err(|_| ())
        }
        fn prepare_atomic(
            &self,
            bytes: &[u8],
            change: &AtomicChange,
            _: &PrepareContext,
        ) -> Result<Prepared<LargeCandidate>, ()> {
            Ok(Prepared::Candidate(LargeCandidate {
                checkpoint: self.0.clone(),
                effects: change
                    .operations
                    .iter()
                    .map(|op| PreparedOperationEffects {
                        op_id: op.op_id.clone(),
                        targets: vec![],
                    })
                    .collect(),
                inventory: self.validate_checkpoint(bytes)?,
            }))
        }
    }
    let (checkpoint, scope, change, context) = fixture();
    let mut maximum = checkpoint.clone();
    // Legal trailing JSON whitespace retains the same real app checkpoint;
    // the opaque component fits while base64 plus envelope metadata does not.
    maximum.resize(control::MAX_CHECKPOINT_BYTES, b' ');
    WordCraftAdapter.validate_checkpoint(&maximum).unwrap();
    let registration = mooring_collab::dispatch::Registration {
        application_id: "wordcraft".into(),
        selected: mooring_collab::dispatch::wordcraft_versions(),
        adapter: LargeAdapter(maximum),
    };
    assert!(registration
        .respond(&serde_json::to_vec(&request(&checkpoint, &scope, &change, &context)).unwrap())
        .is_err());
}
