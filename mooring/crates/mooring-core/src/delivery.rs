//! Stateful read-lease controller. Only verified admission may register one
//! controller for a fresh host gate. Recreating a controller for that same gate
//! is forbidden: admission owns this uniqueness across runtime restarts.
use crate::{
    batch::Tick,
    lease::{DeliveryGate, LeaseError, Renewal},
};
use mooring_protocol::{
    boundary::{BoundaryError, Transport},
    grant::{BoundGrant, PendingGrant},
    DeliveryRenew, DeliveryRenewType, Fence, TicketValidationResult,
};
use std::time::Duration;

#[derive(Debug, PartialEq, Eq)]
pub enum DeliveryError {
    Closed,
    Pending,
    Binding,
    Grant(BoundaryError),
    Lease(LeaseError),
}

struct Pending {
    grant: PendingGrant,
    renewal: Renewal,
}

/// Not Clone/Deserialize. The integration consumes the authenticated ticket
/// exactly once, retains this object, and calls may_deliver before EVERY send.
/// This controls read-lease expiry; history/export/asset permissions and scope
/// routing still require independent host admission. No network route uses it yet.
pub struct ReadLease {
    ticket: TicketValidationResult,
    fence: Fence,
    namespace: String,
    sequence: u64,
    maximum: Duration,
    gate: DeliveryGate,
    pending: Option<Pending>,
    closed: bool,
}

impl ReadLease {
    pub fn admit(
        ticket: BoundGrant,
        fence: Fence,
        maximum: Duration,
        now: Tick,
    ) -> Result<Self, DeliveryError> {
        let bound = ticket.into_ticket().map_err(DeliveryError::Grant)?;
        if maximum.is_zero()
            || bound.duration > maximum
            || bound.ticket.generation != fence.generation
            || bound.ticket.granted.read != Some(true)
        {
            return Err(DeliveryError::Binding);
        }
        // Fence is supplied by verified owner admission, never by a participant.
        // Ticket generation was already checked against its validation request.
        PendingGrant::delivery_typed(
            &DeliveryRenew {
                r#type: DeliveryRenewType::DeliveryRenew,
                request_id: "admission-shape-check".into(),
                scope: bound.ticket.scope.clone(),
                session_id: bound.ticket.session_id.clone(),
                participant_ref: bound.ticket.participant_ref.clone(),
                fence: fence.clone(),
                permission_version: bound.ticket.permission_version,
                renewal_nonce: "admission-shape-check".into(),
                gate_id: bound.ticket.gate_id.clone(),
            },
            now.0,
            maximum,
        )
        .map_err(DeliveryError::Grant)?;
        let mut gate = DeliveryGate::default();
        let request = gate
            .request(Tick(bound.first_send))
            .map_err(DeliveryError::Lease)?;
        gate.renew(request, bound.duration, now)
            .map_err(DeliveryError::Lease)?;
        Ok(Self {
            ticket: bound.ticket,
            fence,
            namespace: bound.nonce_namespace,
            sequence: 0,
            maximum,
            gate,
            pending: None,
            closed: false,
        })
    }

    /// A new request owns a checked sequence; callers cannot nominate a nonce.
    /// No new request replaces a pending one. Retries use retry_bytes unchanged.
    pub fn begin(&mut self, now: Tick) -> Result<&[u8], DeliveryError> {
        self.ensure_open()?;
        if !self.may_deliver(now) {
            return Err(DeliveryError::Closed);
        }
        if self.pending.is_some() {
            return Err(DeliveryError::Pending);
        }
        let Some(sequence) = self.sequence.checked_add(1) else {
            self.revoke();
            return Err(DeliveryError::Closed);
        };
        self.sequence = sequence;
        let nonce = format!("{}:{sequence}", self.namespace);
        let request = DeliveryRenew {
            r#type: DeliveryRenewType::DeliveryRenew,
            request_id: nonce.clone(),
            scope: self.ticket.scope.clone(),
            session_id: self.ticket.session_id.clone(),
            participant_ref: self.ticket.participant_ref.clone(),
            fence: self.fence.clone(),
            permission_version: self.ticket.permission_version,
            renewal_nonce: nonce,
            gate_id: self.ticket.gate_id.clone(),
        };
        let renewal = self.gate.request(now).map_err(|error| {
            self.revoke();
            DeliveryError::Lease(error)
        })?;
        let grant =
            PendingGrant::delivery_typed(&request, now.0, self.maximum).map_err(|error| {
                self.revoke();
                DeliveryError::Grant(error)
            })?;
        self.pending = Some(Pending { grant, renewal });
        Ok(self.pending.as_ref().unwrap().grant.bytes_for_retry())
    }

    pub fn retry_bytes(&self) -> Result<&[u8], DeliveryError> {
        self.ensure_open()?;
        self.pending
            .as_ref()
            .map(|pending| pending.grant.bytes_for_retry())
            .ok_or(DeliveryError::Binding)
    }

    pub fn accept(
        &mut self,
        raw: &[u8],
        host: &Transport<'_>,
        now: Tick,
    ) -> Result<(), DeliveryError> {
        self.ensure_open()?;
        if !self.may_deliver(now) {
            return Err(DeliveryError::Closed);
        }
        let pending = self.pending.as_ref().ok_or(DeliveryError::Binding)?;
        let grant = pending
            .grant
            .validate_reply(raw, host, now.0)
            .map_err(DeliveryError::Grant);
        let grant = match grant {
            Ok(grant) => grant,
            Err(error @ DeliveryError::Grant(BoundaryError::Clock | BoundaryError::Expired)) => {
                self.revoke();
                return Err(error);
            }
            Err(error) => return Err(error), // Wrong/old reply leaves current request intact.
        };
        let renewal = pending.renewal;
        self.gate
            .renew(renewal, grant.duration(), now)
            .map_err(|error| {
                self.revoke();
                DeliveryError::Lease(error)
            })?;
        self.pending = None;
        Ok(())
    }

    pub fn may_deliver(&mut self, now: Tick) -> bool {
        if self.closed {
            return false;
        }
        if !self.gate.may_deliver(now) {
            // Expiry/clock failure requires a NEW authenticated gate, not a late renewal.
            self.revoke();
            return false;
        }
        true
    }

    pub fn revoke(&mut self) {
        self.closed = true;
        self.pending = None;
        self.gate.revoke();
    }
    fn ensure_open(&self) -> Result<(), DeliveryError> {
        if self.closed {
            Err(DeliveryError::Closed)
        } else {
            Ok(())
        }
    }
}

#[cfg(test)]
mod tests {
    include!("delivery_tests.rs");
}
