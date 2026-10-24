//! Native timer/session composition, not a participant or host transport.
use crate::{
    batch::{Change, Limits as BatchLimits},
    schedule::{native::Scheduler, EnqueueReason, NotEnqueued},
    session::{Batch, Dispatch, Error, Limits, Session},
};
use std::{num::NonZeroUsize, time::Duration};

const FRAME_BYTES: usize = 256 * 1024;
const BATCH_OPERATIONS: usize = 256;
const OUTSTANDING: usize = 4;

#[derive(Debug)]
pub enum StartError {
    EnvelopeReserve,
    Timer(crate::schedule::native::StartError),
}
#[derive(Debug, PartialEq, Eq)]
pub enum Failure {
    State(Error),
    Timer(crate::schedule::native::Failure),
}

/// Owns an actual restored app document, its outstanding immutable batches and
/// the independent native100ms timer. A driver never waits for a durable reply
/// inside dispatch(). The future FD actor supplies authenticated ingress,
/// correlation, receipt/checkpoint validation, per-gate delivery and app reads.
/// There is no session transport capability or write authority in this value.
pub struct Actor<D, T: Send + 'static> {
    scheduler: Scheduler<T>,
    state: Session<D, Vec<Change<T>>>,
    envelope_reserve: usize,
    outstanding: usize,
    stranded: Option<Batch<Vec<Change<T>>>>,
}
impl<D, T: Send + 'static> Actor<D, T> {
    /// reserve is a conservative bound on the actual batch envelope. The wire
    /// encoder MUST check its final output is <= the charged bytes before I/O.
    /// Per-change charges must already include their exact normalized encoding.
    pub fn new(document: D, revision: u64, reserve: NonZeroUsize) -> Result<Self, StartError> {
        Self::with_timer(
            document,
            revision,
            reserve,
            Duration::from_millis(100),
            None,
        )
    }
    /// A live driver can leave encoding/send headroom inside the unchanged
    /// 100 ms maximum, and wake its own actor thread without polling faster.
    /// Notification is only scheduler readiness; it is not ACK or authority.
    pub fn new_waking(
        document: D,
        revision: u64,
        reserve: NonZeroUsize,
        flush_after: Duration,
        wake: crate::schedule::native::ReadyWake,
    ) -> Result<Self, StartError> {
        if flush_after.is_zero() || flush_after > Duration::from_millis(100) {
            return Err(StartError::Timer(
                crate::schedule::native::StartError::Limits,
            ));
        }
        Self::with_timer(document, revision, reserve, flush_after, Some(wake))
    }
    fn with_timer(
        document: D,
        revision: u64,
        reserve: NonZeroUsize,
        flush_after: Duration,
        wake: Option<crate::schedule::native::ReadyWake>,
    ) -> Result<Self, StartError> {
        let remaining = FRAME_BYTES
            .checked_sub(reserve.get())
            .and_then(NonZeroUsize::new)
            .ok_or(StartError::EnvelopeReserve)?;
        let limits = BatchLimits {
            max_wait: flush_after,
            max_operations: NonZeroUsize::new(BATCH_OPERATIONS).unwrap(),
            max_bytes: remaining,
        };
        let scheduler = match wake {
            Some(wake) => Scheduler::with_ready_wake(limits, wake),
            None => Scheduler::new(limits),
        }
        .map_err(StartError::Timer)?;
        Ok(Self {
            scheduler,
            state: Session::new(
                document,
                revision,
                Limits {
                    pending_operations: NonZeroUsize::new(BATCH_OPERATIONS * OUTSTANDING).unwrap(),
                    pending_bytes: NonZeroUsize::new(FRAME_BYTES * OUTSTANDING).unwrap(),
                    in_flight_batches: NonZeroUsize::new(OUTSTANDING).unwrap(),
                },
            ),
            envelope_reserve: reserve.get(),
            outstanding: 0,
            stranded: None,
        })
    }
    pub fn confirmed(&self) -> (&D, u64) {
        self.state.confirmed()
    }
    pub fn enqueue(&self, change: Change<T>) -> Result<(), NotEnqueued<T>> {
        if self.state.is_closed() {
            return Err(NotEnqueued {
                reason: EnqueueReason::Closed,
                change,
            });
        }
        self.scheduler.enqueue(change)
    }
    /// None means timeout OR all four outstanding slots are occupied. At full
    /// capacity the event loop must wait for host/control input, not busy-poll.
    /// The timer still runs and retains at most256ops/256KiB unsent independently.
    pub fn dispatch(
        &mut self,
        wait: Duration,
    ) -> Result<Option<Dispatch<'_, Vec<Change<T>>>>, Failure> {
        if self.state.is_closed() {
            return Err(Failure::State(Error::Closed));
        }
        if self.outstanding == OUTSTANDING {
            return Ok(None);
        }
        // Every prior entry is sent and <= one full batch, so one whole maximum
        // batch fits BEFORE releasing any work from the bounded unsent queue.
        let changes = match self.scheduler.receive(wait) {
            Ok(Some(changes)) => changes,
            Ok(None) => return Ok(None),
            Err(error) => {
                self.close();
                return Err(Failure::Timer(error));
            }
        };
        let operations = changes.iter().map(|change| change.operations.get()).sum();
        let bytes = self.envelope_reserve
            + changes
                .iter()
                .map(|change| change.bytes.get())
                .sum::<usize>();
        // Scheduler guarantees a nonempty indivisible batch <= configured caps.
        let batch = Batch {
            body: changes,
            operations: NonZeroUsize::new(operations).unwrap(),
            bytes: NonZeroUsize::new(bytes).unwrap(),
        };
        if let Err(refused) = self.state.enqueue(batch) {
            // Even sequence exhaustion retains the just-handed-off originals.
            let error = refused.reason;
            self.stranded = Some(refused.batch);
            self.close();
            return Err(Failure::State(error));
        }
        self.outstanding += 1;
        self.state.start_send().map_err(Failure::State)
    }
    /// Same externally verified-host contract as Session::settle_host_response.
    /// Restore D only from the committed checkpoint paired with those receipts;
    /// never install a speculative prepare candidate or a bare issuer string.
    pub fn settle_host_response(
        &mut self,
        sequence: u64,
        document: Option<(D, u64)>,
    ) -> Result<Vec<Change<T>>, Failure> {
        match self.state.settle_host_response(sequence, document) {
            Ok(batch) => {
                self.outstanding -= 1;
                Ok(batch.body)
            }
            Err(error) => {
                self.close();
                Err(Failure::State(error))
            }
        }
    }
    pub fn close(&mut self) {
        self.state.close();
        self.scheduler.shutdown();
    }
    /// Joins the timer and transfers all sent/unsent original intents in order.
    pub fn into_recovery(mut self) -> (D, u64, Vec<Change<T>>) {
        self.close();
        let unsent = self.scheduler.close();
        let (document, revision, pending) = self.state.into_recovery();
        let mut originals: Vec<_> = pending.into_iter().flat_map(|batch| batch.body).collect();
        if let Some(batch) = self.stranded {
            originals.extend(batch.body);
        }
        originals.extend(unsent);
        (document, revision, originals)
    }
}
