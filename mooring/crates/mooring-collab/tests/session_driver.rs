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
        mooring_collab::write_frame(&mut peer, &open()).unwrap();
        let acquire = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        let mut granted: Value = serde_json::from_slice(&grant(&acquire)).unwrap();
        granted["lease_duration_ms"] = json!("1000");
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&granted).unwrap()).unwrap();
        mooring_collab::write_frame(&mut peer, &restored).unwrap();
        for _ in 0..2 {
            mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        }
        peer
    });
    let driver = Driver::from_negotiated_inherited_stream(child, &host(), move || {
        Ok(at(clock.load(Ordering::SeqCst)))
    })
    .unwrap();
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
