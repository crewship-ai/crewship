//! Fail-closed delivery gate. The caller authenticates the host's response and
//! binds this gate to one participant/session. No content path may bypass it.
use crate::batch::Tick;
use std::time::Duration;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Renewal {
    sequence: u64,
    sent_at: Tick,
}

#[derive(Debug, PartialEq, Eq)]
pub enum LeaseError {
    Closed,
    ClockReversed,
    SequenceExhausted,
    StaleResponse,
    DeadlineOverflow,
    ExpiredResponse,
}

#[derive(Debug, Default)]
pub struct DeliveryGate {
    sequence: u64,
    pending: Option<Renewal>,
    expires_at: Option<Tick>,
    last_tick: Option<Tick>,
    closed: bool,
}

impl DeliveryGate {
    fn observe(&mut self, now: Tick) -> Result<(), LeaseError> {
        if self.closed {
            return Err(LeaseError::Closed);
        }
        if self.last_tick.is_some_and(|last| now < last) {
            self.revoke();
            return Err(LeaseError::ClockReversed);
        }
        self.last_tick = Some(now);
        Ok(())
    }

    /// Call immediately before sending a renewal request to the host. Only the
    /// most recently sent request may renew this gate; reordering fails closed.
    pub fn request(&mut self, now: Tick) -> Result<Renewal, LeaseError> {
        self.observe(now)?;
        let Some(sequence) = self.sequence.checked_add(1) else {
            self.revoke();
            return Err(LeaseError::SequenceExhausted);
        };
        self.sequence = sequence;
        let renewal = Renewal {
            sequence,
            sent_at: now,
        };
        self.pending = Some(renewal);
        Ok(renewal)
    }

    /// Accept a verified host response. Expiry is request send time + duration,
    /// never response arrival time + duration. Errors grant no new permission.
    pub fn renew(
        &mut self,
        request: Renewal,
        duration: Duration,
        now: Tick,
    ) -> Result<(), LeaseError> {
        self.observe(now)?;
        if self.pending != Some(request) {
            return Err(LeaseError::StaleResponse);
        }
        self.pending = None;
        let expires_at = Tick(
            request
                .sent_at
                .0
                .checked_add(duration)
                .ok_or(LeaseError::DeadlineOverflow)?,
        );
        if now >= expires_at {
            return Err(LeaseError::ExpiredResponse);
        }
        self.expires_at = Some(expires_at);
        Ok(())
    }

    /// Check immediately before every content send, including snapshots,
    /// updates, history, exports and assets. Expiry is an exclusive boundary.
    pub fn may_deliver(&mut self, now: Tick) -> bool {
        self.observe(now).is_ok() && self.expires_at.is_some_and(|expiry| now < expiry)
    }

    /// Permanent closure. Late responses cannot reopen this participant gate;
    /// a new authenticated session needs a fresh gate and host permission.
    pub fn revoke(&mut self) {
        self.closed = true;
        self.pending = None;
        self.expires_at = None;
    }
}

/// Linux CLOCK_BOOTTIME includes time spent suspended. Other platforms fail
/// explicitly: wall time and suspend-excluding Instant are unsafe fallbacks.
#[cfg(target_os = "linux")]
pub fn boottime() -> std::io::Result<Tick> {
    use std::ffi::{c_int, c_long};
    #[repr(C)]
    struct Timespec {
        seconds: c_long,
        nanoseconds: c_long,
    }
    unsafe extern "C" {
        fn clock_gettime(clock: c_int, time: *mut Timespec) -> c_int;
    }
    let mut time = Timespec {
        seconds: 0,
        nanoseconds: 0,
    };
    // SAFETY: Linux clock_gettime writes one C ABI timespec into a valid aligned
    // pointer for the duration of this call. CLOCK_BOOTTIME is 7 on Linux.
    if unsafe { clock_gettime(7, &mut time) } != 0 {
        return Err(std::io::Error::last_os_error());
    }
    if time.seconds < 0 || !(0..1_000_000_000).contains(&time.nanoseconds) {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "invalid CLOCK_BOOTTIME",
        ));
    }
    Ok(Tick(Duration::new(
        time.seconds as u64,
        time.nanoseconds as u32,
    )))
}

#[cfg(not(target_os = "linux"))]
pub fn boottime() -> std::io::Result<Tick> {
    Err(std::io::Error::new(
        std::io::ErrorKind::Unsupported,
        "CLOCK_BOOTTIME required for native delivery leases",
    ))
}
