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
/// An already closed transport group returns every original on refusal.
#[derive(Debug, PartialEq, Eq)]
pub struct GroupNotEnqueued<T> {
    pub reason: EnqueueReason,
    pub changes: Vec<Change<T>>,
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
    /// Coalesced, payload-free thread notification. It conveys no transport,
    /// clock or authority. Rebind before checking ready work and then parking;
    /// a notification between that check and park remains as an unpark token.
    #[derive(Clone)]
    pub struct ReadyWake(Arc<Mutex<thread::Thread>>);
    impl ReadyWake {
        pub fn current() -> Self {
            Self(Arc::new(Mutex::new(thread::current())))
        }
        pub fn bind_current(&self) {
            *self.0.lock().unwrap_or_else(|p| p.into_inner()) = thread::current();
        }
        pub fn is_current(&self) -> bool {
            self.0.lock().unwrap_or_else(|p| p.into_inner()).id() == thread::current().id()
        }
        pub fn notify(&self) {
            let target = self.0.lock().unwrap_or_else(|p| p.into_inner()).clone();
            target.unpark();
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
        wake: Option<ReadyWake>,
    }
    impl<T> Shared<T> {
        fn lock(&self) -> MutexGuard<'_, State<T>> {
            match self.state.lock() {
                Ok(guard) => guard,
                Err(poisoned) => {
                    let mut guard = poisoned.into_inner();
                    guard.failure = Some(Failure::Poisoned);
                    self.changed.notify_all();
                    if let Some(wake) = &self.wake {
                        wake.notify();
                    }
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
            Self::with_clock_wake(limits, clock, None)
        }
        pub fn with_ready_wake(limits: Limits, wake: ReadyWake) -> Result<Self, StartError> {
            Self::with_clock_wake(limits, Arc::new(BootClock), Some(wake))
        }
        fn with_clock_wake(
            limits: Limits,
            clock: Arc<dyn Clock>,
            wake: Option<ReadyWake>,
        ) -> Result<Self, StartError> {
            let queue = TimedQueue::new(limits).map_err(|_| StartError::Limits)?;
            clock.now().map_err(|_| StartError::Clock)?;
            let shared = Arc::new(Shared {
                state: Mutex::new(State {
                    queue,
                    closed: false,
                    failure: None,
                }),
                changed: Condvar::new(),
                wake,
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
                                if let Some(wake) = &worker_shared.wake {
                                    wake.notify();
                                }
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
                    if let Some(wake) = &worker_shared.wake {
                        wake.notify();
                    }
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
                    if let Some(wake) = &self.shared.wake {
                        wake.notify();
                    }
                    return Err(NotEnqueued {
                        reason: EnqueueReason::ClockUnavailable,
                        change,
                    });
                }
            };
            let ready_before = state.queue.ready.len();
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
            if state.failure.is_some() || state.queue.ready.len() > ready_before {
                if let Some(wake) = &self.shared.wake {
                    wake.notify();
                }
            }
            result
        }
        /// Atomically admits one complete group into an EMPTY unsent queue.
        /// One clock sample and the existing mutex keep timer preemption from
        /// splitting it. No configured operation/byte/timer limit is enlarged.
        pub fn enqueue_group(&self, changes: Vec<Change<T>>) -> Result<(), GroupNotEnqueued<T>> {
            let mut state = self.shared.lock();
            let operations = changes
                .iter()
                .try_fold(0usize, |n, c| n.checked_add(c.operations.get()));
            let bytes = changes
                .iter()
                .try_fold(0usize, |n, c| n.checked_add(c.bytes.get()));
            let reason = if state.closed {
                Some(EnqueueReason::Closed)
            } else if state.failure.is_some() {
                Some(EnqueueReason::ClockUnavailable)
            } else if changes.is_empty()
                || operations.is_none_or(|n| n > state.queue.limits.max_operations.get())
            {
                Some(EnqueueReason::Batch(Rejection::TooManyOperations))
            } else if bytes.is_none_or(|n| n > state.queue.limits.max_bytes.get()) {
                Some(EnqueueReason::Batch(Rejection::TooManyBytes))
            } else if state.queue.operations != 0 {
                Some(EnqueueReason::Backpressure)
            } else {
                None
            };
            if let Some(reason) = reason {
                return Err(GroupNotEnqueued { reason, changes });
            }
            let now = match self.clock.now() {
                Ok(now) => now,
                Err(_) => {
                    state.failure = Some(Failure::ClockUnavailable);
                    self.shared.changed.notify_all();
                    if let Some(wake) = &self.shared.wake {
                        wake.notify();
                    }
                    return Err(GroupNotEnqueued {
                        reason: EnqueueReason::ClockUnavailable,
                        changes,
                    });
                }
            };
            let mut remaining = changes.into_iter();
            while let Some(change) = remaining.next() {
                if let Err(refused) = state.queue.enqueue(now, change) {
                    // Aggregate bounds/empty queue/same time guarantee that a
                    // healthy clock cannot fail midway. Still retain everything
                    // and close on uncertainty rather than publishing a prefix.
                    let mut originals = state.queue.close();
                    originals.push(refused.change);
                    originals.extend(remaining);
                    state.failure = Some(Failure::ClockInvalid);
                    self.shared.changed.notify_all();
                    if let Some(wake) = &self.shared.wake {
                        wake.notify();
                    }
                    return Err(GroupNotEnqueued {
                        reason: refused.reason,
                        changes: originals,
                    });
                }
            }
            self.shared.changed.notify_all();
            if !state.queue.ready.is_empty() {
                if let Some(wake) = &self.shared.wake {
                    wake.notify();
                }
            }
            Ok(())
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
        /// Wake all receivers and stop the timer through a shared handle.
        /// Unsent ownership stays in the queue until exclusive close() joins
        /// the worker and returns it. Enqueue after shutdown returns its input.
        pub fn shutdown(&self) {
            let mut state = self.shared.lock();
            state.closed = true;
            self.shared.changed.notify_all();
            if let Some(wake) = &self.shared.wake {
                wake.notify();
            }
        }
        pub fn close(&mut self) -> Vec<Change<T>> {
            self.shutdown();
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
