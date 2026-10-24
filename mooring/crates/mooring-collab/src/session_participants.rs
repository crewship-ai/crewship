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
            },
        );
        Ok((pending.opening, connection))
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
    pub fn close(&mut self) {
        for participant in self.live.values_mut() {
            participant.lease.revoke();
        }
        self.pending.clear();
    }
}
