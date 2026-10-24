//! Owned-document startup on the selected inherited session profile. This is not
//! selected by the CLI. A driver must admit exactly ONE SessionOpen per fresh
//! host runtime_ref; recreating Startup for that same admission is forbidden.
pub use crate::session_owner_guard::PermissionAdvance;
use crate::{
    dispatch::{invalid, inventory_set, wordcraft_versions},
    session_lane::Output,
    session_owner_guard::OwnerGuard,
};
use mooring_core::{
    adapter::PrepareAdapter, batch::Tick, checkpoint_description::DescribeCheckpoint,
};
use mooring_protocol::{boundary::Transport, control, grant::PendingGrant, session_control, *};
use mooring_wordcraft::{adapter::WordCraftAdapter, Document};
use std::{collections::BTreeMap, io, time::Duration};

pub struct Startup {
    open: SessionOpen,
    pending: PendingGrant,
    last_tick: Tick,
}
/// The actual restored application owns its Yrs document. Exact authoritative
/// bytes remain separate: reserialization must never invent a confirmed basis.
pub struct ConfirmedDocument {
    document: Document,
    checkpoint: Checkpoint,
    targets: Vec<TargetVersion>,
}
impl ConfirmedDocument {
    pub(crate) fn committed(
        document: Document,
        checkpoint: Checkpoint,
        targets: Vec<TargetVersion>,
    ) -> Self {
        Self {
            document,
            checkpoint,
            targets,
        }
    }
    pub fn document(&self) -> &Document {
        &self.document
    }
    pub fn checkpoint(&self) -> &Checkpoint {
        &self.checkpoint
    }
    pub fn targets(&self) -> &[TargetVersion] {
        &self.targets
    }
}
pub struct OwnerAccepted {
    open: SessionOpen,
    grant: OwnerGrant,
    host_issuer: String,
    expires: Duration,
    last_tick: Tick,
}
pub struct ReadySession {
    open: SessionOpen,
    owner: OwnerGrant,
    host_issuer: String,
    guard: OwnerGuard,
    confirmed: ConfirmedDocument,
    restore_request: String,
    acknowledged: bool,
}
impl Startup {
    /// Clock is trusted BOOTTIME. A request retry reuses the returned EXACT
    /// bytes and this pending object; no nonce nomination or first-send reset.
    pub fn from_open(raw: &[u8], first_send: Tick) -> io::Result<(Self, Vec<u8>)> {
        Self::from_open_with_origins(raw, first_send, false)
    }
    /// Only the concrete driver with an actual inspector handler may select
    /// this opt-in. Neither a copied capability nor the schema enables it.
    pub(crate) fn from_open_with_origins(
        raw: &[u8],
        first_send: Tick,
        origins: bool,
    ) -> io::Result<(Self, Vec<u8>)> {
        let normalized = session_control::validate(raw, control::Peer::InheritedHost)
            .map_err(|_| invalid("invalid session open"))?;
        let open: SessionOpen = serde_json::from_value(normalized.value().clone())
            .map_err(|_| invalid("not session open"))?;
        // The schema declares optional future opt-ins, not implementation
        // support. This driver still has no origin-inventory lane handler.
        let identity_only = open.required_capabilities
            == vec![SessionOpenRequiredCapabilitiesItem::CheckpointIdentityV1];
        let both = open.required_capabilities.len() == 2
            && open
                .required_capabilities
                .contains(&SessionOpenRequiredCapabilitiesItem::CheckpointIdentityV1)
            && open
                .required_capabilities
                .contains(&SessionOpenRequiredCapabilitiesItem::OriginInventoryV1);
        if !identity_only && !(origins && both) {
            return Err(invalid("unimplemented session capability"));
        }
        if open.scope.application_id != "wordcraft" || open.versions != wordcraft_versions() {
            return Err(invalid("unqualified application tuple"));
        }
        let digest = normalized.digest();
        let request = OwnerAcquire {
            r#type: OwnerAcquireType::OwnerAcquire,
            request_id: format!("owner-{}", &digest[7..39]),
            scope: open.scope.clone(),
            runtime_ref: open.runtime_ref.clone(),
            renewal_nonce: format!("owner-{}", &digest[7..]),
        };
        let raw = serde_json::to_vec(&request).map_err(|_| invalid("owner request encoding"))?;
        let pending =
            PendingGrant::owner_acquire(&raw, Counter(0), first_send.0, Duration::from_secs(60))
                .map_err(|_| invalid("invalid acquire request"))?;
        let retry = pending.bytes_for_retry().to_vec();
        Ok((
            Self {
                open,
                pending,
                last_tick: first_send,
            },
            retry,
        ))
    }
    pub fn retry(&self) -> &[u8] {
        self.pending.bytes_for_retry()
    }
    /// Transport is caller-trusted, never deserialized from a frame. Actual FD
    /// admission must establish it; an issuer string cannot prove authority.
    pub fn accept_owner(
        self,
        raw: &[u8],
        transport: &Transport<'_>,
        now: Tick,
    ) -> io::Result<OwnerAccepted> {
        if now < self.last_tick {
            return Err(invalid("owner clock reversed"));
        }
        let bound = self
            .pending
            .validate_reply(raw, transport, now.0)
            .map_err(|_| invalid("unbound owner grant"))?;
        let grant: OwnerGrant = serde_json::from_value(bound.value().clone())
            .map_err(|_| invalid("invalid owner grant"))?;
        // OwnerAcquire has no session_id. Bind its reply to the ONE pending
        // SessionOpen, not an unrelated host grant or a participant assertion.
        if grant.session_id != self.open.session_id {
            return Err(invalid("owner grant session mismatch"));
        }
        let Transport::AuthenticatedHost { issuer, .. } = transport else {
            return Err(invalid("untrusted owner transport"));
        };
        Ok(OwnerAccepted {
            open: self.open,
            grant,
            host_issuer: (*issuer).into(),
            expires: bound.expires_at(),
            last_tick: now,
        })
    }
}
impl OwnerAccepted {
    /// A driver must poll the bound owner while waiting for restoration.
    pub fn check_owner(&mut self, now: Tick) -> io::Result<()> {
        self.observe(now)
    }
    /// The clock is sampled BEFORE and AFTER restore/serialization work. The
    /// caller must supply BOOTTIME; tests can inject deterministic clocks.
    pub fn restore(
        mut self,
        raw: &[u8],
        mut clock: impl FnMut() -> io::Result<Tick>,
    ) -> io::Result<ReadySession> {
        self.observe(clock()?)?;
        let normalized = session_control::validate(raw, control::Peer::InheritedHost)
            .map_err(|_| invalid("invalid session restore"))?;
        let request: SessionRestore = serde_json::from_value(normalized.value().clone())
            .map_err(|_| invalid("not session restore"))?;
        if request.scope != self.open.scope
            || request.session_id != self.open.session_id
            || request.runtime_ref != self.open.runtime_ref
            || request.fence != self.grant.fence
            || request.versions != self.open.versions
        {
            return Err(invalid("restore owner binding"));
        }
        let checkpoint = &request.checkpoint;
        // Checkpoint generation is creator provenance. The authenticated host
        // must verify it against retained SQL provenance; a fresh owner fence
        // must not relabel an unchanged checkpoint during takeover.
        if checkpoint.scope != self.open.scope
            || checkpoint.versions != self.open.versions
            || checkpoint.revision != self.grant.revision
            || checkpoint.format_version != mooring_wordcraft::profile::CHECKPOINT_FORMAT
        {
            return Err(invalid("restore checkpoint binding"));
        }
        let bytes = checkpoint::verify(
            checkpoint,
            checkpoint::Limits {
                decoded_bytes: control::MAX_CHECKPOINT_BYTES,
                preimage_bytes: control::MAX_CONTROL_BYTES,
            },
        )
        .map_err(|_| invalid("checkpoint full identity"))?
        .state()
        .to_vec();
        let description = WordCraftAdapter
            .describe_checkpoint(&bytes)
            .map_err(|_| invalid("unqualified restored checkpoint metadata"))?;
        if description.format_version != checkpoint.format_version
            || description.versions != checkpoint.versions
            || description.assets != checkpoint.assets
        {
            return Err(invalid("restored checkpoint metadata mismatch"));
        }
        // Validate complete actual application inventory, not a projection that
        // can hide unsupported roots. Its temporary owned doc is dropped before
        // restoring the retained live doc; no app revision ledger is introduced.
        let inventory = WordCraftAdapter
            .validate_checkpoint(&bytes)
            .map_err(|_| invalid("invalid app checkpoint"))?;
        let inventory = inventory_set(&inventory)?;
        let mut targets = BTreeMap::new();
        for target in &request.targets {
            if target.structure_revision > target.revision
                || target.revision > checkpoint.revision
                || targets
                    .insert(target.target_id.as_str(), target.deleted)
                    .is_some()
            {
                return Err(invalid("invalid target counters"));
            }
        }
        if inventory != targets {
            return Err(invalid("incomplete app target inventory"));
        }
        let document = Document::restore(&bytes).map_err(|_| invalid("live document restore"))?;
        self.observe(clock()?)?;
        let guard = OwnerGuard::new(
            &self.open,
            &self.grant,
            &self.host_issuer,
            self.expires,
            self.last_tick,
        );
        Ok(ReadySession {
            open: self.open,
            owner: self.grant,
            host_issuer: self.host_issuer,
            guard,
            confirmed: ConfirmedDocument {
                document,
                checkpoint: request.checkpoint,
                targets: request.targets,
            },
            restore_request: request.request_id,
            acknowledged: false,
        })
    }
    fn observe(&mut self, now: Tick) -> io::Result<()> {
        if now < self.last_tick || now.0 >= self.expires {
            return Err(invalid("owner expired during startup"));
        }
        self.last_tick = now;
        Ok(())
    }
}
impl ReadySession {
    /// Consume startup exactly once. The live actor receives the one owned Doc;
    /// only owner metadata moves into the non-cloneable controller.
    pub fn into_running(
        self,
        now: Tick,
    ) -> io::Result<(crate::session_confirmed::OwnerController, ConfirmedDocument)> {
        if !self.acknowledged || !self.guard.may_own(now) {
            self.guard.close();
            return Err(invalid("session was not acknowledged under a live owner"));
        }
        Ok((
            crate::session_confirmed::OwnerController::from_parts(
                self.open,
                self.owner,
                self.host_issuer,
                self.guard,
            ),
            self.confirmed,
        ))
    }
    pub fn confirmed(&self) -> &ConfirmedDocument {
        &self.confirmed
    }
    /// Initial grant identity/fence only; current permission uses known_owner_permission.
    pub fn owner(&self) -> &OwnerGrant {
        &self.owner
    }
    pub fn host_issuer(&self) -> &str {
        &self.host_issuer
    }
    /// Lease metadata is interior-mutable; the actual confirmed Doc remains immutable.
    pub fn may_own(&self, now: Tick) -> bool {
        self.guard.may_own(now)
    }
    pub fn close_owner(&self) {
        self.guard.close();
    }
    pub fn known_owner_permission(&self) -> Counter {
        self.guard.permission()
    }
    /// This is only an owner-version prerequisite, never a read/write grant.
    pub fn matches_owner_permission(&self, version: Counter, now: Tick) -> bool {
        self.guard.matches_permission(version, now)
    }
    pub fn begin_owner_renewal(&self, now: Tick) -> io::Result<Vec<u8>> {
        self.guard.begin(now)
    }
    pub fn retry_owner_renewal(&self, now: Tick) -> io::Result<Vec<u8>> {
        self.guard.retry(now)
    }
    /// An advance blocks old participant versions; it never opens/renews gates.
    pub fn accept_owner_renewal(
        &self,
        raw: &[u8],
        host: &Transport<'_>,
        now: Tick,
    ) -> io::Result<PermissionAdvance> {
        self.guard.accept(raw, host, now)
    }
    /// Real writes on the shared FD, with owner checks after encoding and queue
    /// wait. Failure closes the whole session; no participant content is sent.
    pub fn acknowledge(
        &mut self,
        output: &Output,
        mut clock: impl FnMut() -> io::Result<Tick>,
    ) -> io::Result<()> {
        if self.acknowledged || self.guard.is_closed() {
            return Err(invalid("startup acknowledgement already consumed"));
        }
        let restored = SessionRestored {
            r#type: SessionRestoredType::SessionRestored,
            request_id: self.restore_request.clone(),
            control_profile: SessionRestoredControlProfile::MooringSessionCausalV1,
            scope: self.open.scope.clone(),
            session_id: self.open.session_id.clone(),
            runtime_ref: self.open.runtime_ref.clone(),
            fence: self.owner.fence.clone(),
            versions: self.open.versions.clone(),
            confirmed_basis: self.basis(),
        };
        let opened = SessionOpened {
            r#type: SessionOpenedType::SessionOpened,
            request_id: self.open.request_id.clone(),
            control_profile: SessionOpenedControlProfile::MooringSessionCausalV1,
            scope: self.open.scope.clone(),
            session_id: self.open.session_id.clone(),
            runtime_ref: self.open.runtime_ref.clone(),
            fence: self.owner.fence.clone(),
            versions: self.open.versions.clone(),
            confirmed_basis: self.basis(),
            capabilities: self
                .open
                .required_capabilities
                .iter()
                .map(|capability| match capability {
                    SessionOpenRequiredCapabilitiesItem::CheckpointIdentityV1 => {
                        SessionOpenedCapabilitiesItem::CheckpointIdentityV1
                    }
                    SessionOpenRequiredCapabilitiesItem::OriginInventoryV1 => {
                        SessionOpenedCapabilitiesItem::OriginInventoryV1
                    }
                    SessionOpenRequiredCapabilitiesItem::HumanNativePrepareV1 => {
                        SessionOpenedCapabilitiesItem::HumanNativePrepareV1
                    }
                })
                .collect(),
        };
        for raw in [serde_json::to_vec(&restored), serde_json::to_vec(&opened)] {
            let raw = raw.map_err(|_| invalid("startup reply encoding"))?;
            let result = output.send_control_checked(&raw, || {
                if self.may_own(clock()?) {
                    Ok(())
                } else {
                    Err(invalid("owner expired before startup reply"))
                }
            });
            if result.is_err() {
                self.guard.close();
                output.close();
                return result;
            }
        }
        self.acknowledged = true;
        Ok(())
    }
    pub fn basis(&self) -> ConfirmedCheckpointBasis {
        ConfirmedCheckpointBasis {
            revision: self.confirmed.checkpoint.revision,
            digest: self.confirmed.checkpoint.digest.clone(),
        }
    }
}
