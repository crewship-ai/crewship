#![cfg(target_os = "linux")]
use mooring_core::{
    actor::Actor,
    batch::{Change, Limits, Rejection},
    schedule::{
        native::{Clock, ReadyWake, Scheduler},
        EnqueueReason,
    },
};
use std::{
    num::NonZeroUsize,
    sync::{
        atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering},
        Arc,
    },
    time::Duration,
};
fn n(v: usize) -> NonZeroUsize {
    NonZeroUsize::new(v).unwrap()
}
fn change(value: u8, operations: usize, bytes: usize) -> Change<u8> {
    Change {
        value,
        operations: n(operations),
        bytes: n(bytes),
    }
}
#[test]
fn complete_group_survives_actual_timer_while_caller_is_preempted() {
    let mut actor = Actor::new_waking(
        "unchanged confirmed",
        10,
        n(1024),
        Duration::from_millis(40),
        ReadyWake::current(),
    )
    .unwrap();
    actor
        .enqueue_group(vec![change(1, 1, 8), change(2, 1, 8)])
        .unwrap();
    let start = mooring_core::lease::boottime().unwrap();
    while mooring_core::lease::boottime().unwrap().0 - start.0 < Duration::from_millis(80) {
        std::thread::sleep(Duration::from_millis(10));
    }
    let observed = actor
        .dispatch(Duration::from_secs(2))
        .unwrap()
        .unwrap()
        .batch
        .body
        .iter()
        .map(|c| c.value)
        .collect::<Vec<_>>();
    assert_eq!(observed, [1, 2]);
    let (doc, revision, originals) = actor.into_recovery();
    assert_eq!((doc, revision), ("unchanged confirmed", 10));
    assert_eq!(
        originals.iter().map(|c| c.value).collect::<Vec<_>>(),
        [1, 2]
    );
}
#[test]
fn complete_group_refusal_returns_all_and_never_merges_pending_work() {
    let mut actor = Actor::new_waking(
        "confirmed",
        10,
        n(1024),
        Duration::from_millis(40),
        ReadyWake::current(),
    )
    .unwrap();
    let operations = actor
        .enqueue_group(vec![change(1, 256, 1), change(2, 1, 1)])
        .unwrap_err();
    assert_eq!(
        operations.reason,
        EnqueueReason::Batch(Rejection::TooManyOperations)
    );
    assert_eq!(
        operations
            .changes
            .iter()
            .map(|c| c.value)
            .collect::<Vec<_>>(),
        [1, 2]
    );
    let bytes = actor
        .enqueue_group(vec![change(3, 1, 256 * 1024 - 1024), change(4, 1, 1)])
        .unwrap_err();
    assert_eq!(bytes.reason, EnqueueReason::Batch(Rejection::TooManyBytes));
    assert_eq!(
        bytes.changes.iter().map(|c| c.value).collect::<Vec<_>>(),
        [3, 4]
    );
    actor.enqueue_group(vec![change(5, 1, 1)]).unwrap();
    let refused = actor
        .enqueue_group(vec![change(6, 1, 1), change(7, 1, 1)])
        .unwrap_err();
    assert_eq!(refused.reason, EnqueueReason::Backpressure);
    assert_eq!(
        refused.changes.iter().map(|c| c.value).collect::<Vec<_>>(),
        [6, 7]
    );
    assert_eq!(
        actor
            .dispatch(Duration::from_secs(2))
            .unwrap()
            .unwrap()
            .batch
            .body
            .iter()
            .map(|c| c.value)
            .collect::<Vec<_>>(),
        [5]
    );
    actor.enqueue_group(refused.changes).unwrap();
    assert_eq!(
        actor
            .dispatch(Duration::from_secs(2))
            .unwrap()
            .unwrap()
            .batch
            .body
            .iter()
            .map(|c| c.value)
            .collect::<Vec<_>>(),
        [6, 7]
    );
    let (_, revision, retained) = actor.into_recovery();
    assert_eq!(revision, 10);
    assert_eq!(
        retained.iter().map(|c| c.value).collect::<Vec<_>>(),
        [5, 6, 7]
    );
}
struct CountingClock {
    tick: AtomicU64,
    calls: AtomicUsize,
    fail: AtomicBool,
}
impl Clock for CountingClock {
    fn now(&self) -> std::io::Result<mooring_core::batch::Tick> {
        self.calls.fetch_add(1, Ordering::SeqCst);
        if self.fail.load(Ordering::SeqCst) {
            return Err(std::io::Error::other("synthetic clock unavailable"));
        }
        Ok(mooring_core::batch::Tick(Duration::from_millis(
            self.tick.load(Ordering::SeqCst),
        )))
    }
}
#[test]
fn complete_group_reads_clock_once_and_returns_every_original_on_clock_failure() {
    let clock = Arc::new(CountingClock {
        tick: AtomicU64::new(1),
        calls: AtomicUsize::new(0),
        fail: AtomicBool::new(false),
    });
    let mut scheduler = Scheduler::with_clock(
        Limits {
            max_wait: Duration::from_millis(40),
            max_operations: n(2),
            max_bytes: n(100),
        },
        clock.clone(),
    )
    .unwrap();
    let before = clock.calls.load(Ordering::SeqCst);
    scheduler
        .enqueue_group(vec![change(1, 1, 1), change(2, 1, 1)])
        .unwrap();
    assert_eq!(
        clock.calls.load(Ordering::SeqCst),
        before + 1,
        "one complete group has one clock basis"
    );
    assert_eq!(
        scheduler
            .receive(Duration::from_secs(1))
            .unwrap()
            .unwrap()
            .iter()
            .map(|c| c.value)
            .collect::<Vec<_>>(),
        [1, 2]
    );
    clock.fail.store(true, Ordering::SeqCst);
    let refused = scheduler
        .enqueue_group(vec![change(3, 1, 1), change(4, 1, 1)])
        .unwrap_err();
    assert_eq!(refused.reason, EnqueueReason::ClockUnavailable);
    assert_eq!(
        refused.changes.iter().map(|c| c.value).collect::<Vec<_>>(),
        [3, 4]
    );
    assert!(scheduler.close().is_empty());
}

#[test]
fn group_clock_reversal_returns_all_without_touching_prior_handoff() {
    let clock = Arc::new(CountingClock {
        tick: AtomicU64::new(1),
        calls: AtomicUsize::new(0),
        fail: AtomicBool::new(false),
    });
    let mut scheduler = Scheduler::with_clock(
        Limits {
            max_wait: Duration::from_millis(40),
            max_operations: n(2),
            max_bytes: n(100),
        },
        clock.clone(),
    )
    .unwrap();
    scheduler
        .enqueue_group(vec![change(1, 1, 1), change(2, 1, 1)])
        .unwrap();
    let handed = scheduler.receive(Duration::from_secs(1)).unwrap().unwrap();
    clock.tick.store(0, Ordering::SeqCst);
    let refused = scheduler
        .enqueue_group(vec![change(3, 1, 1), change(4, 1, 1)])
        .unwrap_err();
    assert_eq!(
        refused.reason,
        EnqueueReason::Batch(Rejection::ClockReversed)
    );
    assert_eq!(
        refused.changes.iter().map(|c| c.value).collect::<Vec<_>>(),
        [3, 4]
    );
    assert_eq!(handed.iter().map(|c| c.value).collect::<Vec<_>>(), [1, 2]);
    assert!(scheduler.close().is_empty());
}
