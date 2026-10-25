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
    sent_at: Tick,
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
                sent_at: now,
                opening,
                grant,
                charge,
            },
        );
        Ok(raw)
    }
    // Fixed negative admission only. It cannot settle operations, join a
    // participant, advance permissions or mint a replacement gate/lease.
    pub fn decline(
        &mut self,
        raw: &[u8],
        host: &Transport<'_>,
        owner: &OwnerController,
        now: Tick,
    ) -> io::Result<()> {
        let Transport::AuthenticatedHost { issuer, .. } = host else {
            return Err(invalid("ticket decline transport"));
        };
        if !mooring_protocol::wire::valid_identifier(issuer)
            || !owner.native_humans()
            || !owner.may_own(now)
        {
            return Err(invalid("ticket decline owner"));
        }
        let reply = validate_ticket_decline(raw)?;
        let pending = self
            .pending
            .get(&reply.request_id)
            .ok_or_else(|| invalid("unrelated ticket decline"))?;
        let opening = &pending.opening;
        let versions = match &opening.join {
            SessionParticipantOpenJoin::SessionJoin(join) => &join.versions,
            SessionParticipantOpenJoin::SessionJoinBounded(join) => &join.versions,
        };
        if now.0 < pending.sent_at.0
            || reply.scope.as_ref() != Some(owner.scope())
            || opening.scope != *owner.scope()
            || opening.session_id != owner.session_id()
            || opening.runtime_ref != owner.runtime_ref()
            || opening.fence != owner.initial_owner().fence
            || versions != owner.versions()
        {
            return Err(invalid("ticket decline binding"));
        }
        let remaining = self
            .bytes
            .checked_sub(pending.charge)
            .ok_or_else(|| invalid("ticket decline accounting"))?;
        self.pending.remove(&reply.request_id);
        self.bytes = remaining;
        Ok(())
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
            if p.permission < owner.known_permission() {
                p.lease.revoke();
                p.maintenance.abandon();
                self.bytes -= p.renewal_charge;
                p.renewal_charge = 0;
                continue;
            }
            if !p.lease.may_deliver(now) {
                if !p.lease.is_expired() {
                    p.lease.revoke();
                    p.maintenance.abandon();
                    self.bytes -= p.renewal_charge;
                    p.renewal_charge = 0;
                }
                // Genuine expiry keeps the ONE original pending/completed
                // correlation proof fully charged until reply or Leave.
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
        if !owner.may_own(now) || p.permission < owner.known_permission() {
            p.lease.revoke();
            return Err(invalid("delivery grant cannot revive closed gate"));
        }
        let live = p.lease.may_deliver(now);
        if !live && !p.lease.is_expired() {
            return Err(invalid("delivery grant cannot revive closed gate"));
        }
        // Already fully verified exact bytes remain harmless even after this
        // recipient expires. They never replace a different pending request.
        if p.maintenance.completed == raw {
            return Ok(());
        }
        if !live {
            p.lease
                .accept_expired_reply(raw, host, now)
                .map_err(|_| invalid("expired delivery grant binding"))?;
            update_charge(&mut self.bytes, p, raw.len())?;
            p.maintenance.pending = None;
            p.maintenance.completed = raw.to_vec();
            return Ok(()); // Denial-only completion; no scheduling or grant.
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

// This is deliberately not part of generic private control validation. Only
// the selected inherited native Actor lane admits the fixed negative shape.
pub(crate) fn validate_ticket_decline(raw: &[u8]) -> io::Result<mooring_protocol::ProtocolError> {
    let normalized = mooring_protocol::wire::validate_bounded_envelope(raw)
        .map_err(|_| invalid("ticket decline shape"))?;
    let reply: mooring_protocol::ProtocolError =
        serde_json::from_slice(normalized.bytes()).map_err(|_| invalid("ticket decline shape"))?;
    if reply.scope.is_none()
        || reply.reason.code != mooring_protocol::ErrorCode::PermissionRevoked
        || reply.reason.retry != mooring_protocol::RejectionRetry::DoNotRetry
        || reply.reason.message != "participant permission revoked"
        || reply.reason.root_op_id.is_some()
        || reply.reason.root_cause.is_some()
    {
        return Err(invalid("ticket decline purpose"));
    }
    Ok(reply)
}

#[cfg(test)]
mod expiry_tests {
    use super::*;
    use crate::{session_confirmed::OwnerController, session_owner_guard::OwnerGuard};
    use serde_json::{json, Value};

    fn at(ms: u64) -> Tick {
        Tick(Duration::from_millis(ms))
    }
    fn host() -> Transport<'static> {
        Transport::AuthenticatedHost {
            issuer: "crewship",
            durabilities: &[mooring_protocol::boundary::Durability::Node],
            required: mooring_protocol::boundary::Durability::Node,
        }
    }
    // Controlled owner metadata; real Participants/PendingGrant admission and
    // retained accounting. This is not external human/SQL authority evidence.
    fn fixture() -> (Participants, OwnerController) {
        let open: mooring_protocol::SessionOpen = serde_json::from_value(json!({"type":"session_open","request_id":"open","control_profile":"mooring.session-causal.v1",
            "scope":{"workspace_id":"w","artifact_id":"d","application_id":"wordcraft"},"session_id":"s","runtime_ref":"runtime",
            "required_capabilities":["checkpoint_identity_v1"],"versions":crate::dispatch::wordcraft_versions()})).unwrap();
        let grant: mooring_protocol::OwnerGrant = serde_json::from_value(json!({"type":"owner_grant","request_id":"owner","scope":open.scope,"session_id":"s","runtime_ref":"runtime",
            "fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"lease_duration_ms":"60000","revision":"0","renewal_nonce":"owner","permission_version":"3"})).unwrap();
        let guard = OwnerGuard::new(&open, &grant, "crewship", Duration::from_secs(60), at(0));
        let owner = OwnerController::from_parts(open, grant, "crewship".into(), guard);
        let opening: SessionParticipantOpen = serde_json::from_value(json!({"type":"session_participant_open","request_id":"participant-open","control_profile":"mooring.session-causal.v1",
            "scope":owner.scope(),"session_id":"s","runtime_ref":"runtime","fence":owner.initial_owner().fence,"connection_ref":"c",
            "join":{"type":"session_join","request_id":"join","ticket":"MOCK_BOUND_HOST_TICKET","versions":owner.versions()}})).unwrap();
        let mut registry = Participants::new();
        let request: Value =
            serde_json::from_slice(&registry.begin(opening, &owner, at(0)).unwrap()).unwrap();
        let ticket = json!({"type":"ticket_validated","request_id":request["request_id"],"scope":owner.scope(),"session_id":"s","generation":"7",
            "participant_ref":"p","permission_version":"3","granted":{"read":true,"write":true},"versions":owner.versions(),"expires_in_ms":"2000","validation_nonce":request["validation_nonce"],"gate_id":"g"});
        registry
            .accept(
                &serde_json::to_vec(&ticket).unwrap(),
                &host(),
                &owner,
                at(0),
            )
            .unwrap();
        (registry, owner)
    }
    fn response(raw: &[u8]) -> Vec<u8> {
        let q: Value = serde_json::from_slice(raw).unwrap();
        serde_json::to_vec(&json!({"type":"delivery_grant","request_id":q["request_id"],"scope":q["scope"],"session_id":q["session_id"],"participant_ref":q["participant_ref"],
            "generation":q["fence"]["generation"],"permission_version":q["permission_version"],"renewal_nonce":q["renewal_nonce"],"duration_ms":"2000","gate_id":q["gate_id"]})).unwrap()
    }
    fn leave(owner: &OwnerController) -> SessionParticipantInput {
        serde_json::from_value(json!({"type":"session_participant_input","request_id":"leave","control_profile":"mooring.session-causal.v1",
            "scope":owner.scope(),"session_id":"s","runtime_ref":"runtime","fence":owner.initial_owner().fence,"connection_ref":"c","participant_ref":"p","gate_id":"g","permission_version":"3",
            "message":{"type":"session_leave","request_id":"public-leave","scope":owner.scope(),"session_id":"s"}})).unwrap()
    }
    #[test]
    fn expired_pending_proof_stays_charged_until_bound_reply_and_leave() {
        let (mut registry, owner) = fixture();
        let admission = registry.bytes;
        let first = registry.renewals(&owner, at(250)).unwrap().remove(0).1;
        registry
            .accept_renewal(&response(&first), &host(), &owner, at(260))
            .unwrap();
        let second = registry.renewals(&owner, at(500)).unwrap().remove(0).1;
        let charged = registry.bytes;
        let renewal_charge = registry.live["c"].renewal_charge;
        assert!(renewal_charge >= second.len() * 2);
        assert_eq!(charged, admission + renewal_charge);
        for now in [2250, 2251, 2300] {
            assert!(registry.renewals(&owner, at(now)).unwrap().is_empty());
            assert_eq!(registry.bytes, charged, "expiry forgot owned pending bytes");
            assert!(registry.live["c"].lease.is_expired());
            assert_eq!(registry.live["c"].renewal_charge, renewal_charge);
        }
        let raw = response(&second);
        registry
            .accept_renewal(&raw, &host(), &owner, at(2300))
            .unwrap();
        assert_eq!(registry.bytes, admission + raw.len());
        assert_eq!(registry.live["c"].maintenance.completed, raw);
        assert!(registry.live["c"].maintenance.pending.is_none());
        for _ in 0..64 {
            registry
                .accept_renewal(&raw, &host(), &owner, at(2600))
                .unwrap();
            assert_eq!(registry.bytes, admission + raw.len());
            assert!(registry.renewals(&owner, at(2600)).unwrap().is_empty());
            assert!(!registry
                .live
                .get_mut("c")
                .unwrap()
                .lease
                .may_deliver(at(2600)));
        }
        registry.leave(&leave(&owner), &owner, at(2600)).unwrap();
        assert_eq!(registry.bytes, 0);
        assert!(registry.live.is_empty());
        assert!(registry
            .accept_renewal(&raw, &host(), &owner, at(2600))
            .is_err());
    }
    #[test]
    fn expired_mismatched_reply_cannot_consume_proof_or_replace_cached_result() {
        let (mut registry, owner) = fixture();
        let pending = registry.renewals(&owner, at(250)).unwrap().remove(0).1;
        assert!(registry.renewals(&owner, at(2000)).unwrap().is_empty());
        let charged = registry.bytes;
        let valid = response(&pending);
        let mut wrong: Value = serde_json::from_slice(&valid).unwrap();
        wrong["renewal_nonce"] = json!("foreign-sequence");
        assert!(registry
            .accept_renewal(
                &serde_json::to_vec(&wrong).unwrap(),
                &host(),
                &owner,
                at(2300)
            )
            .is_err());
        assert_eq!(registry.bytes, charged);
        assert!(registry.live["c"].maintenance.pending.is_some());
        registry
            .accept_renewal(&valid, &host(), &owner, at(2300))
            .unwrap();
        let after = registry.bytes;
        let mut contradiction: Value = serde_json::from_slice(&valid).unwrap();
        contradiction["duration_ms"] = json!("1999");
        assert!(registry
            .accept_renewal(
                &serde_json::to_vec(&contradiction).unwrap(),
                &host(),
                &owner,
                at(2300)
            )
            .is_err());
        assert_eq!(registry.bytes, after);
        assert_eq!(registry.live["c"].maintenance.completed, valid);
        registry
            .accept_renewal(&valid, &host(), &owner, at(2300))
            .unwrap();
        assert!(!registry
            .live
            .get_mut("c")
            .unwrap()
            .lease
            .may_deliver(at(2300)));
    }
}
