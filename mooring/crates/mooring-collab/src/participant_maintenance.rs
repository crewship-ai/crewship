//! Scheduling metadata only. Existing ReadLease remains the sole native gate.
use crate::dispatch::invalid;
use mooring_core::batch::Tick;
use std::{io, time::Duration};

pub(crate) struct Pending {
    first_send: Tick,
    retry_at: Duration,
}
pub(crate) struct Maintenance {
    due: Duration,
    interval: Duration,
    pub pending: Option<Pending>,
    pub completed: Vec<u8>,
}
impl Maintenance {
    pub fn new(first: Tick, duration: Duration) -> io::Result<Self> {
        let interval = interval(duration)?;
        Ok(Self {
            due: add(first, interval)?,
            interval,
            pending: None,
            completed: Vec::new(),
        })
    }
    pub fn begin_due(&self, now: Tick) -> bool {
        self.pending.is_none() && now.0 >= self.due
    }
    pub fn retry_due(&self, now: Tick) -> bool {
        self.pending.as_ref().is_some_and(|p| now.0 >= p.retry_at)
    }
    pub fn started(&mut self, now: Tick) -> io::Result<()> {
        if self.pending.is_some() {
            return Err(invalid("delivery renewal already pending"));
        }
        self.pending = Some(Pending {
            first_send: now,
            retry_at: add(now, self.interval)?,
        });
        Ok(())
    }
    pub fn retried(&mut self, now: Tick) -> io::Result<()> {
        let retry_at = add(now, self.interval)?;
        self.pending
            .as_mut()
            .ok_or_else(|| invalid("delivery retry without request"))?
            .retry_at = retry_at;
        Ok(())
    }
    pub fn completed(&mut self, duration: Duration, raw: Vec<u8>) -> io::Result<()> {
        let first = self
            .pending
            .as_ref()
            .ok_or_else(|| invalid("unsolicited delivery response"))?
            .first_send;
        self.interval = interval(duration)?;
        self.due = add(first, self.interval)?; // Never anchor next renewal to reply/retry time.
        self.pending = None;
        self.completed = raw;
        Ok(())
    }
    pub fn abandon(&mut self) {
        self.pending = None;
        self.completed.clear();
    }
    pub fn wait(&self, now: Tick, maximum: Duration) -> Duration {
        maximum.min(
            self.pending
                .as_ref()
                .map_or(self.due, |p| p.retry_at)
                .saturating_sub(now.0),
        )
    }
}
fn interval(duration: Duration) -> io::Result<Duration> {
    if duration.is_zero() || duration > Duration::from_secs(2) {
        return Err(invalid("delivery duration bound"));
    }
    Ok((duration / 8)
        .min(Duration::from_millis(250))
        .max(Duration::from_millis(1)))
}
fn add(now: Tick, duration: Duration) -> io::Result<Duration> {
    now.0
        .checked_add(duration)
        .ok_or_else(|| invalid("delivery schedule overflow"))
}
#[cfg(test)]
mod tests {
    use super::*;
    fn at(ms: u64) -> Tick {
        Tick(Duration::from_millis(ms))
    }
    #[test]
    fn delayed_reply_leaves_next_renewal_immediately_due_without_resetting_first_send() {
        let mut m = Maintenance::new(at(0), Duration::from_secs(2)).unwrap();
        assert!(!m.begin_due(at(249)));
        assert!(m.begin_due(at(250)));
        m.started(at(250)).unwrap();
        m.retried(at(500)).unwrap();
        m.completed(Duration::from_secs(2), b"checked reply".to_vec())
            .unwrap();
        assert!(m.begin_due(at(1900)));
        assert_eq!(m.due, Duration::from_millis(500));
        assert_eq!(m.wait(at(1900), Duration::from_secs(1)), Duration::ZERO);
    }
    #[test]
    fn bounds_and_overflow_never_change_gate_duration_or_authority() {
        assert!(Maintenance::new(at(0), Duration::ZERO).is_err());
        assert!(Maintenance::new(at(0), Duration::from_millis(2001)).is_err());
        assert!(Maintenance::new(Tick(Duration::MAX), Duration::from_secs(2)).is_err());
        let mut m = Maintenance::new(at(0), Duration::from_secs(2)).unwrap();
        assert!(m.retried(at(0)).is_err());
        m.started(at(250)).unwrap();
        assert!(m.started(at(500)).is_err());
        m.abandon();
        assert!(m.pending.is_none());
        assert!(m.completed.is_empty());
    }
}
