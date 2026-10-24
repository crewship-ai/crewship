//! Owned-document startup on the selected inherited session profile. This is not
//! selected by the CLI. A driver must admit exactly ONE SessionOpen per fresh
//! host runtime_ref; recreating Startup for that same admission is forbidden.
use crate::{
    dispatch::{invalid, inventory_set, wordcraft_versions},
    session_lane::Output,
};
use mooring_core::{adapter::PrepareAdapter, batch::Tick};
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
    expires: Duration,
    last_tick: Tick,
    confirmed: ConfirmedDocument,
    restore_request: String,
    closed: bool,
    acknowledged: bool,
}
impl Startup {
    /// Clock is trusted BOOTTIME. A request retry reuses the returned EXACT
    /// bytes and this pending object; no nonce nomination or first-send reset.
    pub fn from_open(raw: &[u8], first_send: Tick) -> io::Result<(Self, Vec<u8>)> {
        let normalized = session_control::validate(raw, control::Peer::InheritedHost)
            .map_err(|_| invalid("invalid session open"))?;
        let open: SessionOpen = serde_json::from_value(normalized.value().clone())
            .map_err(|_| invalid("not session open"))?;
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
        if checkpoint.scope != self.open.scope
            || checkpoint.generation != self.grant.fence.generation
            || checkpoint.versions != self.open.versions
            || checkpoint.revision != self.grant.revision
            || checkpoint.format_version != mooring_wordcraft::profile::CHECKPOINT_FORMAT
        {
            return Err(invalid("restore checkpoint binding"));
        }
        let bytes = match checkpoint.encoding {
            CheckpointEncoding::Base64 => control::decode_checkpoint(&OpaquePreparedCheckpoint {
                encoding: OpaquePreparedCheckpointEncoding::Base64,
                state: checkpoint.state.clone(),
                digest: checkpoint.digest.clone(),
            })
            .map_err(|_| invalid("checkpoint digest or encoding"))?,
            CheckpointEncoding::Json => {
                let bytes = checkpoint.state.as_bytes();
                let encoded = control::encode_checkpoint(bytes)
                    .map_err(|_| invalid("checkpoint resource bound"))?;
                if encoded.digest != checkpoint.digest {
                    return Err(invalid("checkpoint digest"));
                }
                bytes.to_vec()
            }
        };
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
        Ok(ReadySession {
            open: self.open,
            owner: self.grant,
            host_issuer: self.host_issuer,
            expires: self.expires,
            last_tick: self.last_tick,
            confirmed: ConfirmedDocument {
                document,
                checkpoint: request.checkpoint,
                targets: request.targets,
            },
            restore_request: request.request_id,
            closed: false,
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
    pub fn confirmed(&self) -> &ConfirmedDocument {
        &self.confirmed
    }
    pub fn owner(&self) -> &OwnerGrant {
        &self.owner
    }
    pub fn host_issuer(&self) -> &str {
        &self.host_issuer
    }
    pub fn may_own(&mut self, now: Tick) -> bool {
        if self.closed || now < self.last_tick || now.0 >= self.expires {
            self.closed = true;
            return false;
        }
        self.last_tick = now;
        true
    }
    /// Real writes on the shared FD, with owner checks after encoding and queue
    /// wait. Failure closes the whole session; no participant content is sent.
    pub fn acknowledge(
        &mut self,
        output: &Output,
        mut clock: impl FnMut() -> io::Result<Tick>,
    ) -> io::Result<()> {
        if self.acknowledged || self.closed {
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
                self.closed = true;
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
