//! Consuming owned-Doc transition; unselected by production CLI. Participant
//! admission, ingress, dispatch and verified settlement are separate work.
use crate::{
    dispatch::invalid, session_confirmed::OwnerController, session_driver::Driver,
    session_lane::Route, session_startup::ConfirmedDocument,
};
use mooring_core::{actor::Actor, batch::Change};
use mooring_protocol::{boundary::Transport, AtomicChange, Counter, OwnerGrant};
use std::{io, num::NonZeroUsize, sync::Arc, time::Duration};

type OwnedActor = Actor<ConfirmedDocument, Arc<AtomicChange>>;
pub type Recovery = (ConfirmedDocument, u64, Vec<Change<Arc<AtomicChange>>>);

/// Not Clone. Owns the original driver/FD/worker and exactly one restored Doc.
/// There is no mutable confirmed accessor or public raw transport getter.
pub struct RunningDriver {
    driver: Driver,
    owner: OwnerController,
    actor: OwnedActor,
    issuer: String,
}
impl RunningDriver {
    pub(crate) fn from_driver(
        driver: Driver,
        owner: OwnerController,
        document: ConfirmedDocument,
        issuer: String,
    ) -> io::Result<Self> {
        let revision = document.checkpoint().revision.0;
        // Conservatively charge the exact current binding and the longest
        // actor-local sequence/counter encodings before the scheduler starts.
        // Per-change charges must additionally include their separating comma.
        let empty = serde_json::json!({
            "type":"session_submit_batch", "request_id":"actor-submit:18446744073709551615",
            "control_profile":"mooring.session-causal.v1", "scope":owner.scope(),
            "session_id":owner.session_id(), "runtime_ref":owner.runtime_ref(),
            "fence":owner.initial_owner().fence, "versions":owner.versions(),
            "confirmed_basis":{"revision":"18446744073709551615", "digest":document.checkpoint().digest},
            "changes":[]
        });
        let reserve = serde_json::to_vec(&empty)
            .map_err(|_| invalid("actor envelope encoding"))?
            .len();
        let actor = Actor::new(
            document,
            revision,
            NonZeroUsize::new(reserve).ok_or_else(|| invalid("actor envelope limit"))?,
        )
        .map_err(|_| invalid("actor startup limit"))?;
        Ok(Self {
            driver,
            owner,
            actor,
            issuer,
        })
    }
    pub fn confirmed(&self) -> &ConfirmedDocument {
        self.actor.confirmed().0
    }
    pub fn known_owner_permission(&self) -> Counter {
        self.owner.known_permission()
    }
    pub fn check_owner(&mut self) -> io::Result<()> {
        let result = (|| {
            let now = (self.driver.clock)()?;
            if self.driver.lane.is_closed() || !self.owner.may_own(now) {
                return Err(invalid("running owner expired"));
            }
            Ok(())
        })();
        if result.is_err() {
            self.close();
        }
        result
    }
    /// Retains original first-send and nonce metadata across the consuming
    /// transition. Permission advancement still grants no participant rights.
    pub fn maintain_owner(&mut self, host: &Transport<'_>, wait: Duration) -> io::Result<()> {
        let result = self.maintain_inner(host, wait);
        if result.is_err() {
            self.close();
        }
        result
    }
    fn maintain_inner(&mut self, host: &Transport<'_>, wait: Duration) -> io::Result<()> {
        if !matches!(host, Transport::AuthenticatedHost { issuer, .. } if *issuer == self.issuer) {
            return Err(invalid("running owner requires original host"));
        }
        self.check_owner()?;
        let now = (self.driver.clock)()?;
        let raw = if self.driver.maintenance.begin_due(now) {
            let raw = self.owner.begin_renewal(now)?;
            self.driver.maintenance.started(now)?;
            Some(raw)
        } else if self.driver.maintenance.retry_due(now) {
            let raw = self.owner.retry_renewal(now)?;
            self.driver.maintenance.retried(now)?;
            Some(raw)
        } else {
            None
        };
        if let Some(raw) = raw {
            self.driver.lane.output().send_control_checked(&raw, || {
                if self.owner.may_own((self.driver.clock)()?) {
                    Ok(())
                } else {
                    Err(invalid("running owner expired before send"))
                }
            })?;
        }
        self.check_owner()?;
        let wait = self.driver.maintenance.wait((self.driver.clock)()?, wait);
        if let Some(raw) = self.driver.lane.inbox(Route::Actor).receive(wait)? {
            self.check_owner()?;
            if raw == self.driver.maintenance.completed {
                return Ok(());
            }
            let grant: OwnerGrant = serde_json::from_slice(&raw)
                .map_err(|_| invalid("unexpected running actor control"))?;
            self.owner
                .accept_renewal(&raw, host, (self.driver.clock)()?)?;
            self.driver
                .maintenance
                .completed(Duration::from_millis(grant.lease_duration_ms.0), raw)?;
        }
        self.check_owner()
    }
    pub fn close(&mut self) {
        self.owner.close();
        self.actor.close();
        self.driver.lane.close();
    }
    /// The actual original Doc and all scheduler/session-owned immutable intents
    /// survive a nonterminal close. This produces no terminal outcome or Saved.
    pub fn into_recovery(mut self) -> Recovery {
        self.close();
        self.actor.into_recovery()
    }
}
