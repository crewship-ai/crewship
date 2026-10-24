use mooring_collab::dispatch::Registration;
use mooring_core::{
    adapter::{CausalPrepareAdapter, PrepareAdapter, Prepared, PreparedCandidate, PreparedFailure},
    causal::{CausalContext, CausalPrepareContext},
};
use mooring_protocol::{
    canonical::fingerprint_atomic, control, AtomicChange, ErrorCode, PrepareContext,
    PreparedOperationEffects, Scope, TargetIdentity, Versions,
};
use serde_json::{json, Value};
use std::sync::{
    atomic::{AtomicUsize, Ordering},
    Arc,
};

// This deliberately synthetic adapter can bypass its own read checks so the
// dispatcher must independently validate candidate effects. It is not WordCraft
// or a substitute for actual SQL history qualification or authenticated admission.
struct FixtureAdapter {
    calls: Arc<AtomicUsize>,
    behavior: &'static str,
}
struct FixtureCandidate {
    bytes: Vec<u8>,
    effects: Vec<PreparedOperationEffects>,
    inventory: Vec<TargetIdentity>,
}
impl PreparedCandidate for FixtureCandidate {
    fn checkpoint_bytes(&self) -> &[u8] {
        &self.bytes
    }
    fn effects(&self) -> &[PreparedOperationEffects] {
        &self.effects
    }
    fn inventory(&self) -> &[TargetIdentity] {
        &self.inventory
    }
}
fn inventory() -> Vec<TargetIdentity> {
    vec![TargetIdentity {
        target_id: "paragraph".into(),
        deleted: false,
    }]
}
impl PrepareAdapter for FixtureAdapter {
    type Candidate = FixtureCandidate;
    type Error = ();
    fn validate_checkpoint(&self, bytes: &[u8]) -> Result<Vec<TargetIdentity>, ()> {
        if bytes == b"state" || bytes == b"next" {
            Ok(inventory())
        } else {
            Err(())
        }
    }
    fn prepare_atomic(
        &self,
        _: &[u8],
        _: &AtomicChange,
        _: &PrepareContext,
    ) -> Result<Prepared<FixtureCandidate>, ()> {
        Err(())
    }
}
impl CausalPrepareAdapter for FixtureAdapter {
    fn prepare_atomic_causal(
        &self,
        _: &[u8],
        change: &AtomicChange,
        context: &CausalPrepareContext,
    ) -> Result<Prepared<FixtureCandidate>, ()> {
        self.calls.fetch_add(1, Ordering::SeqCst);
        if self.behavior == "error" {
            return Err(());
        }
        if matches!(self.behavior, "foreign" | "uncertain" | "rejection") {
            return Ok(Prepared::Rejected(PreparedFailure {
                operation_id: if self.behavior == "foreign" {
                    "foreign".into()
                } else {
                    change.operations[1].op_id.clone()
                },
                code: if self.behavior == "uncertain" {
                    ErrorCode::UnknownOutcome
                } else {
                    ErrorCode::InvalidRequest
                },
                message: "actual fixture rejection".into(),
            }));
        }
        let qualified = CausalContext::qualify(context, change, &inventory()).map_err(|_| ())?;
        let mut working = qualified.working();
        let effects: Vec<PreparedOperationEffects> = change.operations.iter().map(|op| {
            serde_json::from_value(json!({"op_id":op.op_id,"targets":[{"target_id":"paragraph","content_changed":true,"structure_changed":false,"created":false,"deleted":false}]})).unwrap()
        }).collect();
        if self.behavior != "bypass" {
            for effect in &effects {
                working.check_current_reads(&effect.op_id).map_err(|_| ())?;
                working
                    .advance_effects(&effect.op_id, &effect.targets)
                    .map_err(|_| ())?;
            }
        }
        Ok(Prepared::Candidate(FixtureCandidate {
            bytes: if self.behavior == "bad-state" {
                b"bad".to_vec()
            } else {
                b"next".to_vec()
            },
            effects,
            inventory: if self.behavior == "bad-inventory" {
                vec![]
            } else {
                inventory()
            },
        }))
    }
}
fn fixture(behavior: &'static str) -> (Registration<FixtureAdapter>, Value, Arc<AtomicUsize>) {
    let calls = Arc::new(AtomicUsize::new(0));
    let versions = Versions {
        protocol_version: "0.4.0".into(),
        model_version: "1.0.0".into(),
        engine_version: "0.1.0".into(),
    };
    let scope = Scope {
        application_id: "fixture".into(),
        workspace_id: "workspace".into(),
        artifact_id: "artifact".into(),
    };
    let change: AtomicChange=serde_json::from_value(json!({"change_id":"change","membership_hash":format!("sha256:{}","0".repeat(64)),"operations":[
        {"op_id":"a","fingerprint":format!("sha256:{}","0".repeat(64)),"participant_ref":"host-participant","base_revision":"0","dependencies":["saved-origin"],"read_preconditions":[{"target_id":"paragraph","revision":"0"}],"payload":{"kind":"fixture"}},
        {"op_id":"b","fingerprint":format!("sha256:{}","0".repeat(64)),"participant_ref":"host-participant","base_revision":"0","dependencies":["a"],"read_preconditions":[{"target_id":"paragraph","revision":"0"}],"payload":{"kind":"fixture"}}
    ]})).unwrap();
    let change = fingerprint_atomic(&scope, &serde_json::to_vec(&change).unwrap())
        .unwrap()
        .with_fingerprints();
    let request = json!({"type":"prepare_atomic_causal","request_id":"request","scope":scope,"selected":versions,"checkpoint":control::encode_checkpoint(b"state").unwrap(),"change":change,"control_profile":control::CAUSAL_PROFILE,
        "context":{"read_policy":"agent_dependency_covered","document_revision":"1","targets":[{"target_id":"paragraph","revision":"1","structure_revision":"0","deleted":false}],"operation_revisions":[{"op_id":"a","revision":"2"},{"op_id":"b","revision":"3"}],
        "qualified_reads":[{"op_id":"a","read_index":"0","target_id":"paragraph","observed_revision":"0","entry":{"revision":"1","structure_revision":"0","deleted":false},"covered":true},{"op_id":"b","read_index":"0","target_id":"paragraph","observed_revision":"0","entry":{"revision":"1","structure_revision":"0","deleted":false},"covered":true}]}});
    (
        Registration {
            application_id: "fixture".into(),
            selected: versions,
            adapter: FixtureAdapter {
                calls: calls.clone(),
                behavior,
            },
        },
        request,
        calls,
    )
}
fn bytes(v: &Value) -> Vec<u8> {
    serde_json::to_vec(v).unwrap()
}
fn refingerprint(request: &mut Value) {
    let scope: Scope = serde_json::from_value(request["scope"].clone()).unwrap();
    request["change"] = serde_json::to_value(
        fingerprint_atomic(&scope, &bytes(&request["change"]))
            .unwrap()
            .with_fingerprints(),
    )
    .unwrap();
}

#[test]
fn original_causal_reads_prepare_owned_candidate_without_saved_or_installation() {
    let (reg, request, calls) = fixture("valid");
    for _ in 0..2 {
        let response = reg.respond_causal(&bytes(&request)).unwrap();
        let response =
            control::validate_causal(&response, control::Peer::InheritedRuntime).unwrap();
        assert_eq!(response.value()["outcome"]["kind"], "candidate");
        assert_eq!(
            response.value()["input_checkpoint_digest"],
            request["checkpoint"]["digest"]
        );
        assert_eq!(
            response.value()["membership_hash"],
            request["change"]["membership_hash"]
        );
        for field in ["issuer", "durability", "revision", "status"] {
            assert!(response.value().get(field).is_none());
        }
    }
    assert_eq!(calls.load(Ordering::SeqCst), 2);
    assert_eq!(
        control::decode_checkpoint(&serde_json::from_value(request["checkpoint"].clone()).unwrap())
            .unwrap(),
        b"state"
    );
    assert_eq!(
        request["change"]["operations"][1]["read_preconditions"][0]["revision"],
        "0"
    );
}

#[test]
fn forged_bindings_or_full_attestations_never_reach_application_preparation() {
    let (reg, request, calls) = fixture("valid");
    for index in 0..8 {
        let mut bad = request.clone();
        match index {
            0 => bad["scope"]["application_id"] = json!("foreign"),
            1 => bad["selected"]["engine_version"] = json!("9.0.0"),
            2 => bad["checkpoint"]["state"] = json!("YmFk"),
            3 => bad["change"]["operations"][0]["participant_ref"] = json!("forged"),
            4 => bad["context"]["qualified_reads"][1]["op_id"] = json!("foreign"),
            5 => bad["context"]["qualified_reads"][1]["entry"]["revision"] = json!("0"),
            6 => bad["context"]["qualified_reads"][1]["observed_structure_revision"] = Value::Null,
            _ => bad["context"]["qualified_reads"][1]["read_index"] = json!("0\n"),
        }
        assert!(reg.respond_causal(&bytes(&bad)).is_err(), "case{index}");
    }
    assert_eq!(calls.load(Ordering::SeqCst), 0);
}

#[test]
fn candidate_cannot_bypass_same_atomic_dependency_or_opaque_inventory_checks() {
    let (reg, mut request, _) = fixture("bypass");
    request["change"]["operations"][1]["dependencies"] = json!(["saved-origin"]);
    refingerprint(&mut request);
    assert!(reg.respond_causal(&bytes(&request)).is_err());
    for behavior in [
        "bad-state",
        "bad-inventory",
        "foreign",
        "uncertain",
        "error",
    ] {
        let (reg, request, _) = fixture(behavior);
        assert!(reg.respond_causal(&bytes(&request)).is_err(), "{behavior}");
    }
    let (reg, request, _) = fixture("rejection");
    let response: Value =
        serde_json::from_slice(&reg.respond_causal(&bytes(&request)).unwrap()).unwrap();
    assert_eq!(response["outcome"]["op_id"], "b");
    assert_eq!(response["outcome"]["reason"]["code"], "invalid_request");
}

#[cfg(unix)]
#[test]
fn real_inherited_stream_selects_exact_profile_and_never_switches_by_message_type() {
    use std::{os::unix::net::UnixStream, thread, time::Duration};
    for causal in [true, false] {
        let (reg, request, _) = fixture("valid");
        let (mut host, child) = UnixStream::pair().unwrap();
        host.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
        host.set_write_timeout(Some(Duration::from_secs(3)))
            .unwrap();
        let selected = reg.selected.clone();
        let service = thread::spawn(move || {
            mooring_collab::private_rpc::serve_causal_registered(child, Duration::from_secs(2), reg)
        });
        let profile = if causal {
            control::CAUSAL_PROFILE
        } else {
            control::PROFILE
        };
        let hello = json!({"type":"hello","request_id":"host","supported":[selected],"control_profiles":[profile]});
        mooring_collab::write_frame(&mut host, &bytes(&hello)).unwrap();
        let ready: Value =
            serde_json::from_slice(&mooring_collab::read_frame(&mut host).unwrap().unwrap())
                .unwrap();
        assert_eq!(ready["selected_control_profile"], profile);
        if causal {
            let mut raw = bytes(&request);
            raw.resize(300_000, b' ');
            use std::io::Write;
            host.write_all(&(raw.len() as u32).to_be_bytes()).unwrap();
            host.write_all(&raw).unwrap();
            let mut header = [0; 4];
            use std::io::Read;
            host.read_exact(&mut header).unwrap();
            let mut response = vec![0; u32::from_be_bytes(header) as usize];
            host.read_exact(&mut response).unwrap();
            assert!(control::validate_causal(&response, control::Peer::InheritedRuntime).is_ok());
            let mut wrong = request.clone();
            wrong["type"] = json!("prepare_atomic");
            wrong["control_profile"] = json!(control::PROFILE);
            mooring_collab::write_frame(&mut host, &bytes(&wrong)).unwrap();
        } else {
            mooring_collab::write_frame(&mut host, &bytes(&request)).unwrap();
        }
        assert!(service.join().unwrap().is_err());
    }
}
