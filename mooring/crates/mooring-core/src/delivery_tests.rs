use crate::{
    batch::Tick,
    delivery::{DeliveryError, ReadLease},
};
use mooring_protocol::{
    boundary::{Durability, Transport},
    grant::PendingGrant,
    Counter, Fence, Versions,
};
use std::time::Duration;

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
fn lease(read: bool) -> Result<ReadLease, DeliveryError> {
    let request = br#"{"type":"ticket_validate","request_id":"q","scope":{"application_id":"a","workspace_id":"w","artifact_id":"d"},"session_id":"s","fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"ticket":"opaque-ticket","validation_nonce":"fresh-host-validated-ticket"}"#;
    let pending = PendingGrant::ticket(
        request,
        Versions {
            protocol_version: "0.4.0".into(),
            model_version: "1.0.0".into(),
            engine_version: "1.0.0".into(),
        },
        Counter(5),
        at(1000).0,
        Duration::from_secs(2),
    )
    .unwrap();
    let reply = format!(
        r#"{{"type":"ticket_validated","request_id":"q","scope":{{"application_id":"a","workspace_id":"w","artifact_id":"d"}},"session_id":"s","generation":"7","participant_ref":"host-p","permission_version":"5","granted":{{"read":{read}}},"versions":{{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"1.0.0"}},"expires_in_ms":"2000","validation_nonce":"fresh-host-validated-ticket","gate_id":"fresh-host-gate"}}"#
    );
    let ticket = pending
        .validate_reply(reply.as_bytes(), &host(), at(1100).0)
        .unwrap();
    ReadLease::admit(
        ticket,
        Fence {
            generation: Counter(7),
            token: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx".into(),
        },
        Duration::from_secs(2),
        at(1100),
    )
}
fn reply(request: &[u8], duration: u64) -> Vec<u8> {
    let raw = std::str::from_utf8(request).unwrap();
    let nonce = raw
        .split("\"request_id\":\"")
        .nth(1)
        .unwrap()
        .split('"')
        .next()
        .unwrap();
    format!(r#"{{"type":"delivery_grant","request_id":"{nonce}","scope":{{"application_id":"a","workspace_id":"w","artifact_id":"d"}},"session_id":"s","participant_ref":"host-p","generation":"7","permission_version":"5","renewal_nonce":"{nonce}","duration_ms":"{duration}","gate_id":"fresh-host-gate"}}"#).into_bytes()
}

#[test]
fn lost_reply_retries_keep_original_deadline_and_expiry_is_permanent() {
    let mut lease = lease(true).unwrap();
    let request = lease.begin(at(1200)).unwrap().to_vec();
    assert_eq!(lease.retry_bytes().unwrap(), request);
    assert_eq!(lease.begin(at(1300)), Err(DeliveryError::Pending));
    lease
        .accept(&reply(&request, 1000), &host(), at(2100))
        .unwrap();
    assert!(lease.may_deliver(at(2199)));
    assert!(!lease.may_deliver(at(2200)));
    assert_eq!(lease.begin(at(2201)), Err(DeliveryError::Closed));
    assert_eq!(
        lease.accept(&reply(&request, 1000), &host(), at(2202)),
        Err(DeliveryError::Closed)
    );
}

#[test]
fn controller_generates_distinct_nonces_and_old_reply_cannot_replace_pending() {
    let mut lease = lease(true).unwrap();
    let first = lease.begin(at(1200)).unwrap().to_vec();
    lease
        .accept(&reply(&first, 1500), &host(), at(1300))
        .unwrap();
    let second = lease.begin(at(1400)).unwrap().to_vec();
    assert_ne!(first, second);
    assert!(lease
        .accept(&reply(&first, 1500), &host(), at(1500))
        .is_err());
    assert_eq!(lease.retry_bytes().unwrap(), second);
    assert!(lease
        .accept(&reply(&second, 1500), &Transport::Participant, at(1501))
        .is_err());
    assert_eq!(lease.retry_bytes().unwrap(), second);
    let different_host = Transport::AuthenticatedHost {
        issuer: "another-host", durabilities: &[Durability::Node], required: Durability::Node,
    };
    assert_eq!(lease.accept(&reply(&second, 1500), &different_host, at(1502)),
        Err(DeliveryError::Grant(mooring_protocol::boundary::BoundaryError::Forbidden)));
    assert_eq!(lease.retry_bytes().unwrap(), second);
    lease
        .accept(&reply(&second, 1500), &host(), at(1600))
        .unwrap();
    assert!(lease.may_deliver(at(2899)));
    assert!(!lease.may_deliver(at(2900)));
}

#[test]
fn revocation_clock_regression_and_expired_unpolled_gate_cannot_reopen() {
    let mut revoked = lease(true).unwrap();
    let request = revoked.begin(at(1200)).unwrap().to_vec();
    revoked.revoke();
    assert_eq!(
        revoked.accept(&reply(&request, 2000), &host(), at(1300)),
        Err(DeliveryError::Closed)
    );
    let mut reversed = lease(true).unwrap();
    reversed.begin(at(1200)).unwrap();
    assert!(!reversed.may_deliver(at(1199)));
    assert_eq!(reversed.begin(at(1300)), Err(DeliveryError::Closed));
    let mut expired = lease(true).unwrap();
    let request = expired.begin(at(2900)).unwrap().to_vec();
    // Reply itself would remain valid until 4900, but the OLD gate expired at 3000.
    assert_eq!(
        expired.accept(&reply(&request, 2000), &host(), at(3000)),
        Err(DeliveryError::Closed)
    );
    assert!(!expired.may_deliver(at(3001)));
}

#[test]
fn read_denied_ticket_never_creates_read_gate() {
    assert!(matches!(lease(false), Err(DeliveryError::Binding)));
}

#[test]
fn nonce_sequence_exhaustion_closes_without_reusing_any_nonce() {
    let mut lease = lease(true).unwrap();
    lease.sequence = u64::MAX;
    assert!(lease.may_deliver(at(1200)));
    assert_eq!(lease.begin(at(1201)), Err(DeliveryError::Closed));
    assert_eq!(lease.retry_bytes(), Err(DeliveryError::Closed));
    assert!(!lease.may_deliver(at(1202)));
}

#[test]
fn expired_pending_reply_is_fully_bound_without_renewing_or_reissuing() {
    for arrival in [3000, 4900] {
        let mut lease = lease(true).unwrap();
        let request = lease.begin(at(2900)).unwrap().to_vec();
        let response = reply(&request, 2000);
        assert!(!lease.may_deliver(at(3000)));
        assert!(lease.is_expired());
        assert_eq!(
            lease.accept(&response, &host(), at(arrival)),
            Err(DeliveryError::Closed)
        );
        lease
            .accept_expired_reply(&response, &host(), at(arrival))
            .unwrap();
        assert!(lease.is_expired());
        assert!(lease.pending.is_none());
        assert!(!lease.may_deliver(at(arrival)));
        assert_eq!(lease.begin(at(arrival)), Err(DeliveryError::Closed));
        assert_eq!(lease.retry_bytes(), Err(DeliveryError::Closed));
        // The original request was consumed, never recreated for another reply.
        assert_eq!(
            lease.accept_expired_reply(&response, &host(), at(arrival)),
            Err(DeliveryError::Binding)
        );
    }
}

#[test]
fn expired_reply_window_never_masks_foreign_or_malformed_pending_binding() {
    for field in [
        "scope",
        "request_id",
        "session_id",
        "participant_ref",
        "generation",
        "permission_version",
        "gate_id",
        "renewal_nonce",
        "duration_ms",
        "unknown",
    ] {
        let mut lease = lease(true).unwrap();
        let request = lease.begin(at(2900)).unwrap().to_vec();
        assert!(!lease.may_deliver(at(3000)));
        let response = String::from_utf8(reply(&request, 2000)).unwrap();
        let raw = match field {
            "scope" => response.replace("\"artifact_id\":\"d\"", "\"artifact_id\":\"foreign\""),
            "duration_ms" => response.replace("\"duration_ms\":\"2000\"", "\"duration_ms\":\"2001\""),
            "unknown" => response.replacen('{', "{\"unknown\":\"foreign\",", 1),
            _ => {
                let prefix = format!("\"{field}\":\"");
                let value_start = response.find(&prefix).unwrap() + prefix.len();
                let value_end = value_start + response[value_start..].find('"').unwrap();
                let mut forged = response.clone();
                forged.replace_range(value_start..value_end, if matches!(field, "generation" | "permission_version") { "999" } else { "foreign" });
                forged
            }
        }.into_bytes();
        assert!(
            lease.accept_expired_reply(&raw, &host(), at(4900)).is_err(),
            "{field}"
        );
        assert_eq!(
            lease.pending.as_ref().unwrap().grant.bytes_for_retry(),
            request
        );
        assert!(!lease.may_deliver(at(4900)));
        assert_eq!(lease.begin(at(4900)), Err(DeliveryError::Closed));
    }
}

#[test]
fn expired_reply_keeps_original_issuer_and_clock_failure_fatal() {
    let mut expired = lease(true).unwrap();
    let request = expired.begin(at(2900)).unwrap().to_vec();
    assert!(!expired.may_deliver(at(3000)));
    let response = reply(&request, 2000);
    for source in [
        Transport::Participant,
        Transport::AuthenticatedHost {
            issuer: "foreign",
            durabilities: &[Durability::Node],
            required: Durability::Node,
        },
    ] {
        assert_eq!(
            expired.accept_expired_reply(&response, &source, at(3001)),
            Err(DeliveryError::Grant(
                mooring_protocol::boundary::BoundaryError::Forbidden
            ))
        );
        assert!(expired.pending.is_some());
    }
    // A later denial observation is retained; no older Tick can classify a reply.
    assert!(!expired.may_deliver(at(3100)));
    assert_eq!(
        expired.accept_expired_reply(&response, &host(), at(3099)),
        Err(DeliveryError::Lease(
            crate::lease::LeaseError::ClockReversed
        ))
    );
    assert!(!expired.is_expired());
    assert!(expired.pending.is_none());
    assert!(expired
        .accept_expired_reply(&response, &host(), at(3200))
        .is_err());

    let mut reversed = lease(true).unwrap();
    let request = reversed.begin(at(1200)).unwrap().to_vec();
    assert!(!reversed.may_deliver(at(1199)));
    assert!(!reversed.is_expired());
    assert!(reversed
        .accept_expired_reply(&reply(&request, 2000), &host(), at(1300))
        .is_err());
    let mut revoked = lease(true).unwrap();
    let request = revoked.begin(at(1200)).unwrap().to_vec();
    revoked.revoke();
    assert!(!revoked.is_expired());
    assert!(revoked
        .accept_expired_reply(&reply(&request, 2000), &host(), at(1300))
        .is_err());
}

#[test]
fn failed_expired_reply_observes_time_before_a_later_reversed_reply() {
    for foreign in [false, true] {
        let mut expired = lease(true).unwrap();
        let request = expired.begin(at(2900)).unwrap().to_vec();
        let response = reply(&request, 2000);
        assert!(!expired.may_deliver(at(3000)));
        let source = if foreign {
            Transport::Participant
        } else {
            host()
        };
        let failed = if foreign { response.as_slice() } else { b"malformed" };
        assert!(expired.accept_expired_reply(failed, &source, at(4000)).is_err());
        assert!(expired.pending.is_some());
        assert_eq!(
            expired.accept_expired_reply(&response, &host(), at(3500)),
            Err(DeliveryError::Lease(crate::lease::LeaseError::ClockReversed)),
            "failed observation must still advance time, foreign={foreign}"
        );
        assert!(!expired.is_expired());
        assert!(expired.pending.is_none());
        assert!(!expired.may_deliver(at(4001)));
    }
}
