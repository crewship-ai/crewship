use mooring_core::{
    batch::{Change, Limits, Rejection, Tick},
    schedule::*,
};
use std::{num::NonZeroUsize, time::Duration};
fn change(value: u32, operations: usize, bytes: usize) -> Change<u32> {
    Change {
        value,
        operations: NonZeroUsize::new(operations).unwrap(),
        bytes: NonZeroUsize::new(bytes).unwrap(),
    }
}
fn tick(ms: u64) -> Tick {
    Tick(Duration::from_millis(ms))
}
#[test]
fn deterministic_deadline_does_not_restart_and_atomic_change_stays_whole() {
    let mut queue = TimedQueue::new(Limits::default()).unwrap();
    queue.enqueue(tick(1), change(10, 2, 20)).unwrap();
    queue.enqueue(tick(100), change(11, 3, 30)).unwrap();
    assert_eq!(queue.deadline(), Some(tick(101)));
    queue.on_timer(tick(100)).unwrap();
    assert!(queue.take_ready().is_none());
    queue.on_timer(tick(101)).unwrap();
    let batch = queue.take_ready().unwrap();
    assert_eq!(batch.len(), 2);
    assert_eq!(batch[0].operations.get(), 2);
    assert_eq!(batch[1].operations.get(), 3);
    assert!(queue.deadline().is_none());
    assert!(queue.close().is_empty());
}
#[test]
fn unsent_operation_and_byte_caps_reject_with_ownership_until_transport_handoff() {
    let limits = Limits {
        max_operations: NonZeroUsize::new(1).unwrap(),
        ..Limits::default()
    };
    let mut queue = TimedQueue::new(limits).unwrap();
    for id in 0..MAX_UNSENT_OPERATIONS {
        queue.enqueue(tick(0), change(id as u32, 1, 1)).unwrap();
    }
    let rejected = queue.enqueue(tick(0), change(999, 1, 1)).unwrap_err();
    assert_eq!(rejected.reason, EnqueueReason::Backpressure);
    assert_eq!(rejected.change.value, 999);
    assert_eq!(queue.take_ready().unwrap()[0].value, 0);
    queue.enqueue(tick(0), rejected.change).unwrap();
    assert_eq!(queue.close().len(), MAX_UNSENT_OPERATIONS);
    let mut queue = TimedQueue::new(Limits::default()).unwrap();
    queue
        .enqueue(tick(0), change(1, 1, MAX_UNSENT_BYTES))
        .unwrap();
    let rejected = queue.enqueue(tick(0), change(2, 1, 1)).unwrap_err();
    assert_eq!(rejected.reason, EnqueueReason::Backpressure);
    assert_eq!(queue.take_ready().unwrap()[0].bytes.get(), MAX_UNSENT_BYTES);
    queue.enqueue(tick(0), rejected.change).unwrap();
    assert_eq!(queue.close()[0].value, 2);
}
#[test]
fn oversize_clock_failure_and_close_preserve_accepted_and_rejected_intents() {
    let mut queue = TimedQueue::new(Limits {
        max_operations: NonZeroUsize::new(2).unwrap(),
        ..Limits::default()
    })
    .unwrap();
    queue.enqueue(tick(10), change(1, 2, 10)).unwrap(); // ready
    queue.enqueue(tick(11), change(2, 1, 10)).unwrap(); // pending
    let oversized = queue.enqueue(tick(11), change(3, 3, 10)).unwrap_err();
    assert_eq!(
        oversized.reason,
        EnqueueReason::Batch(Rejection::TooManyOperations)
    );
    assert_eq!(oversized.change.value, 3);
    assert_eq!(queue.on_timer(tick(9)), Err(Rejection::ClockReversed));
    assert_eq!(
        queue.close().iter().map(|c| c.value).collect::<Vec<_>>(),
        vec![1, 2]
    );
    let closed = queue.enqueue(tick(12), change(4, 1, 10)).unwrap_err();
    assert_eq!(closed.reason, EnqueueReason::Closed);
    assert_eq!(closed.change.value, 4);
    assert!(queue.close().is_empty());
    assert!(TimedQueue::<()>::new(Limits {
        max_bytes: NonZeroUsize::new(MAX_UNSENT_BYTES + 1).unwrap(),
        ..Limits::default()
    })
    .is_err());
}

#[cfg(target_os = "linux")]
mod native {
    use super::*;
    use mooring_core::schedule::native::{Clock, Failure, Scheduler};
    use std::{
        io::{self, Read, Write},
        os::unix::net::UnixStream,
        sync::{
            atomic::{AtomicBool, AtomicUsize, Ordering},
            Arc,
        },
        thread,
        time::Instant,
    };
    #[test]
    fn actual_single_change_timers_send_twice_before_delayed_durable_ack() {
        let scheduler = Arc::new(Scheduler::new(Limits::default()).unwrap());
        let (mut sender, mut authority) = UnixStream::pair().unwrap();
        sender
            .set_read_timeout(Some(Duration::from_secs(3)))
            .unwrap();
        authority
            .set_read_timeout(Some(Duration::from_secs(3)))
            .unwrap();
        let mut ack_socket = sender.try_clone().unwrap();
        let acknowledged = Arc::new(AtomicBool::new(false));
        let ack_flag = acknowledged.clone();
        let ack_reader = thread::spawn(move || {
            let mut ack = [0];
            ack_socket.read_exact(&mut ack).unwrap();
            assert_eq!(ack, [b'K']);
            ack_flag.store(true, Ordering::SeqCst);
        });
        let send_queue = scheduler.clone();
        let transport = thread::spawn(move || {
            for _ in 0..2 {
                let batch = send_queue.receive(Duration::from_secs(3)).unwrap().unwrap();
                assert_eq!(batch.len(), 1);
                sender.write_all(&[batch[0].value as u8]).unwrap();
            }
        });
        let start = Instant::now();
        scheduler.enqueue(change(1, 1, 1)).unwrap();
        let mut received = [0];
        authority.read_exact(&mut received).unwrap();
        let first = start.elapsed();
        assert_eq!(received, [1]);
        assert!(!acknowledged.load(Ordering::SeqCst));
        let second_start = Instant::now();
        scheduler.enqueue(change(2, 1, 1)).unwrap();
        authority.read_exact(&mut received).unwrap();
        let second = second_start.elapsed();
        assert_eq!(received, [2]);
        assert!(!acknowledged.load(Ordering::SeqCst));
        assert!(first >= Duration::from_millis(75) && first < Duration::from_secs(2));
        assert!(second >= Duration::from_millis(75) && second < Duration::from_secs(2));
        thread::sleep(Duration::from_millis(100));
        let ack_delay = start.elapsed();
        authority.write_all(b"K").unwrap();
        ack_reader.join().unwrap();
        transport.join().unwrap();
        eprintln!("actual_timer_evidence first_send_ms={} second_low_load_ms={} durable_ack_ms={} both_sent_before_ack=true",first.as_millis(),second.as_millis(),ack_delay.as_millis());
        let mut scheduler = Arc::try_unwrap(scheduler).ok().unwrap();
        assert!(scheduler.close().is_empty());
    }
    struct ScriptedClock {
        calls: AtomicUsize,
        regress: bool,
    }
    impl Clock for ScriptedClock {
        fn now(&self) -> io::Result<Tick> {
            match self.calls.fetch_add(1, Ordering::SeqCst) {
                0 => Ok(tick(100)),
                1 => Ok(tick(101)),
                _ if self.regress => Ok(tick(99)),
                _ => Err(io::Error::other("synthetic unavailable clock")),
            }
        }
    }
    #[test]
    fn timer_clock_failure_is_visible_without_new_input_and_close_returns_unsent_work() {
        for regress in [false, true] {
            let mut scheduler = Scheduler::with_clock(
                Limits::default(),
                Arc::new(ScriptedClock {
                    calls: AtomicUsize::new(0),
                    regress,
                }),
            )
            .unwrap();
            scheduler.enqueue(change(10, 1, 10)).unwrap();
            assert_eq!(
                scheduler.receive(Duration::from_secs(1)),
                Err(if regress {
                    Failure::ClockInvalid
                } else {
                    Failure::ClockUnavailable
                })
            );
            let rejected = scheduler.enqueue(change(11, 1, 10)).unwrap_err();
            assert_eq!(rejected.change.value, 11);
            assert_eq!(
                scheduler
                    .close()
                    .iter()
                    .map(|change| change.value)
                    .collect::<Vec<_>>(),
                vec![10]
            );
        }
    }
}
