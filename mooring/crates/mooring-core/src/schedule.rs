//! Bounded unsent work and an actual native timer. No durable ACK or Saved.
use crate::batch::{Batcher, Change, Limits, Rejection, Tick};
use std::collections::VecDeque;

pub const MAX_UNSENT_OPERATIONS: usize = 256;
pub const MAX_UNSENT_BYTES: usize = 256 * 1024;
#[derive(Debug, PartialEq, Eq)]
pub enum EnqueueReason {
    Batch(Rejection),
    Backpressure,
    Closed,
    ClockUnavailable,
}
#[derive(Debug, PartialEq, Eq)]
pub struct NotEnqueued<T> {
    pub reason: EnqueueReason,
    pub change: Change<T>,
}
#[derive(Debug, PartialEq, Eq)]
pub struct InvalidLimits;

/// Portable queue algorithm. Platform timers must call on_timer at deadline;
/// enqueue alone is not sufficient on WASM. All counts include pending+ready.
pub struct TimedQueue<T> {
    batcher: Batcher<T>,
    ready: VecDeque<Vec<Change<T>>>,
    operations: usize,
    bytes: usize,
    limits: Limits,
    closed: bool,
}
impl<T> TimedQueue<T> {
    pub fn new(limits: Limits) -> Result<Self, InvalidLimits> {
        if limits.max_operations.get() > MAX_UNSENT_OPERATIONS
            || limits.max_bytes.get() > MAX_UNSENT_BYTES
        {
            return Err(InvalidLimits);
        }
        Ok(Self {
            batcher: Batcher::new(limits),
            ready: VecDeque::new(),
            operations: 0,
            bytes: 0,
            limits,
            closed: false,
        })
    }
    pub fn deadline(&self) -> Option<Tick> {
        self.batcher.deadline()
    }
    pub fn enqueue(&mut self, now: Tick, change: Change<T>) -> Result<(), NotEnqueued<T>> {
        let reason = if self.closed {
            Some(EnqueueReason::Closed)
        } else if change.operations.get() > self.limits.max_operations.get() {
            Some(EnqueueReason::Batch(Rejection::TooManyOperations))
        } else if change.bytes.get() > self.limits.max_bytes.get() {
            Some(EnqueueReason::Batch(Rejection::TooManyBytes))
        } else if self.operations > MAX_UNSENT_OPERATIONS - change.operations.get()
            || self.bytes > MAX_UNSENT_BYTES - change.bytes.get()
        {
            Some(EnqueueReason::Backpressure)
        } else {
            None
        };
        if let Some(reason) = reason {
            return Err(NotEnqueued { reason, change });
        }
        let operations = change.operations.get();
        let bytes = change.bytes.get();
        let ready = self
            .batcher
            .push(now, change)
            .map_err(|error| NotEnqueued {
                reason: EnqueueReason::Batch(error.reason),
                change: error.change,
            })?;
        self.operations += operations;
        self.bytes += bytes;
        self.ready.extend(ready);
        Ok(())
    }
    pub fn on_timer(&mut self, now: Tick) -> Result<(), Rejection> {
        if !self.closed {
            if let Some(batch) = self.batcher.take_due(now)? {
                self.ready.push_back(batch);
            }
        }
        Ok(())
    }
    /// Handoff releases UNSENT capacity, independently of a durable ACK. The
    /// caller must separately bound handed-off/inflight work and retain drafts.
    pub fn take_ready(&mut self) -> Option<Vec<Change<T>>> {
        if self.closed {
            return None;
        }
        let batch = self.ready.pop_front()?;
        for change in &batch {
            self.operations -= change.operations.get();
            self.bytes -= change.bytes.get();
        }
        Some(batch)
    }
    /// Explicit cancellation returns every still-unsent atomic intent. It
    /// cannot return work already handed to a transport or issue an ACK.
    pub fn close(&mut self) -> Vec<Change<T>> {
        self.closed = true;
        let mut pending: Vec<_> = self.ready.drain(..).flatten().collect();
        if let Some(batch) = self.batcher.take() {
            pending.extend(batch);
        }
        self.operations = 0;
        self.bytes = 0;
        pending
    }
}

#[cfg(not(target_arch = "wasm32"))]
pub mod native {
    use super::*;
    use std::{
        io,
        sync::{Arc, Condvar, Mutex, MutexGuard},
        thread::{self, JoinHandle},
        time::{Duration, Instant},
    };

    pub trait Clock: Send + Sync + 'static {
        fn now(&self) -> io::Result<Tick>;
    }
    struct BootClock;
    impl Clock for BootClock {
        fn now(&self) -> io::Result<Tick> {
            crate::lease::boottime()
        }
    }
    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    pub enum Failure {
        ClockUnavailable,
        ClockInvalid,
        Poisoned,
        Closed,
    }
    #[derive(Debug)]
    pub enum StartError {
        Limits,
        Clock,
        Thread,
    }
    struct State<T> {
        queue: TimedQueue<T>,
        closed: bool,
        failure: Option<Failure>,
    }
    struct Shared<T> {
        state: Mutex<State<T>>,
        changed: Condvar,
    }
    impl<T> Shared<T> {
        fn lock(&self) -> MutexGuard<'_, State<T>> {
            match self.state.lock() {
                Ok(guard) => guard,
                Err(poisoned) => {
                    let mut guard = poisoned.into_inner();
                    guard.failure = Some(Failure::Poisoned);
                    self.changed.notify_all();
                    guard
                }
            }
        }
    }
    /// Native worker owns timer wakeups. enqueue stamps the clock while holding
    /// the queue lock, so worker scheduling never restarts the first deadline.
    /// No transport or network/ACK wait executes while the queue lock is held.
    pub struct Scheduler<T: Send + 'static> {
        shared: Arc<Shared<T>>,
        clock: Arc<dyn Clock>,
        worker: Option<JoinHandle<()>>,
    }
    impl<T: Send + 'static> Scheduler<T> {
        pub fn new(limits: Limits) -> Result<Self, StartError> {
            Self::with_clock(limits, Arc::new(BootClock))
        }
        pub fn with_clock(limits: Limits, clock: Arc<dyn Clock>) -> Result<Self, StartError> {
            let queue = TimedQueue::new(limits).map_err(|_| StartError::Limits)?;
            clock.now().map_err(|_| StartError::Clock)?;
            let shared = Arc::new(Shared {
                state: Mutex::new(State {
                    queue,
                    closed: false,
                    failure: None,
                }),
                changed: Condvar::new(),
            });
            let worker_shared = shared.clone();
            let worker_clock = clock.clone();
            let worker = thread::Builder::new()
                .name("mooring-batch-timer".into())
                .spawn(move || {
                    let mut state = worker_shared.lock();
                    loop {
                        if state.closed || state.failure.is_some() {
                            break;
                        }
                        if let Some(deadline) = state.queue.deadline() {
                            let now = match worker_clock.now() {
                                Ok(now) => now,
                                Err(_) => {
                                    state.failure = Some(Failure::ClockUnavailable);
                                    worker_shared.changed.notify_all();
                                    break;
                                }
                            };
                            if state.queue.on_timer(now).is_err() {
                                state.failure = Some(Failure::ClockInvalid);
                                worker_shared.changed.notify_all();
                                break;
                            }
                            if now >= deadline {
                                worker_shared.changed.notify_all();
                            } else {
                                state = match worker_shared
                                    .changed
                                    .wait_timeout(state, deadline.0 - now.0)
                                {
                                    Ok((guard, _)) => guard,
                                    Err(poisoned) => {
                                        let (mut guard, _) = poisoned.into_inner();
                                        guard.failure = Some(Failure::Poisoned);
                                        guard
                                    }
                                };
                            }
                        } else {
                            state = match worker_shared.changed.wait(state) {
                                Ok(guard) => guard,
                                Err(poisoned) => {
                                    let mut guard = poisoned.into_inner();
                                    guard.failure = Some(Failure::Poisoned);
                                    guard
                                }
                            };
                        }
                    }
                    worker_shared.changed.notify_all();
                })
                .map_err(|_| StartError::Thread)?;
            Ok(Self {
                shared,
                clock,
                worker: Some(worker),
            })
        }
        pub fn enqueue(&self, change: Change<T>) -> Result<(), NotEnqueued<T>> {
            let mut state = self.shared.lock();
            if state.closed || state.failure.is_some() {
                return Err(NotEnqueued {
                    reason: if state.closed {
                        EnqueueReason::Closed
                    } else {
                        EnqueueReason::ClockUnavailable
                    },
                    change,
                });
            }
            let now = match self.clock.now() {
                Ok(now) => now,
                Err(_) => {
                    state.failure = Some(Failure::ClockUnavailable);
                    self.shared.changed.notify_all();
                    return Err(NotEnqueued {
                        reason: EnqueueReason::ClockUnavailable,
                        change,
                    });
                }
            };
            let result = state.queue.enqueue(now, change);
            if result.as_ref().is_err_and(|error| {
                matches!(
                    error.reason,
                    EnqueueReason::Batch(Rejection::ClockReversed | Rejection::DeadlineOverflow)
                )
            }) {
                state.failure = Some(Failure::ClockInvalid);
            }
            self.shared.changed.notify_all();
            result
        }
        /// A local wait timeout is not a delivery lease and grants no authority.
        pub fn receive(&self, timeout: Duration) -> Result<Option<Vec<Change<T>>>, Failure> {
            let deadline = Instant::now()
                .checked_add(timeout)
                .ok_or(Failure::ClockInvalid)?;
            let mut state = self.shared.lock();
            loop {
                if let Some(failure) = state.failure {
                    return Err(failure);
                }
                if state.closed {
                    return Err(Failure::Closed);
                }
                if let Some(batch) = state.queue.take_ready() {
                    return Ok(Some(batch));
                }
                let Some(remaining) = deadline.checked_duration_since(Instant::now()) else {
                    return Ok(None);
                };
                state = match self.shared.changed.wait_timeout(state, remaining) {
                    Ok((guard, _)) => guard,
                    Err(poisoned) => {
                        let (mut guard, _) = poisoned.into_inner();
                        guard.failure = Some(Failure::Poisoned);
                        guard
                    }
                };
            }
        }
        pub fn close(&mut self) -> Vec<Change<T>> {
            {
                let mut state = self.shared.lock();
                state.closed = true;
                self.shared.changed.notify_all();
            }
            if let Some(worker) = self.worker.take() {
                let _ = worker.join();
            }
            self.shared.lock().queue.close()
        }
    }
    impl<T: Send + 'static> Drop for Scheduler<T> {
        fn drop(&mut self) {
            let _ = self.close();
        }
    }
}
