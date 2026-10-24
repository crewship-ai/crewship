//! Lease-only interior state. The confirmed application document is never
//! borrowed mutably through this controller, and owner knowledge grants no
//! participant rights. A fresh host runtime_ref admits exactly one controller.
use crate::dispatch::invalid;
use mooring_core::batch::Tick;
use mooring_protocol::{
    boundary::Transport, grant::PendingGrant, Counter, OwnerGrant, OwnerRenew, OwnerRenewType,
    SessionOpen,
};
use std::{cell::RefCell, io, time::Duration};

pub struct PermissionAdvance {
    pub previous: Counter,
    pub current: Counter,
}
struct State {
    expires: Duration,
    last_tick: Tick,
    permission: Counter,
    sequence: u64,
    pending: Option<PendingGrant>,
    closed: bool,
}
pub(crate) struct OwnerGuard {
    open: SessionOpen,
    owner: OwnerGrant,
    host_issuer: String,
    namespace: String,
    state: RefCell<State>,
}
impl OwnerGuard {
    // Only owned startup calls this after authenticating/correlating the initial
    // send-bound grant and validating its exact session and restoration.
    pub(crate) fn new(
        open: &SessionOpen,
        owner: &OwnerGrant,
        host_issuer: &str,
        expires: Duration,
        last_tick: Tick,
    ) -> Self {
        Self {
            open: open.clone(),
            owner: owner.clone(),
            host_issuer: host_issuer.into(),
            namespace: format!("{}-renew", owner.renewal_nonce),
            state: RefCell::new(State {
                expires,
                last_tick,
                permission: owner.permission_version,
                sequence: 0,
                pending: None,
                closed: false,
            }),
        }
    }
    pub(crate) fn close(&self) {
        let mut s = self.state.borrow_mut();
        s.closed = true;
        s.pending = None;
    }
    pub(crate) fn is_closed(&self) -> bool {
        self.state.borrow().closed
    }
    pub(crate) fn may_own(&self, now: Tick) -> bool {
        let mut s = self.state.borrow_mut();
        if s.closed || now < s.last_tick || now.0 >= s.expires {
            s.closed = true;
            s.pending = None;
            return false;
        }
        s.last_tick = now;
        true
    }
    pub(crate) fn permission(&self) -> Counter {
        self.state.borrow().permission
    }
    pub(crate) fn matches_permission(&self, version: Counter, now: Tick) -> bool {
        self.may_own(now) && self.permission() == version
    }
    pub(crate) fn begin(&self, now: Tick) -> io::Result<Vec<u8>> {
        if !self.may_own(now) {
            return Err(invalid("owner closed"));
        }
        let mut s = self.state.borrow_mut();
        if s.pending.is_some() {
            return Err(invalid("owner renewal already pending"));
        }
        let Some(sequence) = s.sequence.checked_add(1) else {
            s.closed = true;
            return Err(invalid("owner nonce exhausted"));
        };
        s.sequence = sequence;
        let nonce = format!("{}:{sequence}", self.namespace);
        let request = OwnerRenew {
            r#type: OwnerRenewType::OwnerRenew,
            request_id: nonce.clone(),
            scope: self.open.scope.clone(),
            session_id: self.open.session_id.clone(),
            fence: self.owner.fence.clone(),
            permission_version: s.permission,
            renewal_nonce: nonce,
        };
        let raw = serde_json::to_vec(&request).map_err(|_| invalid("owner renewal encoding"))?;
        let pending =
            PendingGrant::owner_renew(&raw, &self.open.runtime_ref, now.0, Duration::from_secs(60))
                .map_err(|_| invalid("invalid owner renewal"))?;
        let bytes = pending.bytes_for_retry().to_vec();
        s.pending = Some(pending);
        Ok(bytes)
    }
    pub(crate) fn retry(&self, now: Tick) -> io::Result<Vec<u8>> {
        if !self.may_own(now) {
            return Err(invalid("owner closed"));
        }
        self.state
            .borrow()
            .pending
            .as_ref()
            .map(|p| p.bytes_for_retry().to_vec())
            .ok_or_else(|| invalid("no owner renewal pending"))
    }
    pub(crate) fn accept(
        &self,
        raw: &[u8],
        host: &Transport<'_>,
        now: Tick,
    ) -> io::Result<PermissionAdvance> {
        if !self.may_own(now) {
            return Err(invalid("owner closed"));
        }
        if !matches!(host,Transport::AuthenticatedHost{issuer,..} if *issuer==self.host_issuer) {
            return Err(invalid("owner issuer mismatch"));
        }
        let mut s = self.state.borrow_mut();
        let pending = s
            .pending
            .as_ref()
            .ok_or_else(|| invalid("no owner renewal pending"))?;
        let bound = pending
            .validate_reply(raw, host, now.0)
            .map_err(|_| invalid("unbound owner renewal"))?;
        let reply: OwnerGrant = serde_json::from_value(bound.value().clone())
            .map_err(|_| invalid("invalid owner renewal reply"))?;
        if reply.permission_version < s.permission {
            return Err(invalid("owner permission regressed"));
        }
        let previous = s.permission;
        s.permission = reply.permission_version;
        s.expires = bound.expires_at();
        s.pending = None;
        Ok(PermissionAdvance {
            previous,
            current: s.permission,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_protocol::boundary::Durability;
    use serde_json::{json, Value};
    fn at(ms: u64) -> Tick {
        Tick(Duration::from_millis(ms))
    }
    fn host() -> Transport<'static> {
        Transport::AuthenticatedHost {
            issuer: "crewship",
            durabilities: &[Durability::Node],
            required: Durability::Node,
        }
    }
    fn guard() -> OwnerGuard {
        let open:SessionOpen=serde_json::from_value(json!({"type":"session_open","request_id":"open","control_profile":"mooring.session-causal.v1","scope":{"workspace_id":"w","artifact_id":"p","application_id":"wordcraft"},"session_id":"session","runtime_ref":"fresh-runtime","versions":{"protocol_version":"0.4.0","model_version":"0.2.0","engine_version":"0.10.0"}})).unwrap();
        let owner:OwnerGrant=serde_json::from_value(json!({"type":"owner_grant","request_id":"initial","scope":open.scope,"session_id":open.session_id,"runtime_ref":open.runtime_ref,"fence":{"generation":"1","token":"01234567890123456789012345678901"},"lease_duration_ms":"1000","revision":"0","renewal_nonce":"unique-initial-namespace","permission_version":"1"})).unwrap();
        OwnerGuard::new(
            &open,
            &owner,
            "crewship",
            Duration::from_millis(1000),
            at(0),
        )
    }
    fn reply(request: &[u8], permission: &str) -> Vec<u8> {
        let q: Value = serde_json::from_slice(request).unwrap();
        serde_json::to_vec(&json!({"type":"owner_grant","request_id":q["request_id"],"scope":q["scope"],"session_id":q["session_id"],"runtime_ref":"fresh-runtime","fence":q["fence"],"lease_duration_ms":"1000","revision":"999","renewal_nonce":q["renewal_nonce"],"permission_version":permission})).unwrap()
    }
    #[test]
    fn owner_nonce_floor_and_original_send_bound_survive_retries_and_late_replies() {
        let g = guard();
        let first = g.begin(at(100)).unwrap();
        assert!(g.begin(at(101)).is_err());
        assert_eq!(g.retry(at(200)).unwrap(), first);
        let advance = g.accept(&reply(&first, "2"), &host(), at(300)).unwrap();
        assert_eq!(advance.previous, Counter(1));
        assert_eq!(advance.current, Counter(2));
        assert!(!g.matches_permission(Counter(1), at(301)));
        assert!(g.matches_permission(Counter(2), at(301)));
        let next = g.begin(at(400)).unwrap();
        assert_ne!(next, first);
        let request: Value = serde_json::from_slice(&next).unwrap();
        assert_eq!(request["permission_version"], "2");
        assert!(g.accept(&reply(&first, "1"), &host(), at(500)).is_err());
        assert!(g.accept(&reply(&next, "1"), &host(), at(501)).is_err());
        assert_eq!(g.retry(at(502)).unwrap(), next);
        assert_eq!(g.permission(), Counter(2));
        assert!(g.may_own(at(1099)));
        assert!(!g.may_own(at(1100))); // first-send100+1000, never receipt300+1000.
        assert!(g.accept(&reply(&next, "3"), &host(), at(1101)).is_err());
        assert!(!g.matches_permission(Counter(3), at(1102)));
    }
    #[test]
    fn invalid_issuer_and_identity_cannot_extend_or_reopen_owner() {
        let g = guard();
        let pending = g.begin(at(100)).unwrap();
        let other = Transport::AuthenticatedHost {
            issuer: "another-host",
            durabilities: &[Durability::Node],
            required: Durability::Node,
        };
        assert!(g.accept(&reply(&pending, "2"), &other, at(200)).is_err());
        assert!(g
            .accept(&reply(&pending, "2"), &Transport::Participant, at(201))
            .is_err());
        let mut bad: Value = serde_json::from_slice(&reply(&pending, "2")).unwrap();
        bad["fence"]["generation"] = json!("2");
        assert!(g
            .accept(&serde_json::to_vec(&bad).unwrap(), &host(), at(202))
            .is_err());
        assert!(g.may_own(at(999)));
        assert!(!g.may_own(at(1000)));
        assert!(g.begin(at(1001)).is_err());
        assert!(g.retry(at(1002)).is_err());
    }
    #[test]
    fn owner_floor_covers_full_u64_and_revocation_is_permanent() {
        let g = guard();
        let pending = g.begin(at(100)).unwrap();
        g.accept(&reply(&pending, "18446744073709551615"), &host(), at(200))
            .unwrap();
        assert_eq!(g.permission(), Counter(u64::MAX));
        let next = g.begin(at(300)).unwrap();
        assert!(g
            .accept(&reply(&next, "18446744073709551614"), &host(), at(400))
            .is_err());
        g.close();
        assert!(g
            .accept(&reply(&next, "18446744073709551615"), &host(), at(401))
            .is_err());
        assert!(!g.matches_permission(Counter(u64::MAX), at(402)));
    }
    #[test]
    fn clock_regression_and_nonce_exhaustion_close_without_history_growth() {
        let g = guard();
        assert!(g.may_own(at(100)));
        assert!(!g.may_own(at(99)));
        assert!(!g.may_own(at(101)));
        let g = guard();
        g.state.borrow_mut().sequence = u64::MAX;
        assert!(g.begin(at(100)).is_err());
        assert!(g.is_closed());
        assert!(g.state.borrow().pending.is_none());
    }
}
