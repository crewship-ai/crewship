//! Unselected owned startup/prepare driver. The caller must first verify the
//! inherited host channel and exact session profile. This adds no CLI route,
//! participant admission, submission processing, or durable outcome authority.
use crate::{
    dispatch::{invalid, wordcraft_registration},
    session_lane::{Inbox, Lane, Output, Route},
    session_startup::{ReadySession, Startup},
};
use mooring_core::batch::Tick;
use mooring_protocol::{boundary::Transport, control, SessionOpen};
use std::{
    io,
    os::unix::net::UnixStream,
    sync::Arc,
    thread,
    time::{Duration, Instant},
};

type Clock = Arc<dyn Fn() -> io::Result<Tick> + Send + Sync>;

/// Keeps the actual live document apart from stateless preparation candidates.
/// Drop closes the shared socket before joining the bounded prepare worker.
pub struct Driver {
    lane: Lane,
    prepare: Option<thread::JoinHandle<()>>,
    ready: ReadySession,
    clock: Clock,
}
impl Driver {
    /// This constructor deliberately does not negotiate or assert transport
    /// provenance. Only verified inherited-FD integration may call it. The
    /// supplied clock must be native suspend-aware BOOTTIME, never wall time.
    pub fn from_negotiated_inherited_stream(
        stream: UnixStream,
        transport: &Transport<'_>,
        clock: impl Fn() -> io::Result<Tick> + Send + Sync + 'static,
    ) -> io::Result<Self> {
        let Transport::AuthenticatedHost { .. } = transport else {
            return Err(invalid("startup requires authenticated host"));
        };
        let clock: Clock = Arc::new(clock);
        let lane = Lane::from_negotiated_inherited_stream(stream)?;
        let actor = lane.inbox(Route::Actor);
        let output = lane.output();
        let result = (|| {
            let raw = receive_until(&actor, || Ok(()))?;
            let first_send = clock()?;
            let (startup, acquire) = Startup::from_open(&raw, first_send)?;
            let open: SessionOpen =
                serde_json::from_slice(&raw).map_err(|_| invalid("invalid startup open"))?;
            let prepare_inbox = lane.inbox(Route::Prepare);
            let prepare_output = output.clone();
            let worker = thread::Builder::new()
                .name("mooring-session-prepare".into())
                .spawn(move || prepare_loop(prepare_inbox, prepare_output, open))?;
            let ready = (|| {
                output.send_control_checked(&acquire, || {
                    let now = clock()?;
                    if now < first_send {
                        return Err(invalid("owner clock reversed"));
                    }
                    Ok(())
                })?;
                let grant = receive_until(&actor, || {
                    if clock()? < first_send {
                        return Err(invalid("owner clock reversed"));
                    }
                    Ok(())
                })?;
                let mut owner = startup.accept_owner(&grant, transport, clock()?)?;
                let restore = receive_until(&actor, || owner.check_owner(clock()?))?;
                let mut ready = owner.restore(&restore, || clock())?;
                ready.acknowledge(&output, || clock())?;
                Ok(ready)
            })();
            match ready {
                Ok(ready) => Ok((ready, worker)),
                Err(error) => {
                    lane.close();
                    let _ = worker.join();
                    Err(error)
                }
            }
        })();
        match result {
            Ok((ready, prepare)) => Ok(Self {
                lane,
                prepare: Some(prepare),
                ready,
                clock,
            }),
            Err(error) => {
                lane.close();
                Err(error)
            }
        }
    }
    pub fn confirmed(&self) -> &crate::session_startup::ConfirmedDocument {
        self.ready.confirmed()
    }
    /// No participant control is processed yet. This remains the exact owned
    /// restored document until a separately qualified durable settlement path.
    pub fn check_owner(&mut self) -> io::Result<()> {
        let now = match (self.clock)() {
            Ok(now) => now,
            Err(error) => {
                self.ready.close_owner();
                self.lane.close();
                return Err(error);
            }
        };
        if self.lane.is_closed() || !self.ready.may_own(now) {
            self.ready.close_owner();
            self.lane.close();
            return Err(invalid("session owner expired"));
        }
        Ok(())
    }
}
impl Drop for Driver {
    fn drop(&mut self) {
        self.lane.close();
        if let Some(worker) = self.prepare.take() {
            let _ = worker.join();
        }
    }
}
fn receive_until(inbox: &Inbox, mut check: impl FnMut() -> io::Result<()>) -> io::Result<Vec<u8>> {
    let end = Instant::now()
        .checked_add(Duration::from_secs(5))
        .ok_or_else(|| invalid("startup deadline"))?;
    loop {
        check()?;
        if Instant::now() >= end {
            return Err(invalid("startup deadline"));
        }
        if let Some(raw) = inbox.receive(Duration::from_millis(20))? {
            check()?;
            return Ok(raw);
        }
    }
}
struct CloseOutput(Output);
impl Drop for CloseOutput {
    fn drop(&mut self) {
        self.0.close();
    }
}
fn prepare_loop(inbox: Inbox, output: Output, open: SessionOpen) {
    // Also poison on an unexpected worker unwind, not only ordinary errors.
    let _close_on_exit = CloseOutput(output.clone());
    let registration = wordcraft_registration();
    let result: io::Result<()> = (|| loop {
        let Some(raw) = inbox.receive(Duration::from_millis(50))? else {
            continue;
        };
        let normalized = control::validate_causal(&raw, control::Peer::InheritedHost)
            .map_err(|_| invalid("invalid scoped prepare"))?;
        // Application-only registration is insufficient for an owned session:
        // require the exact same workspace/artifact and semantic tuple too.
        if normalized.value()["scope"]
            != serde_json::to_value(&open.scope).map_err(|_| invalid("scope"))?
            || normalized.value()["selected"]
                != serde_json::to_value(&open.versions).map_err(|_| invalid("versions"))?
        {
            return Err(invalid("prepare escaped session binding"));
        }
        let response = registration.respond_causal(&raw)?;
        output.send(&response)?;
    })();
    let _ = result;
    output.close();
}
