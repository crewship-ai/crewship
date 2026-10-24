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
