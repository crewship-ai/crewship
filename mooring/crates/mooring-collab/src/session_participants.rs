//! Actor-local gates obtained only from correlated host ticket validation.
//! This does not authenticate a browser or replace host sealed-op admission.
use crate::{dispatch::invalid, session_confirmed::OwnerController};
use mooring_core::{batch::Tick, delivery::ReadLease};
use mooring_protocol::{
    boundary::Transport, grant::PendingGrant, Capabilities, Counter, SessionParticipantInput,
    SessionParticipantOpen, SessionParticipantOpenJoin, TicketValidationRequest,
    TicketValidationRequestType,
};
use std::{
    collections::{BTreeMap, BTreeSet},
    io,
    time::Duration,
};

const LIVE: usize = mooring_protocol::session_control::MAX_PARTICIPANTS;
const SEEN: usize = 4096;
const BYTES: usize = mooring_protocol::session_control::MAX_GLOBAL_INPUT_BYTES;
const LEASE: Duration = Duration::from_millis(mooring_protocol::session_control::DELIVERY_LEASE_MS);

struct Pending {
    opening: SessionParticipantOpen,
    grant: PendingGrant,
    charge: usize,
}
pub(crate) struct Participant {
    pub participant: String,
    pub gate: String,
    pub permission: Counter,
    pub granted: Capabilities,
    pub bounded_transfer: bool,
    pub lease: ReadLease,
    charge: usize,
    maintenance: crate::participant_maintenance::Maintenance,
    renewal_charge: usize,
}
pub(crate) struct Participants {
    pending: BTreeMap<String, Pending>,
    pub live: BTreeMap<String, Participant>,
    seen_gates: BTreeSet<String>,
    sequence: u64,
    bytes: usize,
}
impl Participants {
    pub fn new() -> Self {
        Self {
            pending: BTreeMap::new(),
            live: BTreeMap::new(),
            seen_gates: BTreeSet::new(),
            sequence: 0,
            bytes: 0,
        }
    }
    pub fn begin(
        &mut self,
        opening: SessionParticipantOpen,
        owner: &OwnerController,
        now: Tick,
    ) -> io::Result<Vec<u8>> {
        if opening.scope != *owner.scope()
            || opening.session_id != owner.session_id()
            || opening.runtime_ref != owner.runtime_ref()
            || opening.fence != owner.initial_owner().fence
            || !owner.may_own(now)
            || self.pending.len() + self.live.len() >= LIVE
            || self.live.contains_key(&opening.connection_ref)
            || self
                .pending
                .values()
                .any(|p| p.opening.connection_ref == opening.connection_ref)
        {
            return Err(invalid("invalid participant opening"));
        }
        let (ticket, versions) = match &opening.join {
            SessionParticipantOpenJoin::SessionJoin(join) => (&join.ticket, &join.versions),
            SessionParticipantOpenJoin::SessionJoinBounded(join) => (&join.ticket, &join.versions),
        };
        if versions != owner.versions() {
            return Err(invalid("participant versions mismatch"));
        }
        let sequence = self
            .sequence
            .checked_add(1)
            .ok_or_else(|| invalid("ticket nonce exhausted"))?;
        let id = format!("actor-ticket:{sequence}");
        let request = TicketValidationRequest {
            r#type: TicketValidationRequestType::TicketValidate,
            request_id: id.clone(),
            validation_nonce: id.clone(),
            scope: owner.scope().clone(),
            session_id: owner.session_id().into(),
            fence: owner.initial_owner().fence.clone(),
            ticket: ticket.clone(),
        };
        let raw = serde_json::to_vec(&request).map_err(|_| invalid("ticket request encoding"))?;
        let charge = raw
            .len()
            .checked_add(
                serde_json::to_vec(&opening)
                    .map_err(|_| invalid("opening encoding"))?
                    .len(),
            )
            .and_then(|x| x.checked_mul(2))
            .ok_or_else(|| invalid("participant accounting"))?;
        if charge > BYTES - self.bytes {
            return Err(invalid("participant capacity"));
        }
        let grant = PendingGrant::ticket(
            &raw,
            owner.versions().clone(),
            owner.known_permission(),
            now.0,
            LEASE,
        )
        .map_err(|_| invalid("ticket binding"))?;
        self.sequence = sequence;
        self.bytes += charge;
        self.pending.insert(
            id,
            Pending {
                opening,
                grant,
                charge,
            },
        );
        Ok(raw)
    }
    /// Consumes one correlated host reply. Replaying a ticket/gate cannot create
    /// a fresh lease controller or reset its original request-send deadline.
    pub fn accept(
        &mut self,
        raw: &[u8],
        host: &Transport<'_>,
        owner: &OwnerController,
        now: Tick,
    ) -> io::Result<(SessionParticipantOpen, String)> {
        let response: mooring_protocol::TicketValidationResult =
            serde_json::from_slice(raw).map_err(|_| invalid("ticket response shape"))?;
        let pending = self
            .pending
            .get(&response.request_id)
            .ok_or_else(|| invalid("unrelated ticket response"))?;
        if !owner.may_own(now)
            || response.permission_version < owner.known_permission()
            || response.granted.read != Some(true)
            || self.seen_gates.contains(&response.gate_id)
            || self.seen_gates.len() >= SEEN
        {
            return Err(invalid("ticket reply is not current admission"));
        }
        let bound = pending
            .grant
            .validate_reply(raw, host, now.0)
            .map_err(|_| invalid("ticket response binding"))?;
        let first = bound
            .expires_at()
            .checked_sub(bound.duration())
            .ok_or_else(|| invalid("ticket first-send clock"))?;
        let maintenance =
            crate::participant_maintenance::Maintenance::new(Tick(first), bound.duration())?;
        let lease = ReadLease::admit(bound, owner.initial_owner().fence.clone(), LEASE, now)
            .map_err(|_| invalid("ticket read lease"))?;
        let retained = raw
            .len()
            .checked_mul(2)
            .and_then(|x| x.checked_add(response.gate_id.len()))
            .ok_or_else(|| invalid("participant accounting"))?;
        if retained > BYTES - (self.bytes - pending.charge) {
            return Err(invalid("participant capacity"));
        }
        let pending = self.pending.remove(&response.request_id).unwrap();
        self.bytes = self.bytes - pending.charge + retained;
        self.seen_gates.insert(response.gate_id.clone());
        let connection = pending.opening.connection_ref.clone();
        self.live.insert(
            connection.clone(),
            Participant {
                participant: response.participant_ref,
                gate: response.gate_id,
                permission: response.permission_version,
                granted: response.granted,
                bounded_transfer: matches!(
                    pending.opening.join,
                    SessionParticipantOpenJoin::SessionJoinBounded(_)
                ),
                lease,
                charge: retained,
                maintenance,
                renewal_charge: 0,
            },
        );
        Ok((pending.opening, connection))
    }
    /// Scheduling creates no rights. Every fresh/retried request remains owned
    /// by the original ReadLease and must pass a final live check before write.
    pub fn renewals(
        &mut self,
        owner: &OwnerController,
        now: Tick,
    ) -> io::Result<Vec<(String, Vec<u8>)>> {
        if !owner.may_own(now) {
            return Err(invalid("owner expired before delivery renewal"));
        }
        let mut ready = Vec::new();
        for (connection, p) in &mut self.live {
            if p.permission < owner.known_permission() || !p.lease.may_deliver(now) {
                p.lease.revoke();
                p.maintenance.abandon();
                self.bytes -= p.renewal_charge;
                p.renewal_charge = 0;
                continue;
            }
            let raw = if p.maintenance.begin_due(now) {
                let raw = p
                    .lease
                    .begin(now)
                    .map_err(|_| invalid("delivery renewal start"))?
                    .to_vec();
                p.maintenance.started(now)?;
                let charge = raw
                    .len()
                    .checked_mul(2)
                    .and_then(|n| n.checked_add(p.maintenance.completed.len()))
                    .ok_or_else(|| invalid("delivery metadata bound"))?;
                update_charge(&mut self.bytes, p, charge)?;
                Some(raw)
            } else if p.maintenance.retry_due(now) {
                let raw = p
                    .lease
                    .retry_bytes()
                    .map_err(|_| invalid("delivery renewal retry"))?
                    .to_vec();
                p.maintenance.retried(now)?;
                Some(raw)
            } else {
                None
            };
            if let Some(raw) = raw {
                ready.push((connection.clone(), raw));
            }
        }
        Ok(ready)
    }
    pub fn accept_renewal(
        &mut self,
        raw: &[u8],
        host: &Transport<'_>,
        owner: &OwnerController,
        now: Tick,
    ) -> io::Result<()> {
        let response: mooring_protocol::DeliveryGrant =
            serde_json::from_slice(raw).map_err(|_| invalid("delivery grant shape"))?;
        let p = self
            .live
            .values_mut()
            .find(|p| p.gate == response.gate_id && p.participant == response.participant_ref)
            .ok_or_else(|| invalid("unrelated delivery grant"))?;
        if !owner.may_own(now)
            || p.permission < owner.known_permission()
            || !p.lease.may_deliver(now)
        {
            p.lease.revoke();
            return Err(invalid("delivery grant cannot revive closed gate"));
        }
        // One exact completed canonical response is harmless. It neither
        // changes deadline nor suppresses a different current pending nonce.
        if p.maintenance.completed == raw {
            return Ok(());
        }
        p.lease
            .accept(raw, host, now)
            .map_err(|_| invalid("delivery grant binding"))?;
        update_charge(&mut self.bytes, p, raw.len())?;
        p.maintenance
            .completed(Duration::from_millis(response.duration_ms.0), raw.to_vec())
    }
    pub fn wait(&mut self, owner: &OwnerController, now: Tick, maximum: Duration) -> Duration {
        let mut wait = maximum;
        for p in self.live.values_mut() {
            if p.permission >= owner.known_permission() && p.lease.may_deliver(now) {
                wait = p.maintenance.wait(now, wait);
            }
        }
        wait
    }
    pub fn input(
        &mut self,
        input: &SessionParticipantInput,
        owner: &OwnerController,
        now: Tick,
    ) -> io::Result<&mut Participant> {
        if input.scope != *owner.scope()
            || input.session_id != owner.session_id()
            || input.runtime_ref != owner.runtime_ref()
            || input.fence != owner.initial_owner().fence
            || !owner.may_own(now)
        {
            return Err(invalid("participant input session binding"));
        }
        let participant = self
            .live
            .get_mut(&input.connection_ref)
            .ok_or_else(|| invalid("participant is not admitted"))?;
        if participant.participant != input.participant_ref
            || participant.gate != input.gate_id
            || participant.permission != input.permission_version
            || participant.permission < owner.known_permission()
            || !participant.lease.may_deliver(now)
        {
            return Err(invalid("participant input gate binding"));
        }
        Ok(participant)
    }
    pub fn may_send(
        &mut self,
        connection: &str,
        owner: &OwnerController,
        now: Tick,
    ) -> io::Result<()> {
        if !owner.may_own(now) {
            return Err(invalid("owner expired before participant send"));
        }
        let p = self
            .live
            .get_mut(connection)
            .ok_or_else(|| invalid("participant was closed"))?;
        if p.permission < owner.known_permission() || !p.lease.may_deliver(now) {
            return Err(invalid("participant delivery expired"));
        }
        Ok(())
    }
    pub fn leave(
        &mut self,
        input: &SessionParticipantInput,
        owner: &OwnerController,
        now: Tick,
    ) -> io::Result<()> {
        // Closing a bound connection grants no read/write authority and is
        // valid even after its lease expired. Never recreate its old gate.
        if input.scope != *owner.scope()
            || input.session_id != owner.session_id()
            || input.runtime_ref != owner.runtime_ref()
            || input.fence != owner.initial_owner().fence
            || !owner.may_own(now)
        {
            return Err(invalid("participant leave session binding"));
        }
        let p = self
            .live
            .get(&input.connection_ref)
            .ok_or_else(|| invalid("participant is not admitted"))?;
        if p.participant != input.participant_ref
            || p.gate != input.gate_id
            || p.permission != input.permission_version
        {
            return Err(invalid("participant leave gate binding"));
        }
        let charge = p
            .charge
            .checked_add(p.renewal_charge)
            .ok_or_else(|| invalid("participant accounting"))?;
        let remaining = self
            .bytes
            .checked_sub(charge)
            .ok_or_else(|| invalid("participant accounting"))?;
        let mut p = self.live.remove(&input.connection_ref).unwrap();
        p.lease.revoke();
        self.bytes = remaining;
        Ok(())
    }
    pub fn close(&mut self) {
        for participant in self.live.values_mut() {
            participant.lease.revoke();
        }
        self.pending.clear();
    }
}

fn update_charge(bytes: &mut usize, p: &mut Participant, charge: usize) -> io::Result<()> {
    let base = bytes
        .checked_sub(p.renewal_charge)
        .ok_or_else(|| invalid("delivery metadata accounting"))?;
    if charge
        > BYTES
            .checked_sub(base)
            .ok_or_else(|| invalid("delivery metadata accounting"))?
    {
        return Err(invalid("delivery metadata capacity"));
    }
    *bytes = base + charge;
    p.renewal_charge = charge;
    Ok(())
}
