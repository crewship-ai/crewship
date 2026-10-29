//! Bounded timer metadata only. Lease authority remains in OwnerGuard.
use crate::dispatch::invalid;
use mooring_core::batch::Tick;
use std::{io, time::Duration};

pub(crate) struct Pending {
    pub first_send: Tick,
    retry_at: Duration,
}
pub(crate) struct Maintenance {
    due: Duration,
    duration: Duration,
    pub pending: Option<Pending>,
    pub completed: Vec<u8>,
}
impl Maintenance {
    pub fn new(first_send: Tick, duration: Duration, completed: Vec<u8>) -> io::Result<Self> {
        Ok(Self {
            due: due(first_send, duration)?,
            duration,
            pending: None,
            completed,
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
            return Err(invalid("owner renewal already scheduled"));
        }
        self.pending = Some(Pending {
            first_send: now,
            retry_at: self.retry_time(now)?,
        });
        Ok(())
    }
    pub fn retried(&mut self, now: Tick) -> io::Result<()> {
        let retry_at = self.retry_time(now)?;
        self.pending
            .as_mut()
            .ok_or_else(|| invalid("no scheduled owner renewal"))?
            .retry_at = retry_at;
        Ok(())
    }
    fn retry_time(&self, now: Tick) -> io::Result<Duration> {
        let delay = (self.duration / 4)
            .min(Duration::from_millis(250))
            .max(Duration::from_millis(1));
        now.0
            .checked_add(delay)
            .ok_or_else(|| invalid("owner retry clock overflow"))
    }
    pub fn completed(&mut self, duration: Duration, raw: Vec<u8>) -> io::Result<()> {
        let first = self
            .pending
            .as_ref()
            .ok_or_else(|| invalid("unsolicited owner renewal"))?
            .first_send;
        self.due = due(first, duration)?;
        self.duration = duration;
        self.completed = raw;
        self.pending = None;
        Ok(())
    }
    pub fn wait(&self, now: Tick, maximum: Duration) -> Duration {
        let next = self.pending.as_ref().map_or(self.due, |p| p.retry_at);
        maximum
            .min(Duration::from_millis(20))
            .min(next.saturating_sub(now.0))
    }
}
fn due(first: Tick, duration: Duration) -> io::Result<Duration> {
    if duration.is_zero() || duration > Duration::from_secs(60) {
        return Err(invalid("invalid owner duration"));
    }
    first
        .0
        .checked_add(duration / 2)
        .ok_or_else(|| invalid("owner schedule overflow"))
}

#[cfg(test)]
mod tests {
    use super::*;
    fn at(ms: u64) -> Tick {
        Tick(Duration::from_millis(ms))
    }
    #[test]
    fn renewal_deadlines_are_from_first_send_not_retry_or_reply() {
        let mut m =
            Maintenance::new(at(10), Duration::from_millis(1000), b"initial".to_vec()).unwrap();
        assert!(!m.begin_due(at(509)));
        assert!(m.begin_due(at(510)));
        m.started(at(510)).unwrap();
        assert!(m.started(at(511)).is_err());
        assert!(!m.retry_due(at(759)));
        assert!(m.retry_due(at(760)));
        m.retried(at(760)).unwrap();
        assert_eq!(m.pending.as_ref().unwrap().first_send, at(510));
        m.completed(Duration::from_millis(1000), b"completed".to_vec())
            .unwrap();
        assert!(!m.begin_due(at(1009)));
        assert!(m.begin_due(at(1010)));
        assert_eq!(m.completed, b"completed");
    }
    #[test]
    fn wait_is_bounded_and_invalid_or_overflowing_duration_fails() {
        let m = Maintenance::new(at(0), Duration::from_secs(60), Vec::new()).unwrap();
        assert_eq!(m.wait(at(0), Duration::MAX), Duration::from_millis(20));
        assert_eq!(m.wait(at(29999), Duration::MAX), Duration::from_millis(1));
        assert!(Maintenance::new(at(0), Duration::ZERO, Vec::new()).is_err());
        assert!(Maintenance::new(at(0), Duration::from_secs(61), Vec::new()).is_err());
        assert!(
            Maintenance::new(Tick(Duration::MAX), Duration::from_millis(1), Vec::new()).is_err()
        );
    }
}
