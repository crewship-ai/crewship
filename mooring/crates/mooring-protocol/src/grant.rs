//! Host grant replies bound to immutable first-send request metadata.
//! This does not authenticate a channel, register a gate, or send content.
use crate::{
    boundary::{BoundaryError, Transport},
    canonical::CanonicalValue,
    wire, Counter, DeliveryRenew, TicketValidationResult, Versions,
};
use serde_json::Value;
use std::{fmt, time::Duration};

enum Kind {
    Delivery,
    OwnerRenew {
        runtime_ref: String,
    },
    OwnerAcquire {
        minimum_generation: Counter,
    },
    Ticket {
        versions: Versions,
        minimum_permission: Counter,
    },
}

/// Create exactly once for a NEW nonce at its first send. Retries use
/// bytes_for_retry() on this same object; they never reset its private clock.
/// The runtime/controller must enforce nonce freshness and retain this object.
/// Native callers must use CLOCK_BOOTTIME, failing closed if unavailable.
pub struct PendingGrant {
    request: CanonicalValue,
    kind: Kind,
    sent_at: Duration,
    maximum: Duration,
}

/// Metadata-validated host reply. Deadline is relative to the caller's trusted
/// clock epoch; only the stateful gate/controller may authorize content with it.
/// Debug deliberately excludes owner fence tokens and ticket/participant data.
pub struct BoundGrant {
    reply: CanonicalValue,
    expires_at: Duration,
    duration: Duration,
    host_issuer: String,
}
impl fmt::Debug for BoundGrant {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("BoundGrant")
            .field("expires_at", &self.expires_at)
            .finish_non_exhaustive()
    }
}
/// A consumed ticket reply, still dependent on unique-gate admission by the host
/// integration. Its digest is only a bounded nonce namespace, not authority.
pub struct BoundTicket {
    pub ticket: TicketValidationResult,
    pub first_send: Duration,
    pub duration: Duration,
    pub nonce_namespace: String,
    pub host_issuer: String,
}
impl BoundGrant {
    pub fn into_ticket(self) -> Result<BoundTicket, BoundaryError> {
        if self.reply.value()["type"] != "ticket_validated" {
            return Err(BoundaryError::Binding);
        }
        let ticket = serde_json::from_value(self.reply.value().clone())
            .map_err(|_| BoundaryError::Binding)?;
        Ok(BoundTicket {
            ticket,
            first_send: self
                .expires_at
                .checked_sub(self.duration)
                .ok_or(BoundaryError::Clock)?,
            duration: self.duration,
            nonce_namespace: self.reply.digest(),
            host_issuer: self.host_issuer,
        })
    }

    pub fn value(&self) -> &Value {
        self.reply.value()
    }
    pub fn expires_at(&self) -> Duration {
        self.expires_at
    }
    pub fn duration(&self) -> Duration {
        self.duration
    }
}
impl PendingGrant {
    pub fn delivery_typed(
        request: &DeliveryRenew,
        sent_at: Duration,
        maximum: Duration,
    ) -> Result<Self, BoundaryError> {
        let raw = serde_json::to_vec(request).map_err(|_| BoundaryError::Binding)?;
        Self::delivery(&raw, sent_at, maximum)
    }

    pub fn delivery(
        raw: &[u8],
        sent_at: Duration,
        maximum: Duration,
    ) -> Result<Self, BoundaryError> {
        Self::new(raw, "delivery_renew", Kind::Delivery, sent_at, maximum)
    }
    pub fn owner_renew(
        raw: &[u8],
        runtime_ref: &str,
        sent_at: Duration,
        maximum: Duration,
    ) -> Result<Self, BoundaryError> {
        if !wire::valid_identifier(runtime_ref) {
            return Err(BoundaryError::Binding);
        }
        Self::new(
            raw,
            "owner_renew",
            Kind::OwnerRenew {
                runtime_ref: runtime_ref.into(),
            },
            sent_at,
            maximum,
        )
    }
    /// An initial acquire has no pre-existing session/fence to compare. Those
    /// are assigned by the authenticated host, not copied from a participant.
    pub fn owner_acquire(
        raw: &[u8],
        minimum_generation: Counter,
        sent_at: Duration,
        maximum: Duration,
    ) -> Result<Self, BoundaryError> {
        Self::new(
            raw,
            "owner_acquire",
            Kind::OwnerAcquire { minimum_generation },
            sent_at,
            maximum,
        )
    }
    /// Initial ticket validation assigns participant_ref/gate_id/permissions.
    /// Versions and the last observed permission floor remain independently bound.
    pub fn ticket(
        raw: &[u8],
        versions: Versions,
        minimum_permission: Counter,
        sent_at: Duration,
        maximum: Duration,
    ) -> Result<Self, BoundaryError> {
        if [
            &versions.protocol_version,
            &versions.model_version,
            &versions.engine_version,
        ]
        .iter()
        .any(|version| !crate::shape::version(version))
        {
            return Err(BoundaryError::Binding);
        }
        Self::new(
            raw,
            "ticket_validate",
            Kind::Ticket {
                versions,
                minimum_permission,
            },
            sent_at,
            maximum,
        )
    }
    fn new(
        raw: &[u8],
        expected_type: &str,
        kind: Kind,
        sent_at: Duration,
        maximum: Duration,
    ) -> Result<Self, BoundaryError> {
        if maximum.is_zero() {
            return Err(BoundaryError::Capacity);
        }
        let request = wire::validate_bounded_envelope(raw).map_err(BoundaryError::Wire)?;
        if request.value()["type"] != expected_type {
            return Err(BoundaryError::Binding);
        }
        Ok(Self {
            request,
            kind,
            sent_at,
            maximum,
        })
    }
    /// Contains private owner/ticket material. Send only on the trusted host
    /// channel; never log it. Same bytes/nonce retain the ORIGINAL send bound.
    pub fn bytes_for_retry(&self) -> &[u8] {
        self.request.bytes()
    }

    pub fn validate_reply(
        &self,
        raw: &[u8],
        transport: &Transport<'_>,
        received_at: Duration,
    ) -> Result<BoundGrant, BoundaryError> {
        let Transport::AuthenticatedHost { issuer, .. } = transport else {
            return Err(BoundaryError::Forbidden);
        };
        if !wire::valid_identifier(issuer) {
            return Err(BoundaryError::Forbidden);
        }
        if received_at < self.sent_at {
            return Err(BoundaryError::Clock);
        }
        let reply = wire::validate_bounded_envelope(raw).map_err(BoundaryError::Wire)?;
        let value = reply.value();
        let request = self.request.value();
        if value["scope"] != request["scope"] {
            return Err(BoundaryError::Forbidden);
        }
        if value["request_id"] != request["request_id"] {
            return Err(BoundaryError::StaleOwner);
        }
        let duration_field = match &self.kind {
            Kind::Delivery => {
                equal(value, "type", &Value::String("delivery_grant".into()))?;
                same(
                    value,
                    request,
                    &[
                        "session_id",
                        "participant_ref",
                        "permission_version",
                        "gate_id",
                        "renewal_nonce",
                    ],
                )?;
                equal(value, "generation", &request["fence"]["generation"])?;
                "duration_ms"
            }
            Kind::OwnerRenew { runtime_ref } => {
                equal(value, "type", &Value::String("owner_grant".into()))?;
                same(value, request, &["session_id", "fence", "renewal_nonce"])?;
                equal(value, "runtime_ref", &Value::String(runtime_ref.clone()))?;
                // Owner renewal reports the host's current permission epoch.
                // This only raises owner knowledge; participant grants remain
                // separately bound and are never renewed by this response.
                if counter(&value["permission_version"])? < counter(&request["permission_version"])?
                {
                    return Err(BoundaryError::PermissionRevoked);
                }
                "lease_duration_ms"
            }
            Kind::OwnerAcquire { minimum_generation } => {
                equal(value, "type", &Value::String("owner_grant".into()))?;
                same(value, request, &["runtime_ref", "renewal_nonce"])?;
                if counter(&value["fence"]["generation"])? <= minimum_generation.0 {
                    return Err(BoundaryError::StaleOwner);
                }
                "lease_duration_ms"
            }
            Kind::Ticket {
                versions,
                minimum_permission,
            } => {
                equal(value, "type", &Value::String("ticket_validated".into()))?;
                same(value, request, &["session_id", "validation_nonce"])?;
                equal(value, "generation", &request["fence"]["generation"])?;
                if value["versions"]
                    != serde_json::to_value(versions).map_err(|_| BoundaryError::Binding)?
                {
                    return Err(BoundaryError::VersionMismatch);
                }
                if counter(&value["permission_version"])? < minimum_permission.0 {
                    return Err(BoundaryError::PermissionRevoked);
                }
                "expires_in_ms"
            }
        };
        let duration = Duration::from_millis(counter(&value[duration_field])?);
        if duration.is_zero() || duration > self.maximum {
            return Err(BoundaryError::Capacity);
        }
        let expires_at = self
            .sent_at
            .checked_add(duration)
            .ok_or(BoundaryError::Clock)?;
        if received_at >= expires_at {
            return Err(BoundaryError::Expired);
        }
        Ok(BoundGrant {
            reply,
            expires_at,
            duration,
            host_issuer: (*issuer).into(),
        })
    }
}
fn counter(value: &Value) -> Result<u64, BoundaryError> {
    value
        .as_str()
        .and_then(|text| text.parse().ok())
        .ok_or(BoundaryError::Binding)
}
fn equal(value: &Value, field: &str, expected: &Value) -> Result<(), BoundaryError> {
    if &value[field] != expected {
        return Err(BoundaryError::StaleOwner);
    }
    Ok(())
}
fn same(value: &Value, request: &Value, fields: &[&str]) -> Result<(), BoundaryError> {
    for field in fields {
        equal(value, field, &request[field])?;
    }
    Ok(())
}
