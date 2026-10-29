//! Unselected owned startup/prepare driver. The caller must first verify the
//! inherited host channel and exact session profile. This adds no CLI route,
//! participant admission, submission processing, or durable outcome authority.
use crate::{
    dispatch::{invalid, wordcraft_registration},
    session_lane::{Inbox, Lane, Output, Route},
    session_startup::{ReadySession, Startup},
};
use mooring_core::batch::Tick;
use mooring_protocol::{boundary::Transport, control, Counter, OwnerGrant, SessionOpen};
use std::{
    io,
    os::unix::net::UnixStream,
    sync::{Arc, OnceLock},
    thread,
    time::{Duration, Instant},
};

pub(crate) type Clock = Arc<dyn Fn() -> io::Result<Tick> + Send + Sync>;

/// Keeps the actual live document apart from stateless preparation candidates.
/// Drop closes the shared socket before joining the bounded prepare worker.
pub struct Driver {
    pub(crate) lane: Lane,
    prepare: Option<thread::JoinHandle<()>>,
    ready: Option<ReadySession>,
    pub(crate) clock: Clock,
    pub(crate) maintenance: crate::owner_maintenance::Maintenance,
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
        Self::from_stream(stream, transport, clock, false, false)
    }
    /// Separately selected read-only inventory support. It grants no writer or
    /// participant capability; the concrete app still qualifies every input.
    pub fn from_negotiated_inherited_stream_with_origins(
        stream: UnixStream,
        transport: &Transport<'_>,
        clock: impl Fn() -> io::Result<Tick> + Send + Sync + 'static,
    ) -> io::Result<Self> {
        Self::from_stream(stream, transport, clock, true, false)
    }
    /// Concrete application handler support. Selection still requires the
    /// explicit closed SessionOpen capability on the actual inherited channel.
    pub fn from_negotiated_inherited_stream_with_native_humans(
        stream: UnixStream,
        transport: &Transport<'_>,
        clock: impl Fn() -> io::Result<Tick> + Send + Sync + 'static,
    ) -> io::Result<Self> {
        Self::from_stream(stream, transport, clock, true, true)
    }
    fn from_stream(
        stream: UnixStream,
        transport: &Transport<'_>,
        clock: impl Fn() -> io::Result<Tick> + Send + Sync + 'static,
        origins: bool,
        human_native: bool,
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
            let (startup, acquire) =
                Startup::from_open_with_capabilities(&raw, first_send, origins, human_native)?;
            let open: SessionOpen =
                serde_json::from_slice(&raw).map_err(|_| invalid("invalid startup open"))?;
            if open.required_capabilities.contains(
                &mooring_protocol::SessionOpenRequiredCapabilitiesItem::HumanNativePrepareV1,
            ) {
                lane.select_native_humans()?;
            }
            let prepare_inbox = lane.inbox(Route::Prepare);
            let prepare_output = output.clone();
            let expected_fence = Arc::new(OnceLock::new());
            let prepare_fence = expected_fence.clone();
            let worker = thread::Builder::new()
                .name("mooring-session-prepare".into())
                .spawn(move || prepare_loop(prepare_inbox, prepare_output, open, prepare_fence))?;
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
                expected_fence
                    .set(ready.owner().fence.clone())
                    .map_err(|_| invalid("startup owner already bound"))?;
                ready.acknowledge(&output, || clock())?;
                let maintenance = crate::owner_maintenance::Maintenance::new(
                    first_send,
                    Duration::from_millis(ready.owner().lease_duration_ms.0),
                    grant,
                )?;
                Ok((ready, maintenance))
            })();
            match ready {
                Ok((ready, maintenance)) => Ok((ready, worker, maintenance)),
                Err(error) => {
                    lane.close();
                    let _ = worker.join();
                    Err(error)
                }
            }
        })();
        match result {
            Ok((ready, prepare, maintenance)) => Ok(Self {
                lane,
                prepare: Some(prepare),
                ready: Some(ready),
                clock,
                maintenance,
            }),
            Err(error) => {
                lane.close();
                Err(error)
            }
        }
    }
    fn ready(&self) -> &ReadySession {
        // into_actor consumes Driver; no caller can observe the emptied slot.
        self.ready.as_ref().expect("unconsumed startup document")
    }
    /// Consume the acknowledged startup exactly once. The original FD, prepare
    /// worker, owner nonce history and one owned Doc stay together. This does
    /// not admit participants or enable a production session profile.
    pub fn into_actor(mut self) -> io::Result<crate::session_actor::RunningDriver> {
        self.check_owner()?;
        let now = (self.clock)()?;
        let ready = self.ready.take().expect("unconsumed startup document");
        let issuer = ready.host_issuer().to_owned();
        let (owner, document) = ready.into_running(now)?;
        crate::session_actor::RunningDriver::from_driver(self, owner, document, issuer)
    }
    pub fn confirmed(&self) -> &crate::session_startup::ConfirmedDocument {
        self.ready().confirmed()
    }
    /// No participant control is processed yet. This remains the exact owned
    /// restored document until a separately qualified durable settlement path.
    pub fn check_owner(&mut self) -> io::Result<()> {
        let now = match (self.clock)() {
            Ok(now) => now,
            Err(error) => {
                self.ready().close_owner();
                self.lane.close();
                return Err(error);
            }
        };
        if self.lane.is_closed() || !self.ready().may_own(now) {
            self.ready().close_owner();
            self.lane.close();
            return Err(invalid("session owner expired"));
        }
        Ok(())
    }
    /// Lease knowledge only, never participant read/write authority.
    pub fn known_owner_permission(&self) -> Counter {
        self.ready().known_owner_permission()
    }

    /// One bounded owner-control step. Preparation remains on its independent
    /// lane. No participant input, receipts or document replacement is accepted.
    pub fn maintain_owner(&mut self, host: &Transport<'_>, wait: Duration) -> io::Result<()> {
        let result = self.maintain_owner_inner(host, wait);
        if result.is_err() {
            self.ready().close_owner();
            self.lane.close();
        }
        result
    }
    fn maintain_owner_inner(&mut self, host: &Transport<'_>, wait: Duration) -> io::Result<()> {
        if !matches!(host,Transport::AuthenticatedHost{issuer,..} if *issuer==self.ready().host_issuer())
        {
            return Err(invalid("owner maintenance requires original host"));
        }
        self.check_owner()?;
        let now = (self.clock)()?;
        let request = if self.maintenance.begin_due(now) {
            let raw = self.ready().begin_owner_renewal(now)?;
            self.maintenance.started(now)?;
            Some(raw)
        } else if self.maintenance.retry_due(now) {
            let raw = self.ready().retry_owner_renewal(now)?;
            self.maintenance.retried(now)?;
            Some(raw)
        } else {
            None
        };
        if let Some(raw) = request {
            self.lane.output().send_control_checked(&raw, || {
                if self.ready().may_own((self.clock)()?) {
                    Ok(())
                } else {
                    Err(invalid("owner expired before renewal send"))
                }
            })?;
        }
        self.check_owner()?;
        let now = (self.clock)()?;
        let wait = self.maintenance.wait(now, wait);
        if let Some(raw) = self.lane.inbox(Route::Actor).receive(wait)? {
            self.check_owner()?;
            // Local FD retries can yield an exact duplicate of the last reply.
            // Ignoring it changes no deadline, permission knowledge or Doc.
            if raw == self.maintenance.completed {
                return Ok(());
            }
            let grant: OwnerGrant = serde_json::from_slice(&raw)
                .map_err(|_| invalid("unexpected owner actor control"))?;
            self.ready()
                .accept_owner_renewal(&raw, host, (self.clock)()?)?;
            self.maintenance
                .completed(Duration::from_millis(grant.lease_duration_ms.0), raw)?;
        }
        self.check_owner()
    }
    /// Unselected lease-only integration loop; channel/clock failure closes all
    /// lanes. A future actor loop must explicitly qualify additional controls.
    pub fn run_owner(&mut self, host: &Transport<'_>) -> io::Result<()> {
        loop {
            self.maintain_owner(host, Duration::from_millis(20))?;
        }
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
fn prepare_loop(
    inbox: Inbox,
    output: Output,
    open: SessionOpen,
    fence: Arc<OnceLock<mooring_protocol::Fence>>,
) {
    // Also poison on an unexpected worker unwind, not only ordinary errors.
    let _close_on_exit = CloseOutput(output.clone());
    let registration = wordcraft_registration();
    let result: io::Result<()> = (|| loop {
        let Some(raw) = inbox.receive(Duration::from_millis(50))? else {
            continue;
        };
        if let Ok(normalized) =
            mooring_protocol::session_control::validate(&raw, control::Peer::InheritedHost)
        {
            let response = match normalized.value()["type"].as_str() {
                Some("session_describe_checkpoint") => {
                    crate::session_description::respond(&raw, &open)?
                }
                Some("session_inspect_origins") => crate::session_origins::respond(
                    &raw,
                    &open,
                    &mooring_wordcraft::adapter::WordCraftAdapter,
                )?,
                _ => return Err(invalid("unexpected preparation control")),
            };
            output.send(&response)?;
            continue;
        }
        let native = open
            .required_capabilities
            .contains(&mooring_protocol::SessionOpenRequiredCapabilitiesItem::HumanNativePrepareV1);
        let validate = if native {
            control::validate_session_native_prepare
        } else {
            control::validate_causal
        };
        let normalized = validate(&raw, control::Peer::InheritedHost)
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
        let response = if native {
            registration.respond_session_native(&raw, &open, fence.get())?
        } else {
            registration.respond_causal(&raw)?
        };
        output.send(&response)?;
    })();
    let _ = result;
    output.close();
}
