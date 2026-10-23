use mooring_core::{
    batch::Tick,
    lease::{boottime, DeliveryGate, LeaseError},
};
use std::time::Duration;
fn at(ms: u64) -> Tick {
    Tick(Duration::from_millis(ms))
}
fn ttl() -> Duration {
    Duration::from_secs(5)
}

#[test]
fn p43_delayed_response_never_extends_send_time_deadline() {
    let mut gate = DeliveryGate::default();
    assert!(!gate.may_deliver(at(0)));
    let request = gate.request(at(10)).unwrap();
    gate.renew(request, ttl(), at(4000)).unwrap();
    assert!(gate.may_deliver(at(5009)));
    assert!(!gate.may_deliver(at(5010)));
}
#[test]
fn p43_response_arriving_after_expiry_grants_nothing() {
    let mut gate = DeliveryGate::default();
    let request = gate.request(at(0)).unwrap();
    assert_eq!(
        gate.renew(request, ttl(), at(5000)),
        Err(LeaseError::ExpiredResponse)
    );
    assert!(!gate.may_deliver(at(5000)));
}
#[test]
fn p43_lost_revoke_message_and_process_suspend_stop_delivery() {
    let mut gate = DeliveryGate::default();
    let request = gate.request(at(0)).unwrap();
    gate.renew(request, ttl(), at(1)).unwrap();
    assert!(gate.may_deliver(at(2)));
    // The clock advances across suspension even with no revocation message.
    assert!(!gate.may_deliver(at(60_000)));
}
#[test]
fn p43_revoke_fences_in_flight_and_future_requests_permanently() {
    let mut gate = DeliveryGate::default();
    let request = gate.request(at(0)).unwrap();
    gate.revoke();
    assert_eq!(gate.renew(request, ttl(), at(1)), Err(LeaseError::Closed));
    assert_eq!(gate.request(at(1)), Err(LeaseError::Closed));
    assert!(!gate.may_deliver(at(1)));
}
#[test]
fn p43_reordered_or_replayed_responses_cannot_grant_permission() {
    let mut gate = DeliveryGate::default();
    let old = gate.request(at(0)).unwrap();
    let current = gate.request(at(10)).unwrap();
    assert_eq!(
        gate.renew(old, ttl(), at(11)),
        Err(LeaseError::StaleResponse)
    );
    gate.renew(current, ttl(), at(12)).unwrap();
    assert_eq!(
        gate.renew(current, ttl(), at(13)),
        Err(LeaseError::StaleResponse)
    );
    assert!(!gate.may_deliver(at(5010)));
}
#[test]
fn p43_clock_regression_closes_gate_and_arithmetic_overflow_grants_nothing() {
    let mut gate = DeliveryGate::default();
    let request = gate.request(at(10)).unwrap();
    gate.renew(request, ttl(), at(20)).unwrap();
    assert!(!gate.may_deliver(at(19)));
    assert_eq!(gate.request(at(20)), Err(LeaseError::Closed));
    let mut gate = DeliveryGate::default();
    let request = gate.request(at(10)).unwrap();
    assert_eq!(
        gate.renew(request, Duration::MAX, at(11)),
        Err(LeaseError::DeadlineOverflow)
    );
    assert!(!gate.may_deliver(at(11)));
}
#[test]
#[cfg(all(target_os = "linux", target_pointer_width = "64"))]
fn p43_native_boottime_is_available_and_monotonic() {
    let a = boottime().unwrap();
    let b = boottime().unwrap();
    assert!(b >= a);
}
