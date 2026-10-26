#![cfg(target_os = "linux")]
use mooring_core::{
    actor::{Actor, Failure, StartError},
    batch::Change,
    schedule::EnqueueReason,
    session::Error,
};
use std::{
    num::NonZeroUsize,
    time::{Duration, Instant},
};
fn n(v: usize) -> NonZeroUsize {
    NonZeroUsize::new(v).unwrap()
}
fn change(value: u32, operations: usize, bytes: usize) -> Change<u32> {
    Change {
        value,
        operations: n(operations),
        bytes: n(bytes),
    }
}
#[test]
fn actual_timer_sends_b_while_a_has_no_durable_reply() {
    let mut actor = Actor::new("confirmed", 0, n(1024)).unwrap();
    let start = Instant::now();
    actor.enqueue(change(1, 1, 8)).unwrap();
    let a = actor.dispatch(Duration::from_secs(2)).unwrap().unwrap();
    let a_id = a.sequence;
    assert_eq!(a.batch.body[0].value, 1);
    assert_eq!(a.batch.bytes.get(), 1032);
    let a_elapsed = start.elapsed();
    assert!(a_elapsed >= Duration::from_millis(70));
    actor.enqueue(change(2, 1, 8)).unwrap();
    let b = actor.dispatch(Duration::from_secs(2)).unwrap().unwrap();
    let b_id = b.sequence;
    assert_eq!(b.batch.body[0].value, 2);
    let b_elapsed = start.elapsed();
    assert!(b_elapsed >= Duration::from_millis(140));
    assert_eq!(actor.confirmed(), (&"confirmed", 0));
    // The host acknowledgement is deliberately withheld until BOTH sends.
    actor
        .settle_host_response(a_id, Some(("host-a", 1)))
        .unwrap();
    assert_eq!(actor.confirmed(), (&"host-a", 1));
    actor.settle_host_response(b_id, None).unwrap();
    assert_eq!(actor.confirmed(), (&"host-a", 1));
    eprintln!("actual low-load timer: A={a_elapsed:?}, B={b_elapsed:?}; ACK after both");
}
#[test]
fn full_outstanding_and_unsent_caps_retain_all_atomic_work_on_death() {
    let mut actor = Actor::new("confirmed", 8, n(1024)).unwrap();
    for value in 0..4 {
        actor
            .enqueue(change(value, 256, 256 * 1024 - 1024))
            .unwrap();
        let next = actor.dispatch(Duration::from_secs(2)).unwrap().unwrap();
        assert_eq!(next.batch.bytes.get(), 256 * 1024);
        assert_eq!(next.batch.body[0].value, value);
    }
    // Four maximum outstanding batches plus one bounded UNSENT batch.
    actor.enqueue(change(4, 256, 256 * 1024 - 1024)).unwrap();
    assert!(actor.dispatch(Duration::ZERO).unwrap().is_none());
    let rejected = actor.enqueue(change(99, 1, 1)).unwrap_err();
    assert_eq!(rejected.reason, EnqueueReason::Backpressure);
    assert_eq!(rejected.change.value, 99);
    let (document, revision, originals) = actor.into_recovery();
    assert_eq!((document, revision), ("confirmed", 8));
    assert_eq!(
        originals.iter().map(|c| c.value).collect::<Vec<_>>(),
        [0, 1, 2, 3, 4]
    );
}
#[test]
fn rejected_confirmation_closes_timer_and_keeps_unsent_and_outstanding() {
    let mut actor = Actor::new("confirmed", 8, n(1024)).unwrap();
    actor.enqueue(change(1, 256, 1)).unwrap();
    let id = actor
        .dispatch(Duration::from_secs(2))
        .unwrap()
        .unwrap()
        .sequence;
    actor.enqueue(change(2, 1, 1)).unwrap();
    assert_eq!(
        actor
            .settle_host_response(id + 1, Some(("wrong", 9)))
            .unwrap_err(),
        Failure::State(Error::UnexpectedConfirmation)
    );
    assert_eq!(
        actor.enqueue(change(3, 1, 1)).unwrap_err().reason,
        EnqueueReason::Closed
    );
    assert_eq!(
        actor.dispatch(Duration::ZERO).unwrap_err(),
        Failure::State(Error::Closed)
    );
    let (document, revision, originals) = actor.into_recovery();
    assert_eq!((document, revision), ("confirmed", 8));
    assert_eq!(
        originals.iter().map(|c| c.value).collect::<Vec<_>>(),
        [1, 2]
    );
}
#[test]
fn settlement_releases_one_complete_slot_without_waiting_for_other_acks() {
    let mut actor = Actor::new("confirmed", 0, n(1024)).unwrap();
    let mut first = None;
    for value in 0..4 {
        actor.enqueue(change(value, 256, 1)).unwrap();
        let id = actor
            .dispatch(Duration::from_secs(2))
            .unwrap()
            .unwrap()
            .sequence;
        first.get_or_insert(id);
    }
    actor.enqueue(change(4, 256, 1)).unwrap();
    assert!(actor.dispatch(Duration::ZERO).unwrap().is_none());
    actor.settle_host_response(first.unwrap(), None).unwrap();
    assert_eq!(
        actor
            .dispatch(Duration::from_secs(2))
            .unwrap()
            .unwrap()
            .batch
            .body[0]
            .value,
        4
    );
}
#[test]
fn envelope_reserve_cannot_hide_an_oversize_atomic_change() {
    assert!(matches!(
        Actor::<(), u32>::new((), 0, n(256 * 1024)),
        Err(StartError::EnvelopeReserve)
    ));
    let actor = Actor::new((), 0, n(1024)).unwrap();
    let refused = actor.enqueue(change(1, 1, 256 * 1024 - 1023)).unwrap_err();
    assert_eq!(refused.change.value, 1);
    assert!(matches!(refused.reason, EnqueueReason::Batch(_)));
}

#[test]
fn waking_timer_interrupts_park_without_polling_and_rejects_weakened_maximum() {
    use mooring_core::schedule::native::ReadyWake;
    let wake = ReadyWake::current();
    for duration in [Duration::ZERO, Duration::from_millis(101)] {
        assert!(
            Actor::<_, u32>::new_waking("confirmed", 0, n(100), duration, wake.clone()).is_err()
        );
    }
    let mut actor =
        Actor::new_waking("confirmed", 0, n(100), Duration::from_millis(40), wake).unwrap();
    let started = Instant::now();
    actor.enqueue(change(7, 1, 1)).unwrap();
    let deadline = started + Duration::from_secs(2);
    loop {
        if let Some(dispatch) = actor.dispatch(Duration::ZERO).unwrap() {
            assert_eq!(dispatch.batch.body[0].value, 7);
            break;
        }
        assert!(Instant::now() < deadline);
        std::thread::park_timeout(deadline.saturating_duration_since(Instant::now()));
    }
    assert!(started.elapsed() >= Duration::from_millis(20));
    assert!(started.elapsed() < Duration::from_secs(1));
    assert_eq!(actor.confirmed(), (&"confirmed", 0));
    assert_eq!(actor.into_recovery().2[0].value, 7);
}
