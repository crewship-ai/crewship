use mooring_core::batch::{Batcher, Change, Limits, Rejection, Tick};
use std::{num::NonZeroUsize, time::Duration};
fn n(v: usize) -> NonZeroUsize {
    NonZeroUsize::new(v).unwrap()
}
fn tick(v: u64) -> Tick {
    Tick(Duration::from_millis(v))
}
fn change(v: usize, ops: usize, bytes: usize) -> Change<usize> {
    Change {
        value: v,
        operations: n(ops),
        bytes: n(bytes),
    }
}
fn limits() -> Limits {
    Limits {
        max_wait: Duration::from_millis(100),
        max_operations: n(4),
        max_bytes: n(20),
    }
}

#[test]
fn d1_low_load_has_first_operation_deadline() {
    let mut b = Batcher::new(limits());
    assert!(b.push(tick(10), change(1, 1, 2)).unwrap().is_empty());
    assert_eq!(b.deadline(), Some(tick(110)));
    assert!(b.push(tick(100), change(2, 1, 2)).unwrap().is_empty());
    assert_eq!(b.deadline(), Some(tick(110)));
    assert!(b.take_due(tick(109)).unwrap().is_none());
    assert_eq!(b.take_due(tick(110)).unwrap().unwrap().len(), 2);
    assert_eq!(b.deadline(), None);
}

#[test]
fn d1_each_capacity_boundary_emits_immediately() {
    for c in [change(1, 4, 1), change(1, 1, 20)] {
        let mut b = Batcher::new(limits());
        assert_eq!(b.push(tick(0), c).unwrap()[0][0].value, 1);
        assert_eq!(b.deadline(), None);
    }
}

#[test]
fn p42_overflow_flushes_without_splitting_atomic_changes() {
    for (a, c) in [
        (change(1, 3, 2), change(2, 2, 2)),
        (change(1, 1, 15), change(2, 1, 10)),
    ] {
        let mut b = Batcher::new(limits());
        b.push(tick(0), a).unwrap();
        let emitted = b.push(tick(20), c).unwrap();
        assert_eq!(emitted.len(), 1);
        assert_eq!(emitted[0].len(), 1);
        assert_eq!(emitted[0][0].value, 1);
        assert_eq!(b.deadline(), Some(tick(120)));
        assert_eq!(b.take().unwrap()[0].value, 2);
    }
}

#[test]
fn p42_oversized_change_returns_intent_without_draining_pending_work() {
    for (bad, reason) in [
        (change(2, 5, 2), Rejection::TooManyOperations),
        (change(2, 1, 21), Rejection::TooManyBytes),
    ] {
        let mut b = Batcher::new(limits());
        b.push(tick(0), change(1, 1, 1)).unwrap();
        let rejected = b.push(tick(100), bad).unwrap_err();
        assert_eq!(rejected.reason, reason);
        assert_eq!(rejected.change.value, 2);
        assert_eq!(b.take_due(tick(100)).unwrap().unwrap()[0].value, 1);
    }
}

#[test]
fn d1_elapsed_deadline_flushes_before_new_full_change() {
    let mut b = Batcher::new(limits());
    b.push(tick(0), change(1, 1, 1)).unwrap();
    let batches = b.push(tick(100), change(2, 4, 20)).unwrap();
    assert_eq!(batches.len(), 2);
    assert_eq!(batches[0][0].value, 1);
    assert_eq!(batches[1][0].value, 2);
}

#[test]
fn invalid_clock_preserves_pending_and_new_intents() {
    let mut b = Batcher::new(limits());
    b.push(tick(10), change(1, 1, 1)).unwrap();
    assert_eq!(
        b.push(tick(9), change(2, 1, 1)).unwrap_err().reason,
        Rejection::ClockReversed
    );
    assert_eq!(b.take_due(tick(9)).unwrap_err(), Rejection::ClockReversed);
    assert_eq!(b.deadline(), Some(tick(110)));
    assert_eq!(
        b.push(Tick(Duration::MAX), change(2, 1, 1))
            .unwrap_err()
            .reason,
        Rejection::DeadlineOverflow
    );
    assert_eq!(b.take_due(tick(110)).unwrap().unwrap()[0].value, 1);
}

#[test]
fn zero_wait_sends_immediately_and_manual_flush_is_empty_after_drain() {
    let mut l = limits();
    l.max_wait = Duration::ZERO;
    let mut b = Batcher::new(l);
    assert_eq!(b.push(tick(0), change(1, 1, 1)).unwrap().len(), 1);
    assert!(b.take().is_none());
}
