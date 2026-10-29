//! Wire-independent single-owner session state. Not a receipt verifier or runtime.
//!
//! An actor owns this value; the independent preparation lane must not borrow it
//! while waiting for the host. All queued/sent intents remain owned here until a
//! validated, ordered host response is installed or recovery takes ownership.
use std::{collections::VecDeque, num::NonZeroUsize};

#[derive(Clone, Copy, Debug)]
pub struct Limits {
    /// Aggregate unsent AND in-flight work, not a separate allowance for each.
    pub pending_operations: NonZeroUsize,
    pub pending_bytes: NonZeroUsize,
    pub in_flight_batches: NonZeroUsize,
}

#[derive(Debug, PartialEq, Eq)]
pub struct Batch<B> {
    pub body: B,
    pub operations: NonZeroUsize,
    /// Exact encoded bytes charged by the trusted serializer, including envelope.
    pub bytes: NonZeroUsize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Closed,
    Capacity,
    SequenceExhausted,
    UnexpectedConfirmation,
    RevisionNotAdvanced,
}

#[derive(Debug)]
pub struct Refused<B> {
    pub reason: Error,
    pub batch: Batch<B>,
}

struct Entry<B> {
    sequence: u64,
    sent: bool,
    batch: Batch<B>,
}

/// Private actor-local correlation, never an operation identity or durable status.
#[derive(Debug)]
pub struct Dispatch<'a, B> {
    pub sequence: u64,
    pub batch: &'a Batch<B>,
}

pub struct Session<D, B> {
    confirmed: D,
    revision: u64,
    limits: Limits,
    pending: VecDeque<Entry<B>>,
    operations: usize,
    bytes: usize,
    in_flight: usize,
    next_sequence: u64,
    closed: bool,
}

impl<D, B> Session<D, B> {
    /// D is an owned, fully restored authoritative app document. It must be
    /// qualified against the authenticated host checkpoint before construction.
    pub fn new(confirmed: D, revision: u64, limits: Limits) -> Self {
        Self {
            confirmed,
            revision,
            limits,
            pending: VecDeque::new(),
            operations: 0,
            bytes: 0,
            in_flight: 0,
            next_sequence: 1,
            closed: false,
        }
    }
    pub fn confirmed(&self) -> (&D, u64) {
        (&self.confirmed, self.revision)
    }
    pub fn pending_usage(&self) -> (usize, usize) {
        (self.operations, self.bytes)
    }
    pub fn is_closed(&self) -> bool {
        self.closed
    }

    /// Called with complete batches emitted by the Rust scheduler. Refusal
    /// returns ownership and leaves every previously queued intent unchanged.
    pub fn enqueue(&mut self, batch: Batch<B>) -> Result<u64, Refused<B>> {
        let reason = if self.closed {
            Some(Error::Closed)
        } else if self.next_sequence == u64::MAX {
            Some(Error::SequenceExhausted)
        } else if batch.operations.get() > self.limits.pending_operations.get()
            || self.operations > self.limits.pending_operations.get() - batch.operations.get()
            || batch.bytes.get() > self.limits.pending_bytes.get()
            || self.bytes > self.limits.pending_bytes.get() - batch.bytes.get()
        {
            Some(Error::Capacity)
        } else {
            None
        };
        if let Some(reason) = reason {
            if reason == Error::SequenceExhausted {
                self.closed = true;
            }
            return Err(Refused { reason, batch });
        }
        let sequence = self.next_sequence;
        self.next_sequence += 1;
        self.operations += batch.operations.get();
        self.bytes += batch.bytes.get();
        self.pending.push_back(Entry {
            sequence,
            sent: false,
            batch,
        });
        Ok(sequence)
    }

    /// Marks work outstanding BEFORE transport I/O, preventing a fast response
    /// from overtaking registration. The body stays owned here for uncertainty
    /// recovery. A partial/failed send MUST close the actor, not call this again.
    /// No durable acknowledgement is needed before sending the next batch.
    pub fn start_send(&mut self) -> Result<Option<Dispatch<'_, B>>, Error> {
        if self.closed {
            return Err(Error::Closed);
        }
        if self.in_flight == self.limits.in_flight_batches.get() {
            return Ok(None);
        }
        let Some(entry) = self.pending.iter_mut().find(|entry| !entry.sent) else {
            return Ok(None);
        };
        entry.sent = true;
        self.in_flight += 1;
        Ok(Some(Dispatch {
            sequence: entry.sequence,
            batch: &entry.batch,
        }))
    }

    /// The integration MUST first validate authenticated host direction, exact
    /// request/scope/owner/session/member bindings, atomic per-op receipts and
    /// checkpoint digest, then restore the complete owned D. This method does
    /// not turn arbitrary Rust values into Saved or authenticate a host.
    ///
    /// A replacement must advance the host revision. Same-revision replays keep
    /// the already confirmed document. None means the verified result requires
    /// no document installation (e.g.
    /// all rejected or exact replay already present), not unknown outcome. The
    /// caller must not pass a speculative prepared candidate as authoritative D.
    /// A malformed/out-of-order result closes the session and retains all work;
    /// recovery needs a fresh host checkpoint/status, never guessed application.
    pub fn settle_host_response(
        &mut self,
        sequence: u64,
        document: Option<(D, u64)>,
    ) -> Result<Batch<B>, Error> {
        if self.closed {
            return Err(Error::Closed);
        }
        if !self
            .pending
            .front()
            .is_some_and(|entry| entry.sent && entry.sequence == sequence)
        {
            self.closed = true;
            return Err(Error::UnexpectedConfirmation);
        }
        if document
            .as_ref()
            .is_some_and(|(_, revision)| *revision <= self.revision)
        {
            self.closed = true;
            return Err(Error::RevisionNotAdvanced);
        }
        if let Some((document, revision)) = document {
            self.confirmed = document;
            self.revision = revision;
        }
        let entry = self.pending.pop_front().unwrap();
        self.operations -= entry.batch.operations.get();
        self.bytes -= entry.batch.bytes.get();
        self.in_flight -= 1;
        Ok(entry.batch)
    }

    /// Uncertain transport/host outcome or revoked ownership stops sends and
    /// installs. Does not delete drafts, fabricate rejection, or reset identities.
    pub fn close(&mut self) {
        self.closed = true;
    }

    /// Recovery owns every unsent AND sent body in original enqueue order.
    pub fn into_recovery(self) -> (D, u64, Vec<Batch<B>>) {
        (
            self.confirmed,
            self.revision,
            self.pending.into_iter().map(|entry| entry.batch).collect(),
        )
    }
}
