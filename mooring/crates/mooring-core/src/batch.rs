//! Bounded batches of indivisible changes. The runtime must schedule `take_due`
//! at `deadline`; enqueueing alone is not a timer and does not persist anything.
use std::num::NonZeroUsize;
use std::time::Duration;

/// Time relative to the runtime's monotonic clock. Native delivery leases must
/// use a suspend-aware clock; batching does not grant permission to deliver.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Tick(pub Duration);

#[derive(Clone, Copy, Debug)]
pub struct Limits {
    pub max_wait: Duration,
    pub max_operations: NonZeroUsize,
    pub max_bytes: NonZeroUsize,
}

impl Default for Limits {
    fn default() -> Self {
        Self {
            max_wait: Duration::from_millis(100),
            max_operations: NonZeroUsize::new(256).unwrap(),
            max_bytes: NonZeroUsize::new(256 * 1024).unwrap(),
        }
    }
}

/// A complete atomic change, already encoded and validated by the adapter.
/// `bytes` includes its protocol envelope; the batch transport must separately
/// account for the fixed batch envelope when selecting the configured limit.
#[derive(Debug, PartialEq, Eq)]
pub struct Change<T> {
    pub value: T,
    pub operations: NonZeroUsize,
    pub bytes: NonZeroUsize,
}

#[derive(Debug, PartialEq, Eq)]
pub enum Rejection {
    TooManyOperations,
    TooManyBytes,
    ClockReversed,
    DeadlineOverflow,
}

/// Ownership of a rejected change returns to the caller for a visible failure.
#[derive(Debug, PartialEq, Eq)]
pub struct Rejected<T> {
    pub reason: Rejection,
    pub change: Change<T>,
}

#[derive(Debug)]
pub struct Batcher<T> {
    limits: Limits,
    changes: Vec<Change<T>>,
    operations: usize,
    bytes: usize,
    deadline: Option<Tick>,
    last_tick: Option<Tick>,
}

impl<T> Batcher<T> {
    pub fn new(limits: Limits) -> Self {
        Self {
            limits,
            changes: Vec::new(),
            operations: 0,
            bytes: 0,
            deadline: None,
            last_tick: None,
        }
    }

    pub fn deadline(&self) -> Option<Tick> {
        self.deadline
    }

    /// Emits zero, one, or two batches ready to send immediately. A full batch
    /// is emitted on this call; a change that would overflow starts a new batch.
    /// Rejection never drains queued work. No atomic change is ever split.
    pub fn push(
        &mut self,
        now: Tick,
        change: Change<T>,
    ) -> Result<Vec<Vec<Change<T>>>, Rejected<T>> {
        let reason = if self.last_tick.is_some_and(|last| now < last) {
            Some(Rejection::ClockReversed)
        } else if change.operations.get() > self.limits.max_operations.get() {
            Some(Rejection::TooManyOperations)
        } else if change.bytes.get() > self.limits.max_bytes.get() {
            Some(Rejection::TooManyBytes)
        } else if now.0.checked_add(self.limits.max_wait).is_none() {
            Some(Rejection::DeadlineOverflow)
        } else {
            None
        };
        if let Some(reason) = reason {
            return Err(Rejected { reason, change });
        }
        self.last_tick = Some(now);
        let mut ready = Vec::new();
        if self.deadline.is_some_and(|deadline| now >= deadline)
            || self.operations > self.limits.max_operations.get() - change.operations.get()
            || self.bytes > self.limits.max_bytes.get() - change.bytes.get()
        {
            if let Some(batch) = self.take() {
                ready.push(batch);
            }
        }
        if self.changes.is_empty() {
            self.deadline = Some(Tick(now.0.checked_add(self.limits.max_wait).unwrap()));
        }
        self.operations += change.operations.get();
        self.bytes += change.bytes.get();
        self.changes.push(change);
        if self.operations == self.limits.max_operations.get()
            || self.bytes == self.limits.max_bytes.get()
            || self.limits.max_wait.is_zero()
        {
            ready.push(self.take().unwrap());
        }
        Ok(ready)
    }

    /// Timer callback. The caller must send returned work immediately, then
    /// rearm the timer from `deadline`. Taking a batch does not mean "Saved".
    pub fn take_due(&mut self, now: Tick) -> Result<Option<Vec<Change<T>>>, Rejection> {
        if self.last_tick.is_some_and(|last| now < last) {
            return Err(Rejection::ClockReversed);
        }
        self.last_tick = Some(now);
        if self.deadline.is_some_and(|deadline| now >= deadline) {
            Ok(self.take())
        } else {
            Ok(None)
        }
    }

    /// Explicit flush, e.g. shutdown. The caller retains responsibility for
    /// delivery/retry; work returned here has no durable receipt yet.
    pub fn take(&mut self) -> Option<Vec<Change<T>>> {
        if self.changes.is_empty() {
            return None;
        }
        self.operations = 0;
        self.bytes = 0;
        self.deadline = None;
        Some(std::mem::take(&mut self.changes))
    }
}
