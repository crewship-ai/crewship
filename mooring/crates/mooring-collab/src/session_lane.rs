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
}
struct Shared {
    state: Mutex<Queues>,
    ready: Condvar,
    stop: UnixStream,
}
impl Shared {
    fn close(&self) {
        let mut q = self.state.lock().unwrap();
        q.closed = true;
        self.ready.notify_all();
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
}
#[derive(Clone)]
pub struct Output {
    shared: Arc<Shared>,
    writer: Arc<Mutex<UnixStream>>,
}
impl Output {
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
            }),
            ready: Condvar::new(),
            stop,
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
                        || q.count >= session_control::MAX_QUEUED_CONTROLS
                        || canonical.len() > session_control::MAX_QUEUED_CONTROL_BYTES - q.bytes
                    {
                        break;
                    }
                    q.bytes += canonical.len();
                    q.count += 1;
                    q.frames[route as usize].push_back(canonical);
                    read_shared.ready.notify_all();
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
}
