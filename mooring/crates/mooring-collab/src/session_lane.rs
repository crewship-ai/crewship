//! Experimental single-reader inherited transport. Not selected by any CLI.
//! Channel provenance/profile negotiation, request correlation and authority
//! remain the session driver's obligations; bytes cannot attest those facts.
use mooring_protocol::{
    control::{self, Peer},
    session_control,
};
use std::{
    collections::VecDeque,
    io,
    net::Shutdown,
    os::unix::net::UnixStream,
    sync::{Arc, Condvar, Mutex},
    thread::{self, JoinHandle},
    time::{Duration, Instant},
};

#[derive(Clone, Copy)]
pub enum Route {
    Actor = 0,
    Prepare = 1,
}
struct Queues {
    frames: [VecDeque<Vec<u8>>; 2],
    bytes: usize,
    count: usize,
    closed: bool,
    retained_bytes: usize,
    retained_count: usize,
}
struct Shared {
    state: Mutex<Queues>,
    ready: Condvar,
    stop: UnixStream,
    actor_wake: Mutex<Option<mooring_core::schedule::native::ReadyWake>>,
}
impl Shared {
    fn wake_actor(&self) {
        let wake = self.actor_wake.lock().unwrap().clone();
        if let Some(wake) = wake {
            wake.notify();
        }
    }
    fn close(&self) {
        let mut q = self.state.lock().unwrap();
        q.closed = true;
        self.ready.notify_all();
        self.wake_actor();
        drop(q);
        let _ = self.stop.shutdown(Shutdown::Both);
    }
}
#[derive(Clone)]
pub struct Inbox {
    shared: Arc<Shared>,
    route: Route,
}
impl Inbox {
    /// Closure takes priority over queued controls. A dead channel can never
    /// install queued checkpoints or renew gates; recovery may drain originals.
    pub fn receive(&self, timeout: Duration) -> io::Result<Option<Vec<u8>>> {
        let deadline = Instant::now()
            .checked_add(timeout)
            .ok_or_else(|| invalid("receive timeout"))?;
        let mut q = self.shared.state.lock().unwrap();
        loop {
            if q.closed {
                return Err(invalid("closed session channel"));
            }
            if let Some(frame) = q.frames[self.route as usize].pop_front() {
                q.bytes -= frame.len();
                q.count -= 1;
                return Ok(Some(frame));
            }
            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                return Ok(None);
            }
            let (next, wait) = self.shared.ready.wait_timeout(q, remaining).unwrap();
            q = next;
            if wait.timed_out() && !q.closed && q.frames[self.route as usize].is_empty() {
                return Ok(None);
            }
        }
    }
    /// A payload-free readiness token may interrupt this wait. Recheck timer
    /// readiness on every None; no synthetic control enters the bounded queue.
    pub(crate) fn receive_parked(
        &self,
        timeout: Duration,
        wake: &mooring_core::schedule::native::ReadyWake,
    ) -> io::Result<Option<Vec<u8>>> {
        if !matches!(self.route, Route::Actor) || !wake.is_current() {
            return Err(invalid("actor wake target"));
        }
        let take = || {
            let mut q = self.shared.state.lock().unwrap();
            if q.closed {
                return Err(invalid("closed session channel"));
            }
            let frame = q.frames[self.route as usize].pop_front();
            if let Some(ref frame) = frame {
                q.bytes -= frame.len();
                q.count -= 1;
            }
            Ok(frame)
        };
        if let Some(frame) = take()? {
            return Ok(Some(frame));
        }
        // unpark tokens survive the gap between checking the queue and park.
        thread::park_timeout(timeout);
        take()
    }
}
/// Private owned budget reservation, not a receipt/authentication capability.
/// Drop releases accounting even after closure; it carries no protocol bytes.
pub(crate) struct RetainedControl {
    shared: Arc<Shared>,
    bytes: usize,
}
impl Drop for RetainedControl {
    fn drop(&mut self) {
        let mut q = self.shared.state.lock().unwrap_or_else(|p| p.into_inner());
        if let (Some(bytes), Some(count)) = (
            q.retained_bytes.checked_sub(self.bytes),
            q.retained_count.checked_sub(1),
        ) {
            q.retained_bytes = bytes;
            q.retained_count = count;
        } else {
            // An internal accounting failure can never reopen budget/capacity.
            q.closed = true;
            self.shared.ready.notify_all();
            self.shared.wake_actor();
            drop(q);
            let _ = self.shared.stop.shutdown(Shutdown::Both);
        }
    }
}
#[derive(Clone)]
pub struct Output {
    shared: Arc<Shared>,
    writer: Arc<Mutex<UnixStream>>,
}
impl Output {
    /// Inbound queues and retained verified deliveries share EXACTLY the same
    /// 128-control/4MiB aggregate. The caller must reserve before settlement.
    pub(crate) fn reserve_control(&self, bytes: usize) -> io::Result<RetainedControl> {
        let mut q = self.shared.state.lock().unwrap();
        let available = session_control::MAX_QUEUED_CONTROL_BYTES
            .checked_sub(q.bytes)
            .and_then(|n| n.checked_sub(q.retained_bytes))
            .ok_or_else(|| invalid("control accounting"))?;
        if q.closed
            || bytes == 0
            || q.count + q.retained_count >= session_control::MAX_QUEUED_CONTROLS
            || bytes > available
        {
            return Err(invalid("retained control capacity"));
        }
        q.retained_bytes += bytes;
        q.retained_count += 1;
        Ok(RetainedControl {
            shared: self.shared.clone(),
            bytes,
        })
    }

    /// Private control writes do not carry participant content. Participant
    /// outputs must use the separate final-gate API, even on this private FD.
    pub fn send(&self, bytes: &[u8]) -> io::Result<()> {
        if validate_outgoing(bytes)? {
            return Err(invalid("participant send requires final gate"));
        }
        self.write_checked(bytes, || Ok(()))
    }
    pub fn close(&self) {
        self.shared.close();
    }
    /// Owner-bound controls use the same final check after writer wait.
    pub fn send_control_checked(
        &self,
        bytes: &[u8],
        gate: impl FnOnce() -> io::Result<()>,
    ) -> io::Result<()> {
        if validate_outgoing(bytes)? {
            return Err(invalid("participant control bypass"));
        }
        self.write_checked(bytes, gate)
    }
    /// Driver supplies an actual BOOTTIME gate check. It runs AFTER validation,
    /// writer queuing and encoding, immediately before the first frame byte.
    /// This callback is not itself authenticated proof; Go independently checks
    /// its final delivery gate after the private frame arrives, on EVERY chunk.
    pub fn send_participant_checked(
        &self,
        bytes: &[u8],
        gate: impl FnOnce() -> io::Result<()>,
    ) -> io::Result<()> {
        if !validate_outgoing(bytes)? {
            return Err(invalid("not a participant output"));
        }
        self.write_checked(bytes, gate)
    }
    fn write_checked(&self, bytes: &[u8], gate: impl FnOnce() -> io::Result<()>) -> io::Result<()> {
        use std::io::Write;
        let deadline = Instant::now() + Duration::from_secs(5);
        let mut stream = loop {
            if self.shared.state.lock().unwrap().closed {
                return Err(invalid("closed session channel"));
            }
            match self.writer.try_lock() {
                Ok(stream) => break stream,
                Err(std::sync::TryLockError::Poisoned(_)) => {
                    self.shared.close();
                    return Err(invalid("writer poisoned"));
                }
                Err(std::sync::TryLockError::WouldBlock) => {
                    if Instant::now() >= deadline {
                        self.shared.close();
                        return Err(io::ErrorKind::TimedOut.into());
                    }
                    thread::sleep(Duration::from_millis(1));
                }
            }
        };
        let header = (bytes.len() as u32).to_be_bytes();
        if self.shared.state.lock().unwrap().closed {
            return Err(invalid("closed session channel"));
        }
        gate()?;
        let result = (|| {
            let mut writer = DeadlineWriter {
                stream: &mut stream,
                deadline,
            };
            writer.write_all(&header)?;
            writer.write_all(bytes)?;
            writer.flush()
        })();
        if result.is_err() {
            self.shared.close();
        }
        result
    }
}
pub struct Lane {
    shared: Arc<Shared>,
    reader: Option<JoinHandle<()>>,
    output: Output,
}
impl Lane {
    /// Only a driver that already verified inherited-channel provenance and
    /// exact session-profile negotiation may call this. No production caller
    /// or profile selection is added by this isolated transport primitive.
    pub fn from_negotiated_inherited_stream(mut stream: UnixStream) -> io::Result<Self> {
        stream.set_read_timeout(None)?;
        stream.set_write_timeout(Some(Duration::from_secs(5)))?;
        let stop = stream.try_clone()?;
        let writer = Arc::new(Mutex::new(stream.try_clone()?));
        let shared = Arc::new(Shared {
            state: Mutex::new(Queues {
                frames: std::array::from_fn(|_| VecDeque::new()),
                bytes: 0,
                count: 0,
                closed: false,
                retained_bytes: 0,
                retained_count: 0,
            }),
            ready: Condvar::new(),
            stop,
            actor_wake: Mutex::new(None),
        });
        let read_shared = shared.clone();
        let reader = thread::Builder::new()
            .name("mooring-session-reader".into())
            .spawn(move || {
                while let Ok(Some(frame)) = super::read_profile_frame(&mut stream, true) {
                    let (route, canonical) = match validate_incoming(&frame) {
                        Ok(frame) => frame,
                        Err(_) => break,
                    };
                    let mut q = read_shared.state.lock().unwrap();
                    if q.closed
                        || q.count + q.retained_count >= session_control::MAX_QUEUED_CONTROLS
                        || canonical.len()
                            > session_control::MAX_QUEUED_CONTROL_BYTES - q.bytes - q.retained_bytes
                    {
                        break;
                    }
                    q.bytes += canonical.len();
                    q.count += 1;
                    q.frames[route as usize].push_back(canonical);
                    read_shared.ready.notify_all();
                    if matches!(route, Route::Actor) {
                        read_shared.wake_actor();
                    }
                }
                read_shared.close();
            })?;
        let output = Output {
            shared: shared.clone(),
            writer,
        };
        Ok(Self {
            shared,
            reader: Some(reader),
            output,
        })
    }
    pub(crate) fn attach_actor_wake(
        &self,
        wake: mooring_core::schedule::native::ReadyWake,
    ) -> io::Result<()> {
        let mut registered = self.shared.actor_wake.lock().unwrap();
        if registered.is_some() {
            return Err(invalid("actor wake already attached"));
        }
        *registered = Some(wake);
        Ok(())
    }
    pub fn inbox(&self, route: Route) -> Inbox {
        Inbox {
            shared: self.shared.clone(),
            route,
        }
    }
    pub fn output(&self) -> Output {
        self.output.clone()
    }
    pub fn is_closed(&self) -> bool {
        self.shared.state.lock().unwrap().closed
    }
    pub fn close(&self) {
        self.shared.close();
    }
    /// Only for recovery accounting, never dispatch/install after closure.
    pub fn drain_closed(&self) -> io::Result<Vec<Vec<u8>>> {
        let mut q = self.shared.state.lock().unwrap();
        if !q.closed {
            return Err(invalid("channel still active"));
        }
        let frames = q
            .frames
            .iter_mut()
            .flat_map(|frames| frames.drain(..))
            .collect();
        q.bytes = 0;
        q.count = 0;
        Ok(frames)
    }
}
impl Drop for Lane {
    fn drop(&mut self) {
        self.close();
        if let Some(reader) = self.reader.take() {
            let _ = reader.join();
        }
    }
}
fn invalid(reason: &'static str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, reason)
}
fn validate_incoming(raw: &[u8]) -> io::Result<(Route, Vec<u8>)> {
    // Separate exact causal profile remains unchanged when composed here.
    if let Ok(value) = control::validate_causal(raw, Peer::InheritedHost) {
        return Ok((Route::Prepare, value.bytes().to_vec()));
    }
    if let Ok(value) = session_control::validate(raw, Peer::InheritedHost) {
        let route = if matches!(
            value.value()["type"].as_str(),
            Some("session_describe_checkpoint" | "session_inspect_origins")
        ) {
            Route::Prepare
        } else {
            Route::Actor
        };
        return Ok((route, value.bytes().to_vec()));
    }
    // Only these small host lease/admission controls belong to the session
    // driver. In particular a bare CommitResult cannot install session state.
    let value = mooring_protocol::wire::validate_bounded_envelope(raw)
        .map_err(|_| invalid("invalid session control"))?;
    if !matches!(
        value.value()["type"].as_str(),
        Some("owner_grant" | "delivery_grant" | "ticket_validated" | "revocation_notice")
    ) {
        return Err(invalid("unexpected session control"));
    }
    Ok((Route::Actor, value.bytes().to_vec()))
}
fn validate_outgoing(raw: &[u8]) -> io::Result<bool> {
    if control::validate_causal(raw, Peer::InheritedRuntime).is_ok() {
        return Ok(false);
    }
    if let Ok(value) = session_control::validate(raw, Peer::InheritedRuntime) {
        return Ok(value.value()["type"] == "session_participant_output");
    }
    let value = mooring_protocol::wire::validate_bounded_envelope(raw)
        .map_err(|_| invalid("invalid session output"))?;
    if !matches!(
        value.value()["type"].as_str(),
        Some(
            "owner_acquire"
                | "owner_renew"
                | "ticket_validate"
                | "delivery_renew"
                | "revocation_ack"
        )
    ) {
        return Err(invalid("unexpected session output"));
    }
    Ok(false)
}

struct DeadlineWriter<'a> {
    stream: &'a mut UnixStream,
    deadline: Instant,
}
impl std::io::Write for DeadlineWriter<'_> {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        let left = self.deadline.saturating_duration_since(Instant::now());
        if left.is_zero() {
            return Err(io::ErrorKind::TimedOut.into());
        }
        self.stream.set_write_timeout(Some(left))?;
        std::io::Write::write(self.stream, bytes)
    }
    fn flush(&mut self) -> io::Result<()> {
        std::io::Write::flush(self.stream)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;
    fn fixture(name: &str) -> Vec<u8> {
        let f: Value = serde_json::from_str(include_str!(
            "../../../protocol/session-control-fixtures.json"
        ))
        .unwrap();
        serde_json::to_vec(
            &f["cases"]
                .as_array()
                .unwrap()
                .iter()
                .find(|c| c["id"] == name)
                .unwrap()["message"],
        )
        .unwrap()
    }
    fn causal(name: &str) -> Vec<u8> {
        let f: Value = serde_json::from_str(include_str!(
            "../../../protocol/causal-control-fixtures.json"
        ))
        .unwrap();
        serde_json::to_vec(
            &f["cases"]
                .as_array()
                .unwrap()
                .iter()
                .find(|c| c["id"] == name)
                .unwrap()["message"],
        )
        .unwrap()
    }
    fn pair() -> (Lane, UnixStream) {
        let (child, host) = UnixStream::pair().unwrap();
        host.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
        host.set_write_timeout(Some(Duration::from_secs(2)))
            .unwrap();
        (Lane::from_negotiated_inherited_stream(child).unwrap(), host)
    }
    fn await_closed(lane: &Lane) {
        let q = lane.shared.state.lock().unwrap();
        let (q, wait) = lane
            .shared
            .ready
            .wait_timeout_while(q, Duration::from_secs(5), |q| !q.closed)
            .unwrap();
        assert!(q.closed && !wait.timed_out());
    }
    #[test]
    fn preparation_progresses_on_same_fd_while_actor_waits_for_commit() {
        let (lane, mut host) = pair();
        let actor = lane.inbox(Route::Actor);
        let prepare = lane.inbox(Route::Prepare);
        let output = lane.output();
        let submit = fixture("valid-SessionSubmitBatch");
        output.send(&submit).unwrap();
        assert_eq!(
            super::super::read_profile_frame(&mut host, true)
                .unwrap()
                .unwrap(),
            submit
        );
        // Host deliberately withholds durable result and calls preparation on
        // the SAME stream, as the real SQL writer must do. Actor has no reply.
        let request = causal("validate_checkpoint-causal");
        super::super::write_profile_frame(&mut host, &request, true).unwrap();
        let worker = thread::spawn(move || {
            let raw = prepare.receive(Duration::from_secs(2)).unwrap().unwrap();
            let _: mooring_protocol::ValidateCheckpointCausal =
                serde_json::from_slice(&raw).unwrap();
            output.send(&causal("checkpoint_validated-causal")).unwrap();
        });
        assert!(actor.receive(Duration::from_millis(20)).unwrap().is_none());
        let reply = super::super::read_profile_frame(&mut host, true)
            .unwrap()
            .unwrap();
        assert_eq!(
            serde_json::from_slice::<Value>(&reply).unwrap()["type"],
            "checkpoint_validated_causal"
        );
        worker.join().unwrap();
        super::super::write_profile_frame(&mut host, &fixture("valid-SessionCommitResult"), true)
            .unwrap();
        let reply = actor.receive(Duration::from_secs(2)).unwrap().unwrap();
        assert_eq!(
            serde_json::from_slice::<Value>(&reply).unwrap()["type"],
            "session_commit_result"
        );
    }
    #[test]
    fn aggregate_control_count_overflow_closes_both_lanes_and_retains_accounting() {
        let (lane, mut host) = pair();
        let frame = fixture("valid-SessionOpen");
        for _ in 0..=session_control::MAX_QUEUED_CONTROLS {
            super::super::write_profile_frame(&mut host, &frame, true).unwrap();
        }
        await_closed(&lane);
        assert!(lane.inbox(Route::Actor).receive(Duration::ZERO).is_err());
        assert!(lane.inbox(Route::Prepare).receive(Duration::ZERO).is_err());
        let retained = lane.drain_closed().unwrap();
        assert_eq!(retained.len(), session_control::MAX_QUEUED_CONTROLS);
        assert!(
            retained.iter().map(Vec::len).sum::<usize>()
                <= session_control::MAX_QUEUED_CONTROL_BYTES
        );
    }
    #[test]
    fn aggregate_control_bytes_are_not_per_frame_times_count() {
        let (lane, mut host) = pair();
        let mut frame: Value = serde_json::from_slice(&fixture("valid-SessionRestore")).unwrap();
        frame["checkpoint"]["state"] = Value::String("x".repeat(1_500_000));
        let frame = serde_json::to_vec(&frame).unwrap();
        for _ in 0..3 {
            super::super::write_profile_frame(&mut host, &frame, true).unwrap();
        }
        await_closed(&lane);
        let retained = lane.drain_closed().unwrap();
        assert_eq!(retained.len(), 2);
        assert!(
            retained.iter().map(Vec::len).sum::<usize>()
                <= session_control::MAX_QUEUED_CONTROL_BYTES
        );
    }
    #[test]
    fn wrong_direction_and_shutdown_cannot_dispatch_queued_content() {
        let (lane, mut host) = pair();
        super::super::write_profile_frame(&mut host, &fixture("valid-SessionOpened"), true)
            .unwrap();
        await_closed(&lane);
        assert!(lane.inbox(Route::Actor).receive(Duration::ZERO).is_err());
        let (lane, _host) = pair();
        let inbox = lane.inbox(Route::Actor);
        let worker = thread::spawn(move || inbox.receive(Duration::from_secs(60)));
        lane.close();
        assert!(worker.join().unwrap().is_err());
        assert!(lane
            .output()
            .send(&fixture("valid-SessionSubmitBatch"))
            .is_err());
    }
    #[test]
    fn participant_gate_runs_after_writer_wait_and_denial_writes_nothing() {
        let (lane, mut host) = pair();
        let output = lane.output();
        let frame = fixture("valid-SessionParticipantOutput");
        assert!(output.send(&frame).is_err());
        let held = output.writer.lock().unwrap();
        let allowed = Arc::new(std::sync::atomic::AtomicBool::new(true));
        let gate = allowed.clone();
        let queued = output.clone();
        let worker = thread::spawn(move || {
            queued.send_participant_checked(&frame, || {
                if !gate.load(std::sync::atomic::Ordering::SeqCst) {
                    return Err(io::ErrorKind::PermissionDenied.into());
                }
                Ok(())
            })
        });
        allowed.store(false, std::sync::atomic::Ordering::SeqCst);
        drop(held);
        assert_eq!(
            worker.join().unwrap().unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        // An unrelated private control still works, and is the first frame:
        // denied participant content did not leak a header or partial payload.
        let control = fixture("valid-SessionSubmitBatch");
        output.send(&control).unwrap();
        assert_eq!(
            super::super::read_profile_frame(&mut host, true)
                .unwrap()
                .unwrap(),
            control
        );
    }
    #[test]
    fn payload_free_wake_rebinds_and_interrupts_actor_wait() {
        let (lane, _host) = pair();
        let wake = mooring_core::schedule::native::ReadyWake::current();
        lane.attach_actor_wake(wake.clone()).unwrap();
        assert!(lane.attach_actor_wake(wake.clone()).is_err());
        let inbox = lane.inbox(Route::Actor);
        let notify = wake.clone();
        let (ready, started) = std::sync::mpsc::channel();
        let worker = thread::spawn(move || {
            wake.bind_current();
            ready.send(()).unwrap();
            let before = Instant::now();
            assert!(inbox
                .receive_parked(Duration::from_secs(3), &wake)
                .unwrap()
                .is_none());
            assert!(before.elapsed() < Duration::from_secs(1));
        });
        started.recv().unwrap();
        // Also covers an unpark BEFORE the receive's park call.
        notify.notify();
        worker.join().unwrap();
        assert_eq!(lane.shared.state.lock().unwrap().count, 0);
    }
    #[test]
    fn actor_frame_and_close_wake_without_fake_controls() {
        let (lane, mut host) = pair();
        let wake = mooring_core::schedule::native::ReadyWake::current();
        lane.attach_actor_wake(wake.clone()).unwrap();
        let inbox = lane.inbox(Route::Actor);
        let frame = fixture("valid-SessionParticipantOpen");
        super::super::write_frame(&mut host, &frame).unwrap();
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            if let Some(received) = inbox.receive_parked(Duration::from_secs(2), &wake).unwrap() {
                assert_eq!(received, frame);
                break;
            }
            assert!(Instant::now() < deadline);
        }
        lane.close();
        assert!(inbox.receive_parked(Duration::from_secs(2), &wake).is_err());
    }
    #[test]
    fn retained_delivery_bytes_share_both_inboxes_and_release_exactly_once() {
        let (lane, mut host) = pair();
        let actor = fixture("valid-SessionParticipantOpen");
        let prepare = causal("validate_checkpoint-causal");
        let charge = session_control::MAX_QUEUED_CONTROL_BYTES - actor.len() - prepare.len();
        let reservation = lane.output().reserve_control(charge).unwrap();
        super::super::write_profile_frame(&mut host, &actor, true).unwrap();
        super::super::write_profile_frame(&mut host, &prepare, true).unwrap();
        let q = lane.shared.state.lock().unwrap();
        let (q, timeout) = lane
            .shared
            .ready
            .wait_timeout_while(q, Duration::from_secs(2), |q| q.count != 2 && !q.closed)
            .unwrap();
        assert!(!timeout.timed_out());
        assert!(!q.closed);
        assert_eq!(
            q.bytes + q.retained_bytes,
            session_control::MAX_QUEUED_CONTROL_BYTES
        );
        drop(q);
        assert!(lane.output().reserve_control(1).is_err());
        assert_eq!(
            lane.inbox(Route::Actor)
                .receive(Duration::ZERO)
                .unwrap()
                .unwrap(),
            actor
        );
        let replacement = lane.output().reserve_control(actor.len()).unwrap();
        assert_eq!(
            lane.inbox(Route::Prepare)
                .receive(Duration::ZERO)
                .unwrap()
                .unwrap(),
            prepare
        );
        drop(replacement);
        drop(reservation);
        let q = lane.shared.state.lock().unwrap();
        assert_eq!(
            (q.bytes, q.count, q.retained_bytes, q.retained_count),
            (0, 0, 0, 0)
        );
    }
    #[test]
    fn retained_control_count_and_closed_drain_never_reset_live_reservations() {
        let (lane, mut host) = pair();
        let mut reservations: Vec<_> = (0..session_control::MAX_QUEUED_CONTROLS)
            .map(|_| lane.output().reserve_control(1).unwrap())
            .collect();
        assert!(lane.output().reserve_control(1).is_err());
        assert!(lane
            .inbox(Route::Actor)
            .receive(Duration::ZERO)
            .unwrap()
            .is_none());
        drop(reservations.pop());
        super::super::write_profile_frame(
            &mut host,
            &fixture("valid-SessionParticipantOpen"),
            true,
        )
        .unwrap();
        let q = lane.shared.state.lock().unwrap();
        let (q, timeout) = lane
            .shared
            .ready
            .wait_timeout_while(q, Duration::from_secs(2), |q| q.count != 1 && !q.closed)
            .unwrap();
        assert!(!timeout.timed_out());
        assert_eq!(
            q.retained_count + q.count,
            session_control::MAX_QUEUED_CONTROLS
        );
        drop(q);
        assert!(lane.output().reserve_control(1).is_err());
        lane.close();
        assert_eq!(lane.drain_closed().unwrap().len(), 1);
        assert!(lane.output().reserve_control(1).is_err());
        assert_eq!(
            lane.shared.state.lock().unwrap().retained_count,
            reservations.len()
        );
        drop(reservations);
        let q = lane.shared.state.lock().unwrap();
        assert!(q.closed);
        assert_eq!(
            (q.retained_bytes, q.retained_count, q.bytes, q.count),
            (0, 0, 0, 0)
        );
    }
}
