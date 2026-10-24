#![cfg(unix)]
use mooring_collab::session_driver::Driver;
use mooring_core::{adapter::PrepareAdapter, batch::Tick};
use mooring_protocol::{
    boundary::{Durability, Transport},
    control, Counter, TargetVersion,
};
use mooring_wordcraft::{adapter::WordCraftAdapter, Document};
use serde_json::{json, Value};
use std::{os::unix::net::UnixStream, time::Duration};
fn at(ms: u64) -> Tick {
    Tick(Duration::from_millis(ms))
}
fn host() -> Transport<'static> {
    Transport::AuthenticatedHost {
        issuer: "crewship",
        durabilities: &[Durability::Node],
        required: Durability::Node,
    }
}
fn open() -> Vec<u8> {
    serde_json::to_vec(&json!({"type":"session_open","required_capabilities":["checkpoint_identity_v1"],"request_id":"host-open","control_profile":"mooring.session-causal.v1","scope":{"workspace_id":"workspace","artifact_id":"artifact","application_id":"wordcraft"},"session_id":"host-session","runtime_ref":"fresh-authenticated-child","versions":mooring_collab::dispatch::wordcraft_versions()})).unwrap()
}
fn grant(acquire: &[u8]) -> Vec<u8> {
    let acquire: Value = serde_json::from_slice(acquire).unwrap();
    serde_json::to_vec(&json!({"type":"owner_grant","request_id":acquire["request_id"],"scope":acquire["scope"],"session_id":"host-session","runtime_ref":acquire["runtime_ref"],"fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"lease_duration_ms":"60000","revision":"10","renewal_nonce":acquire["renewal_nonce"],"permission_version":"3"})).unwrap()
}
fn restore() -> (Vec<u8>, Value, Vec<String>) {
    let native: Value = serde_json::from_str(include_str!(
        "../../mooring-wordcraft/tests/fixtures/d1-engine-canonical.json"
    ))
    .unwrap();
    let ids: Vec<_> = (0..native["body"].as_array().unwrap().len())
        .map(|i| format!("stable-{i}"))
        .collect();
    let cp = Document::import(native.clone(), ids.clone())
        .unwrap()
        .checkpoint()
        .unwrap();
    let opaque = control::encode_checkpoint(&cp).unwrap();
    let targets: Vec<_> = WordCraftAdapter
        .validate_checkpoint(&cp)
        .unwrap()
        .into_iter()
        .map(|x| TargetVersion {
            target_id: x.target_id,
            deleted: x.deleted,
            revision: Counter(10),
            structure_revision: Counter(0),
        })
        .collect();
    let mut request = json!({"type":"session_restore","request_id":"host-restore","control_profile":"mooring.session-causal.v1","scope":{"workspace_id":"workspace","artifact_id":"artifact","application_id":"wordcraft"},"session_id":"host-session","runtime_ref":"fresh-authenticated-child","fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"versions":mooring_collab::dispatch::wordcraft_versions(),"checkpoint":{"scope":{"workspace_id":"workspace","artifact_id":"artifact","application_id":"wordcraft"},"revision":"10","generation":"7","format_version":mooring_wordcraft::profile::CHECKPOINT_FORMAT,"versions":mooring_collab::dispatch::wordcraft_versions(),"encoding":"base64","state":opaque.state,"digest":opaque.digest,"assets":[]},"targets":targets});
    bind_checkpoint_identity(&mut request["checkpoint"]);
    (serde_json::to_vec(&request).unwrap(), native, ids)
}

#[test]
fn owned_driver_prepares_on_same_fd_while_restore_is_withheld() {
    let (child, mut peer) = UnixStream::pair().unwrap();
    peer.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let (restore_bytes, native, ids) = restore();
    let expected = native.clone();
    let host_worker = std::thread::spawn(move || {
        mooring_collab::write_frame(&mut peer, &open()).unwrap();
        let acquire = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        let owner: Value = serde_json::from_slice(&acquire).unwrap();
        assert_eq!(owner["type"], "owner_acquire");
        let restore: Value = serde_json::from_slice(&restore_bytes).unwrap();
        let full: mooring_protocol::Checkpoint =
            serde_json::from_value(restore["checkpoint"].clone()).unwrap();
        let identity = mooring_protocol::checkpoint::verify(
            &full,
            mooring_protocol::checkpoint::Limits {
                decoded_bytes: control::MAX_CHECKPOINT_BYTES,
                preimage_bytes: control::MAX_CONTROL_BYTES,
            },
        )
        .unwrap();
        let describe = json!({"type":"session_describe_checkpoint","request_id":"describe-before-owner","control_profile":"mooring.session-causal.v1","scope":restore["scope"],"session_id":restore["session_id"],"runtime_ref":restore["runtime_ref"],"versions":restore["versions"],"checkpoint":{"encoding":"base64","state":full.state,"digest":identity.content_digest()}});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&describe).unwrap()).unwrap();
        let described: Value =
            serde_json::from_slice(&mooring_collab::read_frame(&mut peer).unwrap().unwrap())
                .unwrap();
        assert_eq!(described["type"], "session_checkpoint_described");
        assert_eq!(described["request_id"], "describe-before-owner");
        assert_eq!(described["input_digest"], identity.content_digest());
        assert_eq!(described["description"]["assets"], json!([]));
        assert_eq!(described["description"]["versions"], restore["versions"]);
        mooring_collab::write_frame(&mut peer, &grant(&acquire)).unwrap();
        let request = json!({"type":"validate_checkpoint_causal","request_id":"prepare-before-restore","scope":restore["scope"],"selected":restore["versions"],"checkpoint":{"encoding":"base64","state":restore["checkpoint"]["state"],"digest":mooring_protocol::checkpoint::verify(&serde_json::from_value(restore["checkpoint"].clone()).unwrap(), mooring_protocol::checkpoint::Limits { decoded_bytes: control::MAX_CHECKPOINT_BYTES, preimage_bytes: control::MAX_CONTROL_BYTES }).unwrap().content_digest()},"control_profile":"mooring.prepare-causal.v1"});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&request).unwrap()).unwrap();
        let response: Value =
            serde_json::from_slice(&mooring_collab::read_frame(&mut peer).unwrap().unwrap())
                .unwrap();
        assert_eq!(response["type"], "checkpoint_validated_causal");
        assert_eq!(response["request_id"], "prepare-before-restore");
        // Only now may startup restoration advance. No worker candidate was installed.
        mooring_collab::write_frame(&mut peer, &restore_bytes).unwrap();
        for purpose in ["session_restored", "session_opened"] {
            let ack: Value =
                serde_json::from_slice(&mooring_collab::read_frame(&mut peer).unwrap().unwrap())
                    .unwrap();
            assert_eq!(ack["type"], purpose);
        }
        peer
    });
    let mut driver =
        Driver::from_negotiated_inherited_stream(child, &host(), || Ok(at(1))).unwrap();
    assert_eq!(driver.confirmed().document().native().unwrap(), expected);
    assert_eq!(driver.confirmed().document().block_ids().unwrap(), ids);
    assert_eq!(driver.confirmed().checkpoint().revision, Counter(10));
    let _peer = host_worker.join().unwrap();
    driver.check_owner().unwrap();
    drop(driver); // joins preparation after shutdown; no leaked blocked worker.
}

#[test]
fn owned_driver_denies_participant_provenance_before_startup() {
    let (child, _peer) = UnixStream::pair().unwrap();
    assert!(
        Driver::from_negotiated_inherited_stream(child, &Transport::Participant, || Ok(at(0)))
            .is_err()
    );
}

#[test]
fn prepare_cannot_escape_owned_scope_during_startup() {
    let (child, mut peer) = UnixStream::pair().unwrap();
    peer.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let (restore_bytes, _, _) = restore();
    let host_worker = std::thread::spawn(move || {
        mooring_collab::write_frame(&mut peer, &open()).unwrap();
        let acquire = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        mooring_collab::write_frame(&mut peer, &grant(&acquire)).unwrap();
        let restore: Value = serde_json::from_slice(&restore_bytes).unwrap();
        let request = json!({"type":"validate_checkpoint_causal","request_id":"wrong-artifact","scope":{"workspace_id":"workspace","artifact_id":"another-artifact","application_id":"wordcraft"},"selected":restore["versions"],"checkpoint":{"encoding":"base64","state":restore["checkpoint"]["state"],"digest":mooring_protocol::checkpoint::verify(&serde_json::from_value(restore["checkpoint"].clone()).unwrap(), mooring_protocol::checkpoint::Limits { decoded_bytes: control::MAX_CHECKPOINT_BYTES, preimage_bytes: control::MAX_CONTROL_BYTES }).unwrap().content_digest()},"control_profile":"mooring.prepare-causal.v1"});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&request).unwrap()).unwrap();
        // No inventory response or ownership acknowledgement may escape.
        assert!(matches!(
            mooring_collab::read_frame(&mut peer),
            Ok(None) | Err(_)
        ));
    });
    assert!(Driver::from_negotiated_inherited_stream(child, &host(), || Ok(at(1))).is_err());
    host_worker.join().unwrap();
}
#[test]
fn owner_expiry_while_waiting_restore_closes_and_joins_worker() {
    use std::sync::{
        atomic::{AtomicU64, Ordering},
        Arc,
    };
    let (child, mut peer) = UnixStream::pair().unwrap();
    peer.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let now = Arc::new(AtomicU64::new(1));
    let next = now.clone();
    let host_worker = std::thread::spawn(move || {
        mooring_collab::write_frame(&mut peer, &open()).unwrap();
        let acquire = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        mooring_collab::write_frame(&mut peer, &grant(&acquire)).unwrap();
        next.store(60002, Ordering::SeqCst);
        assert!(matches!(
            mooring_collab::read_frame(&mut peer),
            Ok(None) | Err(_)
        ));
    });
    assert!(
        Driver::from_negotiated_inherited_stream(child, &host(), move || Ok(at(
            now.load(Ordering::SeqCst)
        )))
        .is_err()
    );
    host_worker.join().unwrap();
}

#[test]
fn clock_error_permanently_closes_even_if_clock_later_recovers() {
    use std::sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    };
    let (child, mut peer) = UnixStream::pair().unwrap();
    peer.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let (restore_bytes, _, _) = restore();
    let host_worker = std::thread::spawn(move || {
        mooring_collab::write_frame(&mut peer, &open()).unwrap();
        let acquire = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        mooring_collab::write_frame(&mut peer, &grant(&acquire)).unwrap();
        mooring_collab::write_frame(&mut peer, &restore_bytes).unwrap();
        for _ in 0..2 {
            mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        }
        peer
    });
    let failing = Arc::new(AtomicBool::new(false));
    let clock = failing.clone();
    let mut driver = Driver::from_negotiated_inherited_stream(child, &host(), move || {
        if clock.load(Ordering::SeqCst) {
            Err(std::io::Error::other("clock unavailable"))
        } else {
            Ok(at(1))
        }
    })
    .unwrap();
    let mut peer = host_worker.join().unwrap();
    failing.store(true, Ordering::SeqCst);
    assert!(driver.check_owner().is_err());
    assert!(matches!(
        mooring_collab::read_frame(&mut peer),
        Ok(None) | Err(_)
    ));
    failing.store(false, Ordering::SeqCst);
    assert!(driver.check_owner().is_err());
}

fn bind_checkpoint_identity(value: &mut Value) {
    let cp = serde_json::from_value(value.clone()).unwrap();
    let identity = mooring_protocol::checkpoint::compute(
        &cp,
        mooring_protocol::checkpoint::Limits {
            decoded_bytes: control::MAX_CHECKPOINT_BYTES,
            preimage_bytes: control::MAX_CONTROL_BYTES,
        },
    )
    .unwrap();
    value["digest"] = json!(identity.full_digest());
}

#[test]
fn review_4131_descriptor_binding_fails_closed_before_any_owner_grant() {
    for swap in ["scope", "session", "runtime", "tuple", "digest"] {
        let (child, mut peer) = UnixStream::pair().unwrap();
        peer.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
        let (restore_raw, _, _) = restore();
        let host_worker = std::thread::spawn(move || {
            mooring_collab::write_frame(&mut peer, &open()).unwrap();
            let acquire = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
            assert_eq!(
                serde_json::from_slice::<Value>(&acquire).unwrap()["type"],
                "owner_acquire"
            );
            let restore: Value = serde_json::from_slice(&restore_raw).unwrap();
            let cp = serde_json::from_value(restore["checkpoint"].clone()).unwrap();
            let identity = mooring_protocol::checkpoint::verify(
                &cp,
                mooring_protocol::checkpoint::Limits {
                    decoded_bytes: control::MAX_CHECKPOINT_BYTES,
                    preimage_bytes: control::MAX_CONTROL_BYTES,
                },
            )
            .unwrap();
            let mut request = json!({"type":"session_describe_checkpoint","request_id":"review-descriptor","control_profile":"mooring.session-causal.v1","scope":restore["scope"],"session_id":restore["session_id"],"runtime_ref":restore["runtime_ref"],"versions":restore["versions"],"checkpoint":{"encoding":"base64","state":restore["checkpoint"]["state"],"digest":identity.content_digest()}});
            match swap {
                "scope" => request["scope"]["artifact_id"] = json!("other-artifact"),
                "session" => request["session_id"] = json!("other-session"),
                "runtime" => request["runtime_ref"] = json!("other-child"),
                "tuple" => request["versions"]["model_version"] = json!("0.1.0"),
                "digest" => {
                    request["checkpoint"]["digest"] = json!(format!("sha256:{}", "0".repeat(64)))
                }
                _ => unreachable!(),
            }
            mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&request).unwrap()).unwrap();
            assert!(
                matches!(mooring_collab::read_frame(&mut peer), Ok(None) | Err(_)),
                "wrong-bound description must not emit content or startup acknowledgements"
            );
        });
        assert!(
            Driver::from_negotiated_inherited_stream(child, &host(), || Ok(at(1))).is_err(),
            "{swap}"
        );
        host_worker.join().unwrap();
    }
}

fn renewing_driver() -> (
    Driver,
    UnixStream,
    std::sync::Arc<std::sync::atomic::AtomicU64>,
) {
    renewing_driver_with_duration(1000)
}
fn renewing_driver_with_duration(
    duration: u64,
) -> (
    Driver,
    UnixStream,
    std::sync::Arc<std::sync::atomic::AtomicU64>,
) {
    renewing_driver_selected(duration, false)
}
fn renewing_driver_selected(
    duration: u64,
    native: bool,
) -> (
    Driver,
    UnixStream,
    std::sync::Arc<std::sync::atomic::AtomicU64>,
) {
    use std::sync::{
        atomic::{AtomicU64, Ordering},
        Arc,
    };
    let now = Arc::new(AtomicU64::new(0));
    let clock = now.clone();
    let (child, mut peer) = UnixStream::pair().unwrap();
    peer.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
    let (restored, _, _) = restore();
    let worker = std::thread::spawn(move || {
        let opening = if native {
            let mut v: Value = serde_json::from_slice(&open()).unwrap();
            v["required_capabilities"] = json!([
                "checkpoint_identity_v1",
                "human_native_prepare_v1",
                "origin_inventory_v1"
            ]);
            serde_json::to_vec(&v).unwrap()
        } else {
            open()
        };
        mooring_collab::write_frame(&mut peer, &opening).unwrap();
        let acquire = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        let mut granted: Value = serde_json::from_slice(&grant(&acquire)).unwrap();
        granted["lease_duration_ms"] = json!(duration.to_string());
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&granted).unwrap()).unwrap();
        mooring_collab::write_frame(&mut peer, &restored).unwrap();
        for _ in 0..2 {
            mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        }
        peer
    });
    let constructor = if native {
        Driver::from_negotiated_inherited_stream_with_native_humans
    } else {
        Driver::from_negotiated_inherited_stream
    };
    let driver = constructor(child, &host(), move || Ok(at(clock.load(Ordering::SeqCst)))).unwrap();
    (driver, worker.join().unwrap(), now)
}
fn renewal_reply(request: &[u8], permission: &str) -> Vec<u8> {
    let request: Value = serde_json::from_slice(request).unwrap();
    serde_json::to_vec(&json!({"type":"owner_grant","request_id":request["request_id"],"scope":request["scope"],"session_id":request["session_id"],"runtime_ref":"fresh-authenticated-child","fence":request["fence"],"lease_duration_ms":"1000","revision":"999","renewal_nonce":request["renewal_nonce"],"permission_version":permission})).unwrap()
}
fn receive_json(peer: &mut UnixStream) -> (Vec<u8>, Value) {
    let raw = mooring_collab::read_frame(peer).unwrap().unwrap();
    let value = serde_json::from_slice(&raw).unwrap();
    (raw, value)
}

fn admitted_actor() -> (mooring_collab::session_actor::RunningDriver, UnixStream) {
    admitted_actor_with_write(true)
}
fn admitted_actor_with_write(
    write: bool,
) -> (mooring_collab::session_actor::RunningDriver, UnixStream) {
    let (actor, peer, _) = admitted_actor_with_clock(write, 1000);
    (actor, peer)
}
fn admitted_actor_with_clock(
    write: bool,
    owner_duration: u64,
) -> (
    mooring_collab::session_actor::RunningDriver,
    UnixStream,
    std::sync::Arc<std::sync::atomic::AtomicU64>,
) {
    admitted_actor_with_delivery_clock(write, owner_duration, 2000)
}
fn admitted_actor_with_delivery_clock(
    write: bool,
    owner_duration: u64,
    delivery_duration: u64,
) -> (
    mooring_collab::session_actor::RunningDriver,
    UnixStream,
    std::sync::Arc<std::sync::atomic::AtomicU64>,
) {
    admitted_actor_selected(write, owner_duration, delivery_duration, false)
}
fn admitted_actor_selected(
    write: bool,
    owner_duration: u64,
    delivery_duration: u64,
    native: bool,
) -> (
    mooring_collab::session_actor::RunningDriver,
    UnixStream,
    std::sync::Arc<std::sync::atomic::AtomicU64>,
) {
    let (driver, mut peer, now) = renewing_driver_selected(owner_duration, native);
    let scope = driver.confirmed().checkpoint().scope.clone();
    let versions = driver.confirmed().checkpoint().versions.clone();
    let mut actor = driver.into_actor().unwrap();
    let host_worker = std::thread::spawn(move || {
        let open = json!({"type":"session_participant_open","request_id":"private-open","control_profile":"mooring.session-causal.v1",
            "scope":scope,"session_id":"host-session","runtime_ref":"fresh-authenticated-child",
            "fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
            "connection_ref":"connection","join":{"type":"session_join","request_id":"public-join","ticket":"MOCK_HOST_ISSUED_TICKET","versions":versions}});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&open).unwrap()).unwrap();
        let (_, ticket) = receive_json(&mut peer);
        assert_eq!(ticket["type"], "ticket_validate");
        let reply = json!({"type":"ticket_validated","request_id":ticket["request_id"],"scope":ticket["scope"],"session_id":ticket["session_id"],
            "generation":"7","participant_ref":"participant","permission_version":"3","granted":{"read":true,"write":write},
            "versions":versions,"expires_in_ms":delivery_duration.to_string(),"validation_nonce":ticket["validation_nonce"],"gate_id":"gate"});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&reply).unwrap()).unwrap();
        let (_, joined) = receive_json(&mut peer);
        assert_eq!(joined["message"]["type"], "session_joined");
        assert_eq!(joined["message"]["request_id"], "public-join");
        assert_eq!(joined["participant_ref"], "participant");
        peer
    });
    let limit = std::time::Instant::now() + Duration::from_secs(3);
    while !host_worker.is_finished() {
        actor.step(&host(), Duration::from_millis(20)).unwrap();
        assert!(
            std::time::Instant::now() < limit,
            "mock ticket handshake stalled"
        );
    }
    (actor, host_worker.join().unwrap(), now)
}
fn agent_change(
    scope: &mooring_protocol::Scope,
    id: &str,
    dependency: Option<&str>,
    text: &str,
) -> mooring_protocol::AtomicChange {
    let change = json!({"change_id":format!("change-{id}"),"membership_hash":format!("sha256:{}","0".repeat(64)),
        "operations":[{"op_id":id,"fingerprint":format!("sha256:{}","0".repeat(64)),"participant_ref":"participant","base_revision":"10",
            "dependencies":dependency.into_iter().collect::<Vec<_>>(),"read_preconditions":[{"target_id":"stable-0","revision":"10"}],
            "payload":{"kind":"text.insert","target_id":"stable-0","at":{"kind":"end"},"text":text}}]});
    mooring_protocol::canonical::fingerprint_atomic(scope, &serde_json::to_vec(&change).unwrap())
        .unwrap()
        .with_fingerprints()
}
fn participant_input(
    scope: &mooring_protocol::Scope,
    changes: &[mooring_protocol::AtomicChange],
    id: &str,
) -> Value {
    json!({"type":"session_participant_input","request_id":format!("private-{id}"),"control_profile":"mooring.session-causal.v1",
        "scope":scope,"session_id":"host-session","runtime_ref":"fresh-authenticated-child",
        "fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
        "connection_ref":"connection","participant_ref":"participant","gate_id":"gate","permission_version":"3",
        "message":{"type":"submit_changes","request_id":id,"scope":scope,"session_id":"host-session",
            "versions":mooring_wordcraft::profile::qualified_versions(),"changes":changes}})
}
fn mock_commit(
    batch: &Value,
    checkpoint: &mut mooring_protocol::Checkpoint,
    targets: &mut Vec<TargetVersion>,
) -> Value {
    mock_commit_selected(batch, checkpoint, targets, false)
}
fn mock_commit_selected(
    batch: &Value,
    checkpoint: &mut mooring_protocol::Checkpoint,
    targets: &mut Vec<TargetVersion>,
    native: bool,
) -> Value {
    use mooring_core::{
        adapter::{CausalPrepareAdapter, Prepared, PreparedCandidate},
        causal::{CausalPrepareContext, EntryVersion, QualifiedRead},
    };
    let changes: Vec<mooring_protocol::AtomicChange> =
        serde_json::from_value(batch["changes"].clone()).unwrap();
    let mut outcomes = Vec::new();
    let mut native_deltas = Vec::new();
    for change in changes {
        let before = mooring_protocol::checkpoint::verify(
            checkpoint,
            mooring_protocol::checkpoint::Limits {
                decoded_bytes: control::MAX_CHECKPOINT_BYTES,
                preimage_bytes: control::MAX_CONTROL_BYTES,
            },
        )
        .unwrap();
        let next = checkpoint.revision.0 + 1;
        let mut qualified_reads = Vec::new();
        for op in &change.operations {
            for (index, read) in op.read_preconditions.iter().enumerate() {
                let target = targets
                    .iter()
                    .find(|target| target.target_id == read.target_id)
                    .unwrap();
                qualified_reads.push(QualifiedRead {
                    op_id: op.op_id.clone(),
                    read_index: index as u32,
                    target_id: read.target_id.clone(),
                    observed_revision: read.revision,
                    observed_structure_revision: read.structure_revision,
                    entry: Some(EntryVersion {
                        revision: target.revision,
                        structure_revision: target.structure_revision,
                        deleted: target.deleted,
                    }),
                    covered: true, // MOCK host history attestation; NOT SQL qualification.
                });
            }
        }
        let context = CausalPrepareContext {
            document_revision: checkpoint.revision,
            targets: targets.clone(),
            operation_revisions: vec![mooring_protocol::PreparedOperationRevision {
                op_id: change.operations[0].op_id.clone(),
                revision: Counter(next),
            }],
            qualified_reads,
        };
        let Prepared::Candidate(candidate) = WordCraftAdapter
            .prepare_atomic_causal(before.state(), &change, &context)
            .unwrap()
        else {
            panic!("mock host app rejected");
        };
        let opaque = control::encode_checkpoint(candidate.checkpoint_bytes()).unwrap();
        if native {
            let updated = Document::restore(candidate.checkpoint_bytes()).unwrap();
            let raw = updated.update_since(before.state()).unwrap();
            let allocations = mooring_wordcraft::document_update::allocation_origins(&raw)
                .unwrap()
                .iter()
                .map(u64::to_string)
                .collect::<Vec<_>>();
            let affected: std::collections::BTreeSet<_> = candidate
                .effects()
                .iter()
                .flat_map(|e| e.targets.iter().map(|t| t.target_id.clone()))
                .collect();
            native_deltas.push(json!({"change_id":change.change_id,"membership_hash":change.membership_hash,"revision":next.to_string(),"checkpoint_state_digest":opaque.digest,"native_delta":{"writer_origin":candidate.native_writer_origin().unwrap().to_string(),"allocation_origins":allocations,"update":mooring_protocol::boundary::encode_base64(&raw,262144).unwrap(),"targets":affected}}));
        }
        checkpoint.state = opaque.state;
        checkpoint.revision = Counter(next);
        for effect in candidate.effects().iter().flat_map(|op| &op.targets) {
            let target = targets
                .iter_mut()
                .find(|target| target.target_id == effect.target_id)
                .unwrap();
            target.revision = Counter(next);
            if effect.structure_changed {
                target.structure_revision = Counter(next);
            }
            target.deleted = effect.deleted;
        }
        let mut value = serde_json::to_value(&*checkpoint).unwrap();
        bind_checkpoint_identity(&mut value);
        *checkpoint = serde_json::from_value(value).unwrap();
        outcomes.push(json!({"change_id":change.change_id,"status":"saved","first_revision":next.to_string(),"last_revision":next.to_string(),
            "operations":[{"op_id":change.operations[0].op_id,"status":"saved","revision":next.to_string(),"issuer":"crewship","durability":"node"}]}));
    }
    let mut result = json!({"type":"session_commit_result","request_id":batch["request_id"],"control_profile":"mooring.session-causal.v1",
        "scope":batch["scope"],"session_id":batch["session_id"],"runtime_ref":batch["runtime_ref"],"fence":batch["fence"],"versions":batch["versions"],
        "receipt":{"type":"commit_result","request_id":batch["request_id"],"scope":batch["scope"],"generation":"7","revision":checkpoint.revision,
            "issuer":"crewship","durability":"node","changes":outcomes},"checkpoint":checkpoint,"targets":targets});
    if native {
        result["native_deltas"] = json!(native_deltas);
    }
    result
}

#[test]
fn admitted_actor_actual_timer_sends_b_before_a_ack_then_installs_only_correlated_host_checkpoints()
{
    let (mut actor, mut peer) = admitted_actor();
    let before = actor.confirmed().document().native().unwrap();
    let ids = actor.confirmed().document().block_ids().unwrap();
    let mut checkpoint = actor.confirmed().checkpoint().clone();
    let mut targets = actor.confirmed().targets().to_vec();
    let a = agent_change(&checkpoint.scope, "A", None, " A");
    let b = agent_change(&checkpoint.scope, "B", Some("A"), " B");
    let (events, receive) = std::sync::mpsc::channel();
    let (permit, permits) = std::sync::mpsc::channel();
    let worker = std::thread::spawn(move || {
        let started = std::time::Instant::now();
        mooring_collab::write_frame(
            &mut peer,
            &serde_json::to_vec(&participant_input(
                &checkpoint.scope,
                std::slice::from_ref(&a),
                "submit-A",
            ))
            .unwrap(),
        )
        .unwrap();
        let (_, pending_a) = receive_json(&mut peer);
        assert_eq!(pending_a["message"]["state"], "pending");
        let (_, first) = receive_json(&mut peer);
        let first_delay = started.elapsed();
        assert_eq!(first["type"], "session_submit_batch");
        assert!(started.elapsed() >= Duration::from_millis(20));
        let a_reply = mock_commit(&first, &mut checkpoint, &mut targets);
        mooring_collab::write_frame(
            &mut peer,
            &serde_json::to_vec(&participant_input(&checkpoint.scope, &[a], "retry-A")).unwrap(),
        )
        .unwrap();
        let (_, retry) = receive_json(&mut peer);
        assert_eq!(retry["message"]["state"], "pending");
        let b_started = std::time::Instant::now();
        mooring_collab::write_frame(
            &mut peer,
            &serde_json::to_vec(&participant_input(&checkpoint.scope, &[b], "submit-B")).unwrap(),
        )
        .unwrap();
        let (_, pending_b) = receive_json(&mut peer);
        assert_eq!(pending_b["message"]["state"], "pending");
        let (_, second) = receive_json(&mut peer);
        assert_eq!(second["type"], "session_submit_batch");
        assert_eq!(second["confirmed_basis"]["revision"], "10");
        assert_eq!(
            second["changes"][0]["operations"][0]["dependencies"],
            json!(["A"])
        );
        eprintln!("real native timer: A={}ms B={}ms after respective input, both before host A ACK (host authority/history/receipts MOCK)", first_delay.as_millis(), b_started.elapsed().as_millis());
        let b_reply = mock_commit(&second, &mut checkpoint, &mut targets);
        events.send(0).unwrap();
        permits.recv().unwrap();
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&a_reply).unwrap()).unwrap();
        let (_, saved_a) = receive_json(&mut peer);
        assert_eq!(
            saved_a["message"]["changes"][0]["operations"][0]["op_id"],
            "A"
        );
        events.send(1).unwrap();
        permits.recv().unwrap();
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&b_reply).unwrap()).unwrap();
        let (_, saved_b) = receive_json(&mut peer);
        assert_eq!(
            saved_b["message"]["changes"][0]["operations"][0]["op_id"],
            "B"
        );
        events.send(2).unwrap();
        peer
    });
    let deadline = std::time::Instant::now() + Duration::from_secs(4);
    loop {
        actor.step(&host(), Duration::from_millis(20)).unwrap();
        if let Ok(stage) = receive.try_recv() {
            let mut expected = before.clone();
            let suffix = ["", " A", " A B"][stage];
            expected["body"][0]["text"] = json!(format!(
                "{}{}",
                before["body"][0]["text"].as_str().unwrap(),
                suffix
            ));
            expected["body"][0]["runs"][0]["len"] =
                json!(before["body"][0]["runs"][0]["len"].as_u64().unwrap() + suffix.len() as u64);
            assert_eq!(actor.confirmed().document().native().unwrap(), expected);
            assert_eq!(actor.confirmed().document().block_ids().unwrap(), ids);
            assert_eq!(
                actor.confirmed().checkpoint().revision,
                Counter(10 + stage as u64)
            );
            if stage == 2 {
                break;
            }
            permit.send(()).unwrap();
        }
        assert!(
            std::time::Instant::now() < deadline,
            "actual actor/mock host stalled"
        );
    }
    let _peer = worker.join().unwrap();
    let (_, revision, remaining) = actor.into_recovery();
    assert_eq!(revision, 12);
    assert!(remaining.is_empty());
}

#[test]
fn admitted_actor_uncertainty_bad_member_and_duplicate_receipt_keep_original_intents() {
    for mode in ["uncertain", "foreign-member", "duplicate-A"] {
        let (mut actor, mut peer) = admitted_actor();
        let before = actor.confirmed().document().native().unwrap();
        let ids = actor.confirmed().document().block_ids().unwrap();
        let mut checkpoint = actor.confirmed().checkpoint().clone();
        let mut targets = actor.confirmed().targets().to_vec();
        let a = agent_change(&checkpoint.scope, "A", None, " A");
        let b = agent_change(&checkpoint.scope, "B", Some("A"), " B");
        let originals = [a.clone(), b.clone()];
        let worker = std::thread::spawn(move || {
            mooring_collab::write_frame(
                &mut peer,
                &serde_json::to_vec(&participant_input(&checkpoint.scope, &[a], "submit-A"))
                    .unwrap(),
            )
            .unwrap();
            assert_eq!(receive_json(&mut peer).1["message"]["state"], "pending");
            let first = receive_json(&mut peer).1;
            let mut reply = mock_commit(&first, &mut checkpoint, &mut targets);
            mooring_collab::write_frame(
                &mut peer,
                &serde_json::to_vec(&participant_input(&checkpoint.scope, &[b], "submit-B"))
                    .unwrap(),
            )
            .unwrap();
            assert_eq!(receive_json(&mut peer).1["message"]["state"], "pending");
            assert_eq!(
                receive_json(&mut peer).1["changes"][0]["operations"][0]["op_id"],
                "B"
            );
            if mode == "uncertain" {
                return;
            } // Simulated nonterminal SQL/transport failure: no guessed receipt.
            if mode == "foreign-member" {
                reply["receipt"]["changes"][0]["operations"][0]["op_id"] = json!("foreign");
            }
            mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&reply).unwrap()).unwrap();
            if mode == "duplicate-A" {
                assert_eq!(
                    receive_json(&mut peer).1["message"]["changes"][0]["operations"][0]["op_id"],
                    "A"
                );
                mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&reply).unwrap())
                    .unwrap();
            }
            assert!(mooring_collab::read_frame(&mut peer).unwrap().is_none());
        });
        let deadline = std::time::Instant::now() + Duration::from_secs(4);
        while actor.step(&host(), Duration::from_millis(20)).is_ok() {
            assert!(
                std::time::Instant::now() < deadline,
                "uncertain actor did not close"
            );
        }
        worker.join().unwrap();
        let (document, revision, remaining) = actor.into_recovery();
        let saved_a = mode == "duplicate-A";
        assert_eq!(revision, if saved_a { 11 } else { 10 });
        let mut expected = before;
        if saved_a {
            expected["body"][0]["text"] = json!(format!(
                "{} A",
                expected["body"][0]["text"].as_str().unwrap()
            ));
            expected["body"][0]["runs"][0]["len"] =
                json!(expected["body"][0]["runs"][0]["len"].as_u64().unwrap() + 2);
        }
        assert_eq!(document.document().native().unwrap(), expected);
        assert_eq!(document.document().block_ids().unwrap(), ids);
        let expected_originals = if saved_a {
            &originals[1..]
        } else {
            &originals[..]
        };
        assert_eq!(remaining.len(), expected_originals.len());
        for (actual, expected) in remaining.iter().zip(expected_originals) {
            assert_eq!(
                actual.value.as_ref(),
                expected,
                "changed original after {mode}"
            );
        }
    }
}

#[test]
fn actor_input_transplants_read_only_write_and_ticket_replay_cannot_enter_the_scheduler() {
    for field in [
        "participant_ref",
        "gate_id",
        "permission_version",
        "connection_ref",
        "membership_hash",
        "original_author",
        "read-only-write",
        "ticket-replay",
    ] {
        let (mut actor, mut peer) = admitted_actor_with_write(field != "read-only-write");
        let before = actor.confirmed().document().checkpoint().unwrap();
        let change = agent_change(&actor.confirmed().checkpoint().scope, "A", None, " A");
        let mut input = participant_input(
            &actor.confirmed().checkpoint().scope,
            &[change],
            "transplant",
        );
        match field {
            "read-only-write" => {}
            "ticket-replay" => {
                input = json!({"type":"ticket_validated","request_id":"actor-ticket:1","scope":actor.confirmed().checkpoint().scope,
                "session_id":"host-session","generation":"7","participant_ref":"participant","permission_version":"3",
                "granted":{"read":true,"write":true},"versions":mooring_wordcraft::profile::qualified_versions(),
                "expires_in_ms":"2000","validation_nonce":"actor-ticket:1","gate_id":"gate"})
            }
            "permission_version" => input[field] = json!("4"),
            "membership_hash" => {
                input["message"]["changes"][0][field] = json!(format!("sha256:{}", "f".repeat(64)))
            }
            "original_author" => {
                input["message"]["changes"][0]["operations"][0]["participant_ref"] =
                    json!("other-author")
            }
            _ => input[field] = json!("foreign"),
        }
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&input).unwrap()).unwrap();
        let deadline = std::time::Instant::now() + Duration::from_secs(2);
        while actor.step(&host(), Duration::from_millis(20)).is_ok() {
            assert!(std::time::Instant::now() < deadline);
        }
        let (document, revision, remaining) = actor.into_recovery();
        assert_eq!(revision, 10);
        assert_eq!(document.document().checkpoint().unwrap(), before);
        assert!(remaining.is_empty(), "unadmitted {field} became pending");
    }
}
#[test]
fn consuming_actor_keeps_original_doc_fd_worker_and_pending_renewal() {
    use std::sync::atomic::Ordering;
    let (mut driver, mut peer, now) = renewing_driver();
    let before = driver.confirmed().document().checkpoint().unwrap();
    let native = driver.confirmed().document().native().unwrap();
    let ids = driver.confirmed().document().block_ids().unwrap();
    now.store(500, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (original, _) = receive_json(&mut peer);
    now.store(600, Ordering::SeqCst);
    let mut running = driver.into_actor().unwrap();
    assert_eq!(running.confirmed().document().native().unwrap(), native);
    assert_eq!(running.confirmed().document().block_ids().unwrap(), ids);
    now.store(750, Ordering::SeqCst);
    running.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (retry, _) = receive_json(&mut peer);
    assert_eq!(retry, original);

    let cp = running.confirmed().checkpoint();
    let identity = mooring_protocol::checkpoint::verify(
        cp,
        mooring_protocol::checkpoint::Limits {
            decoded_bytes: control::MAX_CHECKPOINT_BYTES,
            preimage_bytes: control::MAX_CONTROL_BYTES,
        },
    )
    .unwrap();
    let request = json!({"type":"validate_checkpoint_causal","request_id":"prepare-after-consuming-transition","scope":cp.scope,"selected":cp.versions,"checkpoint":{"encoding":"base64","state":cp.state,"digest":identity.content_digest()},"control_profile":"mooring.prepare-causal.v1"});
    mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&request).unwrap()).unwrap();
    let (_, prepared) = receive_json(&mut peer);
    assert_eq!(prepared["type"], "checkpoint_validated_causal");
    assert_eq!(prepared["request_id"], "prepare-after-consuming-transition");
    mooring_collab::write_frame(&mut peer, &renewal_reply(&original, "4")).unwrap();
    now.store(800, Ordering::SeqCst);
    running
        .maintain_owner(&host(), Duration::from_millis(20))
        .unwrap();
    assert_eq!(running.known_owner_permission(), Counter(4));
    assert_eq!(running.confirmed().checkpoint().revision, Counter(10));
    assert_eq!(running.confirmed().document().checkpoint().unwrap(), before);
    now.store(1000, Ordering::SeqCst);
    running.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (next, _) = receive_json(&mut peer);
    assert_ne!(next, original);
    now.store(1500, Ordering::SeqCst);
    assert!(running.check_owner().is_err());
    let _ = mooring_collab::write_frame(&mut peer, &renewal_reply(&next, "5"));
    assert!(running.maintain_owner(&host(), Duration::ZERO).is_err());
    let (document, revision, originals) = running.into_recovery();
    assert_eq!(revision, 10);
    assert_eq!(document.document().checkpoint().unwrap(), before);
    assert_eq!(document.document().native().unwrap(), native);
    assert_eq!(document.document().block_ids().unwrap(), ids);
    assert!(originals.is_empty());
}

#[test]
fn consuming_transition_cannot_restart_an_expired_or_reversed_owner() {
    use std::sync::atomic::Ordering;
    for reverse in [false, true] {
        let (mut driver, mut peer, now) = renewing_driver();
        if reverse {
            now.store(100, Ordering::SeqCst);
            driver.check_owner().unwrap();
            now.store(99, Ordering::SeqCst);
        } else {
            now.store(1000, Ordering::SeqCst);
        }
        assert!(driver.into_actor().is_err());
        assert!(mooring_collab::read_frame(&mut peer).unwrap().is_none());
    }
}

#[test]
fn running_actor_rejects_unqualified_document_replacement_and_recovers_original() {
    let (driver, mut peer, _) = renewing_driver();
    let before = driver.confirmed().document().checkpoint().unwrap();
    let mut running = driver.into_actor().unwrap();
    mooring_collab::write_frame(&mut peer, &restore().0).unwrap();
    let deadline = std::time::Instant::now() + Duration::from_secs(2);
    while running
        .maintain_owner(&host(), Duration::from_millis(20))
        .is_ok()
    {
        assert!(std::time::Instant::now() < deadline);
    }
    let (document, revision, originals) = running.into_recovery();
    assert_eq!(revision, 10);
    assert_eq!(document.document().checkpoint().unwrap(), before);
    assert!(originals.is_empty());
}

#[test]
fn driver_renews_exact_first_send_nonce_and_only_advances_owner_knowledge() {
    use std::sync::atomic::Ordering;
    let (mut driver, mut peer, now) = renewing_driver();
    let before = driver.confirmed().document().checkpoint().unwrap();
    now.store(500, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (first, request) = receive_json(&mut peer);
    assert_eq!(request["type"], "owner_renew");
    let response = renewal_reply(&first, "4");
    mooring_collab::write_frame(&mut peer, &response).unwrap();
    now.store(550, Ordering::SeqCst);
    driver
        .maintain_owner(&host(), Duration::from_millis(20))
        .unwrap();
    assert_eq!(driver.known_owner_permission(), Counter(4));
    assert_eq!(driver.confirmed().checkpoint().revision, Counter(10));
    assert_eq!(driver.confirmed().document().checkpoint().unwrap(), before);
    // Exact duplicate is ignored without applying the grant's revision999 or
    // moving its first-send500+duration1000 deadline.
    mooring_collab::write_frame(&mut peer, &response).unwrap();
    now.store(999, Ordering::SeqCst);
    driver
        .maintain_owner(&host(), Duration::from_millis(20))
        .unwrap();
    now.store(1000, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (second, request) = receive_json(&mut peer);
    assert_ne!(first, second);
    assert_eq!(request["permission_version"], "4");
    now.store(1250, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (retry, _) = receive_json(&mut peer);
    assert_eq!(retry, second);
    mooring_collab::write_frame(&mut peer, &renewal_reply(&second, "4")).unwrap();
    now.store(1400, Ordering::SeqCst);
    driver
        .maintain_owner(&host(), Duration::from_millis(20))
        .unwrap();
    now.store(1500, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (third, _) = receive_json(&mut peer);
    now.store(2000, Ordering::SeqCst);
    assert!(driver.maintain_owner(&host(), Duration::ZERO).is_err());
    let _ = mooring_collab::write_frame(&mut peer, &renewal_reply(&third, "5"));
    assert!(driver.maintain_owner(&host(), Duration::ZERO).is_err());
    assert_eq!(driver.known_owner_permission(), Counter(4));
    assert_eq!(driver.confirmed().document().checkpoint().unwrap(), before);
}
#[test]
fn preparation_progresses_while_owner_renewal_reply_is_withheld() {
    use std::sync::atomic::Ordering;
    let (mut driver, mut peer, now) = renewing_driver();
    now.store(500, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (renew, _) = receive_json(&mut peer);
    let cp = driver.confirmed().checkpoint();
    let identity = mooring_protocol::checkpoint::verify(
        cp,
        mooring_protocol::checkpoint::Limits {
            decoded_bytes: 1572864,
            preimage_bytes: 2097152,
        },
    )
    .unwrap();
    let request = json!({"type":"validate_checkpoint_causal","request_id":"while-owner-pending","scope":cp.scope,"selected":cp.versions,"checkpoint":{"encoding":"base64","state":cp.state,"digest":identity.content_digest()},"control_profile":"mooring.prepare-causal.v1"});
    mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&request).unwrap()).unwrap();
    let (_, prepared) = receive_json(&mut peer);
    assert_eq!(prepared["type"], "checkpoint_validated_causal");
    assert_eq!(prepared["request_id"], "while-owner-pending");
    mooring_collab::write_frame(&mut peer, &renewal_reply(&renew, "3")).unwrap();
    now.store(600, Ordering::SeqCst);
    driver
        .maintain_owner(&host(), Duration::from_millis(20))
        .unwrap();
    assert_eq!(driver.confirmed().checkpoint().revision, Counter(10));
}
#[test]
fn unexpected_actor_control_or_reversed_clock_closes_without_replacing_document() {
    use std::sync::atomic::Ordering;
    for reverse in [false, true] {
        let (mut driver, mut peer, now) = renewing_driver();
        let before = driver.confirmed().document().checkpoint().unwrap();
        if reverse {
            now.store(100, Ordering::SeqCst);
            driver.check_owner().unwrap();
            now.store(99, Ordering::SeqCst);
        } else {
            // A second startup restore is not a durable settlement authority.
            mooring_collab::write_frame(&mut peer, &restore().0).unwrap();
        }
        // One bounded maintenance tick may time out before the sole reader
        // has normalized the complete hostile frame. Require eventual closure,
        // not delivery by the OS within the first20ms scheduling quantum.
        let deadline = std::time::Instant::now() + Duration::from_secs(2);
        loop {
            if driver
                .maintain_owner(&host(), Duration::from_millis(20))
                .is_err()
            {
                break;
            }
            assert!(
                std::time::Instant::now() < deadline,
                "hostile control did not close owner"
            );
        }
        assert!(driver.check_owner().is_err());
        assert_eq!(driver.confirmed().document().checkpoint().unwrap(), before);
    }
}

#[test]
fn duplicate_completed_grant_does_not_extend_old_lease_while_next_reply_is_pending() {
    use std::sync::atomic::Ordering;
    let (mut driver, mut peer, now) = renewing_driver();
    now.store(500, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (first, _) = receive_json(&mut peer);
    let reply = renewal_reply(&first, "4");
    mooring_collab::write_frame(&mut peer, &reply).unwrap();
    now.store(550, Ordering::SeqCst);
    driver
        .maintain_owner(&host(), Duration::from_millis(20))
        .unwrap();
    now.store(1000, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (_second, _) = receive_json(&mut peer);
    mooring_collab::write_frame(&mut peer, &reply).unwrap();
    now.store(1499, Ordering::SeqCst);
    driver
        .maintain_owner(&host(), Duration::from_millis(20))
        .unwrap();
    now.store(1500, Ordering::SeqCst);
    assert!(driver.maintain_owner(&host(), Duration::ZERO).is_err());
}
#[test]
fn nonidentical_completed_nonce_reply_closes_instead_of_becoming_new_permission() {
    use std::sync::atomic::Ordering;
    let (mut driver, mut peer, now) = renewing_driver();
    now.store(500, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (first, _) = receive_json(&mut peer);
    mooring_collab::write_frame(&mut peer, &renewal_reply(&first, "4")).unwrap();
    now.store(550, Ordering::SeqCst);
    driver
        .maintain_owner(&host(), Duration::from_millis(20))
        .unwrap();
    now.store(1000, Ordering::SeqCst);
    driver.maintain_owner(&host(), Duration::ZERO).unwrap();
    let (_second, _) = receive_json(&mut peer);
    mooring_collab::write_frame(&mut peer, &renewal_reply(&first, "5")).unwrap();
    now.store(1100, Ordering::SeqCst);
    assert!(driver
        .maintain_owner(&host(), Duration::from_millis(20))
        .is_err());
    assert_eq!(driver.known_owner_permission(), Counter(4));
}

#[test]
fn optin_origin_inventory_progresses_before_owner_and_after_owned_startup() {
    let (child, mut peer) = UnixStream::pair().unwrap();
    peer.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let (restore_bytes, native, ids) = restore();
    let mut restored: Value = serde_json::from_slice(&restore_bytes).unwrap();
    restored["checkpoint"]["generation"] = json!("6");
    bind_checkpoint_identity(&mut restored["checkpoint"]);
    let full: mooring_protocol::Checkpoint =
        serde_json::from_value(restored["checkpoint"].clone()).unwrap();
    let verified = mooring_protocol::checkpoint::verify(
        &full,
        mooring_protocol::checkpoint::Limits {
            decoded_bytes: control::MAX_CHECKPOINT_BYTES,
            preimage_bytes: control::MAX_CONTROL_BYTES,
        },
    )
    .unwrap();
    let expected: Vec<_> = mooring_wordcraft::origin_inventory::inspect(verified.state())
        .unwrap()
        .allocated()
        .iter()
        .map(|(origin, clock)| json!({"origin":origin.to_string(),"clock":clock.to_string()}))
        .collect();
    let request = json!({"type":"session_inspect_origins","request_id":"origins-before-owner","control_profile":restored["control_profile"],"scope":restored["scope"],"session_id":restored["session_id"],"runtime_ref":restored["runtime_ref"],"fence":restored["fence"],"versions":restored["versions"],"checkpoint":restored["checkpoint"]});
    let host_worker = std::thread::spawn(move || {
        let mut opening: Value = serde_json::from_slice(&open()).unwrap();
        opening["required_capabilities"] = json!(["origin_inventory_v1", "checkpoint_identity_v1"]);
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&opening).unwrap()).unwrap();
        let acquire = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        // Preparation must progress before the actor has received its owner.
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&request).unwrap()).unwrap();
        let inspected: Value =
            serde_json::from_slice(&mooring_collab::read_frame(&mut peer).unwrap().unwrap())
                .unwrap();
        assert_eq!(inspected["type"], "session_origins_inspected");
        assert_eq!(inspected["allocated"], json!(expected));
        assert_eq!(inspected["checkpoint_generation"], "6");
        assert_eq!(inspected["fence"]["generation"], "7");
        assert_eq!(inspected["checkpoint_basis"]["digest"], full.digest);
        assert_eq!(inspected["content_digest"], verified.content_digest());
        mooring_collab::write_frame(&mut peer, &grant(&acquire)).unwrap();
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&restored).unwrap()).unwrap();
        for purpose in ["session_restored", "session_opened"] {
            let ack: Value =
                serde_json::from_slice(&mooring_collab::read_frame(&mut peer).unwrap().unwrap())
                    .unwrap();
            assert_eq!(ack["type"], purpose);
            if purpose == "session_opened" {
                assert_eq!(ack["capabilities"], opening["required_capabilities"]);
            }
        }
        (peer, request, inspected)
    });
    let driver =
        Driver::from_negotiated_inherited_stream_with_origins(child, &host(), || Ok(at(1)))
            .unwrap();
    assert_eq!(driver.confirmed().document().native().unwrap(), native);
    assert_eq!(driver.confirmed().document().block_ids().unwrap(), ids);
    let before = driver.confirmed().document().checkpoint().unwrap();
    let (mut peer, mut request, mut expected) = host_worker.join().unwrap();
    request["request_id"] = json!("origins-after-startup");
    expected["request_id"] = request["request_id"].clone();
    mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&request).unwrap()).unwrap();
    let actual: Value =
        serde_json::from_slice(&mooring_collab::read_frame(&mut peer).unwrap().unwrap()).unwrap();
    assert_eq!(actual, expected);
    assert_eq!(driver.confirmed().document().checkpoint().unwrap(), before);
    assert_eq!(driver.confirmed().document().native().unwrap(), native);
}

#[test]
fn actor_ready_wake_measures_input_to_send_across_bounded_loads_without_ack() {
    for (operations, text_bytes) in [(1usize, 2usize), (32, 2), (255, 2), (256, 2), (1, 240_000)] {
        for repeat in 0..3 {
            let (mut actor, mut peer) = admitted_actor();
            let checkpoint = actor.confirmed().checkpoint().clone();
            let original = actor.confirmed().document().native().unwrap();
            let changes = (0..operations)
                .map(|n| {
                    agent_change(
                        &checkpoint.scope,
                        &format!("matrix-{n}"),
                        None,
                        &"x".repeat(text_bytes),
                    )
                })
                .collect::<Vec<_>>();
            let input = serde_json::to_vec(&participant_input(
                &checkpoint.scope,
                &changes,
                "matrix-input",
            ))
            .unwrap();
            let (sent, receive) = std::sync::mpsc::channel();
            let worker = std::thread::spawn(move || {
                let started = std::time::Instant::now();
                mooring_collab::write_frame(&mut peer, &input).unwrap();
                let (_, pending) = receive_json(&mut peer);
                assert_eq!(pending["message"]["state"], "pending");
                let (bytes, batch) = receive_json(&mut peer);
                assert_eq!(batch["type"], "session_submit_batch");
                assert_eq!(batch["changes"].as_array().unwrap().len(), operations);
                let delay = started.elapsed();
                sent.send((delay, bytes.len())).unwrap();
                peer
            });
            let deadline = std::time::Instant::now() + Duration::from_secs(4);
            let (delay, bytes) = loop {
                actor.step(&host(), Duration::from_millis(20)).unwrap();
                if let Ok(result) = receive.try_recv() {
                    break result;
                }
                assert!(std::time::Instant::now() < deadline);
            };
            eprintln!("actor wake matrix ops={operations} text={text_bytes} repeat={repeat} input_to_send_us={} batch_bytes={bytes} ACK=held", delay.as_micros());
            assert_eq!(actor.confirmed().document().native().unwrap(), original);
            assert_eq!(actor.confirmed().checkpoint(), &checkpoint);
            let (_, _, retained) = actor.into_recovery();
            assert_eq!(
                retained
                    .iter()
                    .map(|change| change.value.operations.len())
                    .sum::<usize>(),
                operations
            );
            // Keep the actual peer open until all recovery assertions complete.
            drop(worker.join().unwrap());
        }
    }
}

#[test]
fn actor_ready_wake_idle_stays_at_bounded_owner_wait_frequency() {
    let (mut actor, _peer) = admitted_actor();
    let initial = actor.confirmed().checkpoint().clone();
    let started = std::time::Instant::now();
    let mut steps = 0;
    while started.elapsed() < Duration::from_millis(500) {
        actor.step(&host(), Duration::from_secs(1)).unwrap();
        steps += 1;
    }
    assert!(
        (1..=32).contains(&steps),
        "unexpected idle step count {steps}"
    );
    assert_eq!(actor.confirmed().checkpoint(), &initial);
    eprintln!(
        "actor wake idle steps={steps} wall_us={} (existing20msownerwait,no1mspoll)",
        started.elapsed().as_micros()
    );
}

fn drive_actor_to_control_error(actor: &mut mooring_collab::session_actor::RunningDriver) {
    // A readiness wake may precede FD-reader validation or be coalesced with a
    // timer wake. One step need not consume a just-written host frame.
    let deadline = std::time::Instant::now() + Duration::from_secs(2);
    loop {
        if actor.step(&host(), Duration::from_millis(20)).is_err() {
            return;
        }
        assert!(
            std::time::Instant::now() < deadline,
            "host denial was never processed"
        );
    }
}

fn drive_peer_control<T: Send + 'static>(
    actor: &mut mooring_collab::session_actor::RunningDriver,
    peer: UnixStream,
    exchange: impl FnOnce(UnixStream) -> T + Send + 'static,
) -> T {
    let worker = std::thread::spawn(move || exchange(peer));
    let deadline = std::time::Instant::now() + Duration::from_secs(3);
    while !worker.is_finished() {
        actor.step(&host(), Duration::from_millis(20)).unwrap();
        assert!(
            std::time::Instant::now() < deadline,
            "peer exchange stalled"
        );
    }
    worker.join().unwrap()
}

fn delivery_reply(request: &[u8]) -> Value {
    let request: Value = serde_json::from_slice(request).unwrap();
    json!({"type":"delivery_grant","request_id":request["request_id"],"scope":request["scope"],"session_id":request["session_id"],
        "participant_ref":request["participant_ref"],"generation":request["fence"]["generation"],"permission_version":request["permission_version"],
        "renewal_nonce":request["renewal_nonce"],"duration_ms":"2000","gate_id":request["gate_id"]})
}
#[test]
fn actor_delivery_renewal_retains_first_send_and_immediately_catches_up_after_delayed_reply() {
    use std::sync::atomic::Ordering;
    let (mut actor, mut peer, now) = admitted_actor_with_clock(true, 60_000);
    let before = actor.confirmed().document().checkpoint().unwrap();
    now.store(250, Ordering::SeqCst);
    actor.step(&host(), Duration::ZERO).unwrap();
    let (first, request) = receive_json(&mut peer);
    assert_eq!(request["type"], "delivery_renew");
    let cp = actor.confirmed().checkpoint();
    let identity = mooring_protocol::checkpoint::verify(
        cp,
        mooring_protocol::checkpoint::Limits {
            decoded_bytes: 1572864,
            preimage_bytes: 2097152,
        },
    )
    .unwrap();
    let prepare = json!({"type":"validate_checkpoint_causal","request_id":"prepare-while-delivery-pending","scope":cp.scope,"selected":cp.versions,"checkpoint":{"encoding":"base64","state":cp.state,"digest":identity.content_digest()},"control_profile":"mooring.prepare-causal.v1"});
    mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&prepare).unwrap()).unwrap();
    assert_eq!(
        receive_json(&mut peer).1["type"],
        "checkpoint_validated_causal"
    );
    let response = serde_json::to_vec(&delivery_reply(&first)).unwrap();
    now.store(1900, Ordering::SeqCst); // Original ticket deadline2000 has NOT expired.
    let initial_reply = response.clone();
    let (peer, second) = drive_peer_control(&mut actor, peer, move |mut peer| {
        mooring_collab::write_frame(&mut peer, &initial_reply).unwrap();
        let (retry, _) = receive_json(&mut peer);
        assert_eq!(retry, first); // Same first-send and nonce on retry.
        let (second, second_value) = receive_json(&mut peer);
        assert_ne!(second_value["renewal_nonce"], request["renewal_nonce"]);
        (peer, second)
    });
    // The late response immediately starts the next request: due time was
    // firstsend250+interval250, NOT receive1900+interval250.
    now.store(2200, Ordering::SeqCst);
    let (mut peer, third) = drive_peer_control(&mut actor, peer, move |mut peer| {
        mooring_collab::write_frame(&mut peer, &response).unwrap(); // exactduplicate
        mooring_collab::write_frame(
            &mut peer,
            &serde_json::to_vec(&delivery_reply(&second)).unwrap(),
        )
        .unwrap();
        assert_eq!(receive_json(&mut peer).0, second);
        let (third, _) = receive_json(&mut peer);
        (peer, third)
    });
    assert_eq!(actor.confirmed().document().checkpoint().unwrap(), before);
    // A pending newer reply would fit its OWN first-send window, but cannot
    // resurrect the previous live gate once that gate actually expired.
    now.store(3900, Ordering::SeqCst); // second firstsend1900+2000, exclusiveexpiry
    mooring_collab::write_frame(
        &mut peer,
        &serde_json::to_vec(&delivery_reply(&third)).unwrap(),
    )
    .unwrap();
    drive_actor_to_control_error(&mut actor);
    let (document, _, originals) = actor.into_recovery();
    assert_eq!(document.document().checkpoint().unwrap(), before);
    assert!(originals.is_empty());
}
#[test]
fn actor_delivery_transplants_never_renew_current_gate_or_modify_document() {
    use std::sync::atomic::Ordering;
    for mode in [
        "scope",
        "session",
        "participant",
        "gate",
        "generation",
        "permission",
        "nonce",
        "duration",
    ] {
        let (mut actor, mut peer, now) = admitted_actor_with_clock(true, 60_000);
        let before = actor.confirmed().document().checkpoint().unwrap();
        now.store(250, Ordering::SeqCst);
        actor.step(&host(), Duration::ZERO).unwrap();
        let (request, _) = receive_json(&mut peer);
        let mut reply = delivery_reply(&request);
        match mode {
            "scope" => reply["scope"]["artifact_id"] = json!("other"),
            "session" => reply["session_id"] = json!("other"),
            "participant" => reply["participant_ref"] = json!("other"),
            "gate" => reply["gate_id"] = json!("other"),
            "generation" => reply["generation"] = json!("8"),
            "permission" => reply["permission_version"] = json!("4"),
            "nonce" => reply["renewal_nonce"] = json!("other"),
            "duration" => reply["duration_ms"] = json!("5001"),
            _ => unreachable!(),
        }
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&reply).unwrap()).unwrap();
        drive_actor_to_control_error(&mut actor);
        assert_eq!(
            actor.into_recovery().0.document().checkpoint().unwrap(),
            before,
            "{mode}"
        );
    }
}
#[test]
fn owner_permission_advance_cannot_refresh_old_participant_rights_or_pending_delivery() {
    use std::sync::atomic::Ordering;
    let (mut actor, mut peer, now) = admitted_actor_with_clock(true, 1000);
    let before = actor.confirmed().document().checkpoint().unwrap();
    now.store(500, Ordering::SeqCst);
    actor.step(&host(), Duration::ZERO).unwrap();
    let (owner_request, _) = receive_json(&mut peer);
    let (delivery, _) = receive_json(&mut peer);
    mooring_collab::write_frame(&mut peer, &renewal_reply(&owner_request, "4")).unwrap();
    actor.step(&host(), Duration::from_millis(20)).unwrap();
    assert_eq!(actor.known_owner_permission(), Counter(4));
    mooring_collab::write_frame(
        &mut peer,
        &serde_json::to_vec(&delivery_reply(&delivery)).unwrap(),
    )
    .unwrap();
    drive_actor_to_control_error(&mut actor);
    assert_eq!(
        actor.into_recovery().0.document().checkpoint().unwrap(),
        before
    );
}

#[test]
fn verified_saved_receipts_wait_for_a_new_authenticated_gate_and_keep_author_fifo() {
    use std::sync::atomic::Ordering;
    let (mut actor, peer, now) = admitted_actor_with_clock(true, 60_000);
    let mut checkpoint = actor.confirmed().checkpoint().clone();
    let mut targets = actor.confirmed().targets().to_vec();
    let initial = checkpoint.clone();
    let a = agent_change(&checkpoint.scope, "A", None, " A");
    let b = agent_change(&checkpoint.scope, "B", Some("A"), " B");
    let scope = checkpoint.scope.clone();
    let (peer, first, second) = drive_peer_control(&mut actor, peer, move |mut peer| {
        mooring_collab::write_frame(
            &mut peer,
            &serde_json::to_vec(&participant_input(&scope, &[a], "client-A")).unwrap(),
        )
        .unwrap();
        assert_eq!(receive_json(&mut peer).1["message"]["state"], "pending");
        let first = receive_json(&mut peer).1;
        mooring_collab::write_frame(
            &mut peer,
            &serde_json::to_vec(&participant_input(&scope, &[b], "client-B")).unwrap(),
        )
        .unwrap();
        assert_eq!(receive_json(&mut peer).1["message"]["state"], "pending");
        let second = receive_json(&mut peer).1;
        (peer, first, second)
    });
    assert_eq!(actor.confirmed().checkpoint(), &initial);
    let a_receipt = mock_commit(&first, &mut checkpoint, &mut targets);
    let b_receipt = mock_commit(&second, &mut checkpoint, &mut targets);
    now.store(2100, Ordering::SeqCst); // Old gate expired; no late renewal is sent.
    let mut peer = peer;
    mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&a_receipt).unwrap()).unwrap();
    mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&b_receipt).unwrap()).unwrap();
    let deadline = std::time::Instant::now() + Duration::from_secs(3);
    while actor.confirmed().checkpoint().revision != Counter(12) {
        actor.step(&host(), Duration::from_millis(20)).unwrap();
        assert!(std::time::Instant::now() < deadline);
    }
    let scope = checkpoint.scope.clone();
    let versions = checkpoint.versions.clone();
    let peer = drive_peer_control(&mut actor, peer, move |mut peer| {
        let opening = json!({"type":"session_participant_open","request_id":"new-private-open","control_profile":"mooring.session-causal.v1",
            "scope":scope,"session_id":"host-session","runtime_ref":"fresh-authenticated-child","fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
            "connection_ref":"new-connection","join":{"type":"session_join","request_id":"new-public-join","ticket":"NEW_AUTHENTICATED_HOST_TICKET","versions":versions}});
        // A newly authorized DIFFERENT participant may read through its own
        // status path, but must never inherit this author's retained receipts.
        let mut other = opening.clone();
        other["request_id"] = json!("other-private-open");
        other["connection_ref"] = json!("other-connection");
        other["join"]["request_id"] = json!("other-public-join");
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&other).unwrap()).unwrap();
        let other_ticket = receive_json(&mut peer).1;
        assert_eq!(other_ticket["type"], "ticket_validate");
        let other_reply = json!({"type":"ticket_validated","request_id":other_ticket["request_id"],"scope":other_ticket["scope"],"session_id":other_ticket["session_id"],
            "generation":"7","participant_ref":"other-participant","permission_version":"3","granted":{"read":true,"write":false},"versions":versions,
            "expires_in_ms":"2000","validation_nonce":other_ticket["validation_nonce"],"gate_id":"other-gate"});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&other_reply).unwrap()).unwrap();
        let other_joined = receive_json(&mut peer).1;
        assert_eq!(other_joined["message"]["type"], "session_joined");
        assert_eq!(other_joined["connection_ref"], "other-connection");
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&opening).unwrap()).unwrap();
        let ticket = receive_json(&mut peer).1;
        // No Saved was emitted to either the expired old gate or another
        // participant: the next frame is the SAME author's ticket validation.
        // must be the actual new ticket validation, followed by new Joined.
        assert_eq!(ticket["type"], "ticket_validate");
        let reply = json!({"type":"ticket_validated","request_id":ticket["request_id"],"scope":ticket["scope"],"session_id":ticket["session_id"],
            "generation":"7","participant_ref":"participant","permission_version":"3","granted":{"read":true,"write":false},"versions":versions,
            "expires_in_ms":"2000","validation_nonce":ticket["validation_nonce"],"gate_id":"new-gate"});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&reply).unwrap()).unwrap();
        let joined = receive_json(&mut peer).1;
        assert_eq!(joined["message"]["type"], "session_joined");
        for expected in [a_receipt, b_receipt] {
            let delivered = receive_json(&mut peer).1;
            assert_eq!(delivered["connection_ref"], "new-connection");
            assert_eq!(delivered["gate_id"], "new-gate");
            assert_eq!(delivered["message"], expected["receipt"]);
        }
        peer
    });
    assert_eq!(actor.confirmed().checkpoint(), &checkpoint);
    drop(peer);
}

#[test]
fn p3_five_second_pending_grant_cannot_reopen_current_gate_at_its_first_send_expiry() {
    use std::sync::atomic::Ordering;
    let (mut actor, peer, now) = admitted_actor_with_delivery_clock(true, 60_000, 5000);
    let checkpoint = actor.confirmed().checkpoint().clone();
    let native = actor.confirmed().document().native().unwrap();
    let ids = actor.confirmed().document().block_ids().unwrap();
    let scope = checkpoint.scope.clone();
    let a = agent_change(&scope, "A", None, " A");
    let b = agent_change(&scope, "B", Some("A"), " B");
    let peer = drive_peer_control(&mut actor, peer, move |mut peer| {
        for (change, id) in [(a, "A"), (b, "B")] {
            mooring_collab::write_frame(
                &mut peer,
                &serde_json::to_vec(&participant_input(&scope, &[change], id)).unwrap(),
            )
            .unwrap();
            assert_eq!(receive_json(&mut peer).1["message"]["state"], "pending");
            assert_eq!(receive_json(&mut peer).1["type"], "session_submit_batch");
        }
        peer
    });
    now.store(250, Ordering::SeqCst);
    let clock = now.clone();
    let (mut peer, second) = drive_peer_control(&mut actor, peer, move |mut peer| {
        let first = receive_json(&mut peer).0;
        let mut grant = delivery_reply(&first);
        grant["duration_ms"] = json!("5000");
        clock.store(260, Ordering::SeqCst);
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&grant).unwrap()).unwrap();
        clock.store(500, Ordering::SeqCst);
        // A cached retry can already be queued before the first reply is read.
        let second = loop {
            let request = receive_json(&mut peer).0;
            if request != first {
                break request;
            }
        };
        (peer, second)
    });
    // First grant owns [250,5250), never [reply-time,reply-time+5000).
    // The pending request's own [500,5500) cannot revive this expired gate.
    now.store(5250, Ordering::SeqCst);
    let mut late = delivery_reply(&second);
    late["duration_ms"] = json!("5000");
    mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&late).unwrap()).unwrap();
    drive_actor_to_control_error(&mut actor);
    let (confirmed, revision, originals) = actor.into_recovery();
    assert_eq!(confirmed.checkpoint(), &checkpoint);
    assert_eq!(confirmed.document().native().unwrap(), native);
    assert_eq!(confirmed.document().block_ids().unwrap(), ids);
    assert_eq!(revision, 10);
    assert_eq!(originals.len(), 2);
    assert_eq!(originals[0].value.operations[0].op_id, "A");
    assert_eq!(originals[1].value.operations[0].op_id, "B");
}

#[test]
fn session_leave_releases_expired_connection_without_reopening_its_old_gate() {
    let (mut actor, peer, now) = admitted_actor_with_clock(false, 60_000);
    now.store(2100, std::sync::atomic::Ordering::SeqCst);
    let checkpoint = actor.confirmed().checkpoint().clone();
    let scope = checkpoint.scope.clone();
    let versions = checkpoint.versions.clone();
    let peer = drive_peer_control(&mut actor, peer, move |mut peer| {
        let mut leave = participant_input(&scope, &[], "leave");
        leave["message"] = json!({"type":"session_leave","request_id":"leave","scope":scope,"session_id":"host-session"});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&leave).unwrap()).unwrap();
        let opening = json!({"type":"session_participant_open","request_id":"new-private-open","control_profile":"mooring.session-causal.v1",
            "scope":scope,"session_id":"host-session","runtime_ref":"fresh-authenticated-child","fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
            "connection_ref":"connection","join":{"type":"session_join","request_id":"new-public-join","ticket":"NEW_AUTHENTICATED_HOST_TICKET","versions":versions}});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&opening).unwrap()).unwrap();
        let ticket = receive_json(&mut peer).1;
        assert_eq!(ticket["type"], "ticket_validate");
        let reply = json!({"type":"ticket_validated","request_id":ticket["request_id"],"scope":ticket["scope"],"session_id":ticket["session_id"],
            "generation":"7","participant_ref":"participant","permission_version":"3","granted":{"read":true,"write":false},"versions":versions,
            "expires_in_ms":"2000","validation_nonce":ticket["validation_nonce"],"gate_id":"new-gate"});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&reply).unwrap()).unwrap();
        let joined = receive_json(&mut peer).1;
        assert_eq!(joined["message"]["type"], "session_joined");
        assert_eq!(joined["gate_id"], "new-gate");
        peer
    });
    assert_eq!(actor.confirmed().checkpoint(), &checkpoint);
    drop(peer);
}

#[test]
fn transplanted_leave_cannot_remove_connection_or_forget_original_pending_change() {
    let (mut actor, peer, _) = admitted_actor_with_clock(true, 60_000);
    let checkpoint = actor.confirmed().checkpoint().clone();
    let scope = checkpoint.scope.clone();
    let a = agent_change(&scope, "A", None, " A");
    let peer = drive_peer_control(&mut actor, peer, move |mut peer| {
        mooring_collab::write_frame(
            &mut peer,
            &serde_json::to_vec(&participant_input(&scope, &[a], "A")).unwrap(),
        )
        .unwrap();
        assert_eq!(receive_json(&mut peer).1["message"]["state"], "pending");
        assert_eq!(receive_json(&mut peer).1["type"], "session_submit_batch");
        peer
    });
    let mut leave = participant_input(&checkpoint.scope, &[], "leave");
    leave["gate_id"] = json!("foreign-gate");
    leave["message"] = json!({"type":"session_leave","request_id":"leave","scope":checkpoint.scope,"session_id":"host-session"});
    let mut peer = peer;
    mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&leave).unwrap()).unwrap();
    drive_actor_to_control_error(&mut actor);
    let (confirmed, revision, originals) = actor.into_recovery();
    assert_eq!(confirmed.checkpoint(), &checkpoint);
    assert_eq!(revision, 10);
    assert_eq!(originals.len(), 1);
    assert_eq!(originals[0].value.operations[0].op_id, "A");
}

#[test]
fn native_whole_public_groups_send_before_ack_and_forward_only_host_public_receipt() {
    let (mut actor, peer, _) = admitted_actor_selected(true, 60000, 5000, true);
    let scope = actor.confirmed().checkpoint().scope.clone();
    let versions = actor.confirmed().checkpoint().versions.clone();
    let mut peer = drive_peer_control(&mut actor, peer, move |mut peer| {
        let opening = json!({"type":"session_participant_open","request_id":"reader-open","control_profile":"mooring.session-causal.v1",
            "scope":scope,"session_id":"host-session","runtime_ref":"fresh-authenticated-child","fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
            "connection_ref":"reader-connection","join":{"type":"session_join","request_id":"reader-join","ticket":"HOST_READER_TICKET","versions":versions}});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&opening).unwrap()).unwrap();
        let ticket = receive_json(&mut peer).1;
        let reply = json!({"type":"ticket_validated","request_id":ticket["request_id"],"scope":ticket["scope"],"session_id":ticket["session_id"],
            "generation":"7","participant_ref":"reader","permission_version":"3","granted":{"read":true,"write":false},"versions":versions,
            "expires_in_ms":"5000","validation_nonce":ticket["validation_nonce"],"gate_id":"reader-gate"});
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&reply).unwrap()).unwrap();
        let joined = receive_json(&mut peer).1;
        assert_eq!(joined["message"]["type"], "session_joined");
        peer
    });
    let mut cp = actor.confirmed().checkpoint().clone();
    let mut targets = actor.confirmed().targets().to_vec();
    let scope = cp.scope.clone();
    let a1 = agent_change(&scope, "group-A1", None, " A1");
    let a2 = agent_change(&scope, "group-A2", Some("group-A1"), " A2");
    let b = agent_change(&scope, "group-B", Some("group-A2"), " B");
    let originals = [a1.clone(), a2.clone(), b.clone()];
    let (events, receive) = std::sync::mpsc::channel();
    let worker = std::thread::spawn(move || {
        for input in [
            participant_input(&scope, &[a1.clone(), a2.clone()], "public-A"),
            participant_input(&scope, std::slice::from_ref(&b), "public-B"),
        ] {
            mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&input).unwrap()).unwrap();
        }
        let mut batches = Vec::new();
        let mut pending = 0;
        while batches.len() < 2 {
            let (_, m) = receive_json(&mut peer);
            match m["type"].as_str().unwrap() {
                "session_submit_batch" => batches.push(m),
                "session_participant_output" => {
                    assert_eq!(m["message"]["type"], "changes_received");
                    pending += 1;
                }
                _ => panic!("unexpected control"),
            }
        }
        assert_eq!(pending, 2);
        assert_eq!(batches[0]["changes"], json!([a1, a2]));
        assert_eq!(batches[1]["changes"], json!([b]));
        assert_eq!(batches[1]["confirmed_basis"]["revision"], "10");
        events.send(false).unwrap(); // BOTH actual FD batches precede any ACK.
        for (batch, public_id, outer_id) in [
            (&batches[0], "public-A", "private-public-A"),
            (&batches[1], "public-B", "private-public-B"),
        ] {
            let mut response = mock_commit_selected(batch, &mut cp, &mut targets, true);
            let mut public = response["receipt"].clone();
            public["request_id"] = json!(public_id);
            response["public_receipt"] = public.clone();
            mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&response).unwrap())
                .unwrap();
            let initial_from = if public_id == "public-B" { 12 } else { 10 };
            let deltas = response["native_deltas"].as_array().unwrap();
            let mut positions = std::collections::BTreeMap::from([
                ("connection".to_owned(), (0usize, initial_from)),
                ("reader-connection".to_owned(), (0usize, initial_from)),
            ]);
            let mut own_receipts = 0;
            for _ in 0..deltas.len() * 2 + 1 {
                let (_, output) = receive_json(&mut peer);
                let connection = output["connection_ref"].as_str().unwrap();
                if output["message"]["type"] == "commit_result" {
                    assert_eq!(connection, "connection");
                    assert_eq!(output["request_id"], outer_id);
                    assert_eq!(output["message"], public, "host public receipt unchanged");
                    own_receipts += 1;
                    continue;
                }
                let (position, from) = positions.get_mut(connection).unwrap();
                let delta = &deltas[*position];
                let to = delta["revision"].as_str().unwrap().parse::<u64>().unwrap();
                assert_eq!(output["message"]["type"], "document_update");
                assert_eq!(output["request_id"], format!("actor-update:{to}"));
                assert_eq!(output["message"]["from_revision"], from.to_string());
                assert_eq!(output["message"]["to_revision"], to.to_string());
                assert_eq!(
                    output["message"]["receipt"], response["receipt"],
                    "unchanged full private receipt at every cut"
                );
                let data = mooring_wordcraft::document_update::decode(
                    output["message"]["data"].as_str().unwrap(),
                )
                .unwrap();
                assert_eq!(
                    mooring_protocol::boundary::encode_base64(&data.update, 262144).unwrap(),
                    delta["native_delta"]["update"]
                );
                *from = to;
                *position += 1;
            }
            assert_eq!(own_receipts, 1);
            assert!(positions.values().all(|(n, _)| *n == deltas.len()));
        }
        events.send(true).unwrap();
        peer
    });
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    loop {
        actor.step(&host(), Duration::from_millis(20)).unwrap();
        if let Ok(done) = receive.try_recv() {
            if done {
                break;
            }
            assert_eq!(actor.confirmed().checkpoint().revision, Counter(10));
        }
        assert!(std::time::Instant::now() < deadline);
    }
    worker.join().unwrap();
    assert_eq!(actor.confirmed().checkpoint().revision, Counter(13));
    let (_, _, remaining) = actor.into_recovery();
    assert!(remaining.is_empty());
    assert_eq!(originals[1].operations[0].dependencies, vec!["group-A1"]);
}

#[test]
fn native_public_receipt_or_cut_transplant_retains_originals_without_installation() {
    for mode in [
        "missing-public",
        "public-id",
        "public-membership",
        "cut-membership",
        "delta-origin",
    ] {
        let (mut actor, mut peer, _) = admitted_actor_selected(true, 60000, 5000, true);
        let initial = actor.confirmed().checkpoint().clone();
        let mut cp = initial.clone();
        let mut targets = actor.confirmed().targets().to_vec();
        let change = agent_change(&cp.scope, "original-A", None, " A");
        let original = change.clone();
        let input = participant_input(&cp.scope, &[change], "public-A");
        let worker = std::thread::spawn(move || {
            mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&input).unwrap()).unwrap();
            let batch = loop {
                let (_, m) = receive_json(&mut peer);
                if m["type"] == "session_submit_batch" {
                    break m;
                }
            };
            let mut response = mock_commit_selected(&batch, &mut cp, &mut targets, true);
            let mut public = response["receipt"].clone();
            public["request_id"] = json!("public-A");
            response["public_receipt"] = public;
            match mode {
                "missing-public" => {
                    response.as_object_mut().unwrap().remove("public_receipt");
                }
                "public-id" => response["public_receipt"]["request_id"] = json!("another-public"),
                "public-membership" => {
                    response["public_receipt"]["changes"][0]["change_id"] = json!("another-change")
                }
                "cut-membership" => {
                    response["native_deltas"][0]["membership_hash"] =
                        json!(format!("sha256:{}", "0".repeat(64)))
                }
                "delta-origin" => {
                    response["native_deltas"][0]["native_delta"]["allocation_origins"] = json!([])
                }
                _ => unreachable!(),
            }
            mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&response).unwrap())
                .unwrap();
            peer
        });
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        loop {
            if actor.step(&host(), Duration::from_millis(20)).is_err() {
                break;
            }
            assert!(std::time::Instant::now() < deadline, "{mode}");
        }
        worker.join().unwrap();
        let (confirmed, revision, originals) = actor.into_recovery();
        assert_eq!(confirmed.checkpoint(), &initial, "{mode}");
        assert_eq!(revision, 10, "{mode}");
        assert_eq!(originals.len(), 1, "{mode}");
        assert_eq!(originals[0].value.as_ref(), &original, "{mode}");
    }
}
