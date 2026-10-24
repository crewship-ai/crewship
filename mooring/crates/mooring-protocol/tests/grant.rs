use mooring_protocol::{
    boundary::{BoundaryError, Durability, Transport},
    grant::PendingGrant,
    wire::WireError,
    Counter,
};
use serde_json::Value;
use std::time::Duration;
fn fixture() -> Value {
    serde_json::from_str(include_str!(
        "../../../protocol/grant-bindings-fixtures.json"
    ))
    .unwrap()
}
fn expected_error(label: &str) -> BoundaryError {
    match label {
        "Forbidden" => BoundaryError::Forbidden,
        "StaleOwner" => BoundaryError::StaleOwner,
        "PermissionRevoked" => BoundaryError::PermissionRevoked,
        "VersionMismatch" => BoundaryError::VersionMismatch,
        "Expired" => BoundaryError::Expired,
        "Clock" => BoundaryError::Clock,
        "Capacity" => BoundaryError::Capacity,
        "Wire" => BoundaryError::Wire(WireError::InvalidShape),
        _ => panic!("unknown expected error"),
    }
}
fn pending(kind: &str, fixture: &Value, sent_at: Duration) -> PendingGrant {
    let raw = serde_json::to_vec(&fixture["requests"][kind]).unwrap();
    let context = &fixture["context"];
    let maximum = Duration::from_millis(context["maximum_duration_ms"].as_u64().unwrap());
    match kind {
        "delivery" => PendingGrant::delivery(&raw, sent_at, maximum),
        "owner_renew" => PendingGrant::owner_renew(
            &raw,
            context["runtime_ref"].as_str().unwrap(),
            sent_at,
            maximum,
        ),
        "owner_acquire" => PendingGrant::owner_acquire(
            &raw,
            Counter(
                context["minimum_generation"]
                    .as_str()
                    .unwrap()
                    .parse()
                    .unwrap(),
            ),
            sent_at,
            maximum,
        ),
        "ticket" => PendingGrant::ticket(
            &raw,
            serde_json::from_value(context["versions"].clone()).unwrap(),
            Counter(
                context["minimum_permission"]
                    .as_str()
                    .unwrap()
                    .parse()
                    .unwrap(),
            ),
            sent_at,
            maximum,
        ),
        _ => panic!("unknown pending kind"),
    }
    .unwrap_or_else(|error| panic!("{kind}: {error:?}"))
}
#[test]
fn shared_host_grant_binding_and_first_send_deadline_cases() {
    let fixture = fixture();
    let context = &fixture["context"];
    let host = Transport::AuthenticatedHost {
        issuer: "crewship",
        durabilities: &[Durability::Node],
        required: Durability::Node,
    };
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 49);
    for case in cases {
        let kind = case["kind"].as_str().unwrap();
        let request = pending(
            kind,
            &fixture,
            Duration::from_millis(context["first_send_ms"].as_u64().unwrap()),
        );
        let mut reply = fixture["replies"][kind].clone();
        for mutation in case["mutations"].as_array().unwrap() {
            *reply
                .pointer_mut(mutation["path"].as_str().unwrap())
                .unwrap() = mutation["value"].clone();
        }
        let transport = if case["transport"] == "participant" {
            &Transport::Participant
        } else {
            &host
        };
        let received = case
            .get("received_at_ms")
            .unwrap_or(&context["received_at_ms"])
            .as_u64()
            .unwrap();
        let bytes = request.bytes_for_retry().to_vec();
        let result = request.validate_reply(
            &serde_json::to_vec(&reply).unwrap(),
            transport,
            Duration::from_millis(received),
        );
        if let Some(error) = case["expected_error"].as_str() {
            assert_eq!(result.unwrap_err(), expected_error(error), "{}", case["id"]);
        } else {
            let checked = result.unwrap_or_else(|error| panic!("{}: {error:?}", case["id"]));
            assert_eq!(
                checked.expires_at(),
                Duration::from_millis(2000),
                "{}",
                case["id"]
            );
            assert_eq!(checked.duration(), Duration::from_millis(1000));
            assert_eq!(checked.value(), &reply);
        }
        assert_eq!(request.bytes_for_retry(), bytes);
    }
}

#[test]
fn lost_response_same_nonce_retry_cannot_rebind_send_time_or_grant_late_permission() {
    let fixture = fixture();
    let request = pending("delivery", &fixture, Duration::from_millis(1000));
    let host = Transport::AuthenticatedHost {
        issuer: "crewship",
        durabilities: &[Durability::Node],
        required: Durability::Node,
    };
    let original = request.bytes_for_retry().to_vec();
    let reply = serde_json::to_vec(&fixture["replies"]["delivery"]).unwrap();
    // Initial host response was lost. Resend identical nonce/body much later;
    // the replayed full-duration host response still expires at original+1000.
    assert_eq!(request.bytes_for_retry(), original);
    assert_eq!(
        request
            .validate_reply(&reply, &host, Duration::from_millis(1900))
            .unwrap()
            .expires_at(),
        Duration::from_millis(2000)
    );
    assert_eq!(
        request
            .validate_reply(&reply, &host, Duration::from_millis(2000))
            .unwrap_err(),
        BoundaryError::Expired
    );
    assert_eq!(
        request
            .validate_reply(&reply, &host, Duration::from_millis(2100))
            .unwrap_err(),
        BoundaryError::Expired
    );
    assert_eq!(
        request
            .validate_reply(b"invalid", &Transport::Runtime, Duration::from_millis(1100))
            .unwrap_err(),
        BoundaryError::Forbidden
    );
    let overflow = pending("delivery", &fixture, Duration::MAX);
    assert_eq!(
        overflow
            .validate_reply(&reply, &host, Duration::MAX)
            .unwrap_err(),
        BoundaryError::Clock
    );
}
