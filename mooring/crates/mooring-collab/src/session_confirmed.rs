//! One-way owned startup transition and verified host-document settlement.
//! No ingress, participant authority, mutable document accessor, or CLI route.
use crate::{
    dispatch::{invalid, inventory_set},
    session_owner_guard::{OwnerGuard, PermissionAdvance},
    session_startup::ConfirmedDocument,
};
use mooring_core::{
    adapter::PrepareAdapter, batch::Tick, checkpoint_description::DescribeCheckpoint,
};
use mooring_protocol::{
    boundary::{PendingAtomic, Transport},
    session_receipt, Counter, OperationResult, OwnerGrant, Scope, SessionOpen, Versions,
};
use mooring_wordcraft::{adapter::WordCraftAdapter, Document};
use std::{collections::BTreeMap, io};

pub struct PendingSettlement<'a> {
    pub request_id: &'a str,
    pub changes: &'a [PendingAtomic],
}

/// Not Clone: retries and monotonic nonce ownership cannot be reset by copying
/// controller state. It owns lease metadata, never a confirmed or candidate Doc.
pub struct OwnerController {
    open: SessionOpen,
    initial: OwnerGrant,
    issuer: String,
    guard: OwnerGuard,
}
impl OwnerController {
    pub(crate) fn from_parts(
        open: SessionOpen,
        initial: OwnerGrant,
        issuer: String,
        guard: OwnerGuard,
    ) -> Self {
        Self {
            open,
            initial,
            issuer,
            guard,
        }
    }
    pub fn scope(&self) -> &Scope {
        &self.open.scope
    }
    pub fn versions(&self) -> &Versions {
        &self.open.versions
    }
    pub fn session_id(&self) -> &str {
        &self.open.session_id
    }
    pub fn runtime_ref(&self) -> &str {
        &self.open.runtime_ref
    }
    pub fn initial_owner(&self) -> &OwnerGrant {
        &self.initial
    }
    pub fn may_own(&self, now: Tick) -> bool {
        self.guard.may_own(now)
    }
    pub fn close(&self) {
        self.guard.close();
    }
    pub fn known_permission(&self) -> Counter {
        self.guard.permission()
    }
    pub fn matches_owner_permission(&self, version: Counter, now: Tick) -> bool {
        self.guard.matches_permission(version, now)
    }
    pub fn begin_renewal(&self, now: Tick) -> io::Result<Vec<u8>> {
        self.guard.begin(now)
    }
    pub fn retry_renewal(&self, now: Tick) -> io::Result<Vec<u8>> {
        self.guard.retry(now)
    }
    pub fn accept_renewal(
        &self,
        raw: &[u8],
        host: &Transport<'_>,
        now: Tick,
    ) -> io::Result<PermissionAdvance> {
        self.guard.accept(raw, host, now)
    }

    /// The FD actor must first match the FIFO sequence to its retained original
    /// request/membership, then call this method using that exact binding. Only
    /// its successful result may replace the actor's confirmed Doc. None means
    /// verified rejected-only/already-confirmed replay, never unknown outcome.
    /// Any failure permanently closes owner metadata; the driver must also
    /// close transport and return all pending original intents for recovery.
    pub fn verify_committed(
        &self,
        current: &ConfirmedDocument,
        raw: &[u8],
        pending: PendingSettlement<'_>,
        host: &Transport<'_>,
        mut clock: impl FnMut() -> io::Result<Tick>,
    ) -> io::Result<Option<ConfirmedDocument>> {
        let result = (|| {
            if !self.may_own(clock()?) {
                return Err(invalid("owner expired before settlement"));
            }
            let Transport::AuthenticatedHost { issuer, .. } = host else {
                return Err(invalid("settlement needs authenticated host"));
            };
            if *issuer != self.issuer {
                return Err(invalid("settlement issuer changed"));
            }
            if current.checkpoint().scope != self.open.scope
                || current.checkpoint().versions != self.open.versions
                || current.checkpoint().format_version
                    != mooring_wordcraft::profile::CHECKPOINT_FORMAT
            {
                return Err(invalid(
                    "confirmed document belongs to another session model/scope",
                ));
            }
            let checked = session_receipt::validate(
                raw,
                host,
                &session_receipt::Binding {
                    request_id: pending.request_id,
                    scope: &self.open.scope,
                    session_id: &self.open.session_id,
                    runtime_ref: &self.open.runtime_ref,
                    fence: &self.initial.fence,
                    versions: &self.open.versions,
                    changes: pending.changes,
                },
            )
            .map_err(|_| invalid("invalid committed session receipt"))?;
            let result = checked.result();
            let before = current.checkpoint().revision.0;
            let after = result.checkpoint.revision.0;
            if after < before {
                return Err(invalid("checkpoint revision moved backwards"));
            }
            let mut next = before;
            for change in &result.receipt.changes {
                for operation in &change.operations {
                    if let OperationResult::Saved(saved) = operation {
                        if saved.revision.0 > before {
                            next = next
                                .checked_add(1)
                                .ok_or_else(|| invalid("saved revision overflow"))?;
                            if saved.revision.0 != next {
                                return Err(invalid("uncovered committed revision"));
                            }
                        }
                    }
                }
            }
            if next != after {
                return Err(invalid("checkpoint includes unaccounted changes"));
            }
            let replacement = if after == before {
                if &result.checkpoint != current.checkpoint() || result.targets != current.targets()
                {
                    return Err(invalid("unchanged revision changed checkpoint identity"));
                }
                None
            } else {
                // Every advancing revision was just durably saved by this owner.
                // Historical same-revision checkpoints keep their old provenance.
                if result.checkpoint.generation != self.initial.fence.generation
                    || result.checkpoint.format_version
                        != mooring_wordcraft::profile::CHECKPOINT_FORMAT
                {
                    return Err(invalid("new committed checkpoint provenance or format"));
                }
                let prior: BTreeMap<_, _> = current
                    .targets()
                    .iter()
                    .map(|target| (target.target_id.as_str(), target))
                    .collect();
                let incoming: BTreeMap<_, _> = result
                    .targets
                    .iter()
                    .map(|target| (target.target_id.as_str(), target))
                    .collect();
                for (id, old) in &prior {
                    let new = incoming
                        .get(id)
                        .ok_or_else(|| invalid("committed target history omitted"))?;
                    if new.revision < old.revision
                        || new.structure_revision < old.structure_revision
                        || (old.deleted && !new.deleted)
                        || (new.revision != old.revision && new.revision.0 <= before)
                        || (new.structure_revision != old.structure_revision
                            && new.structure_revision.0 <= before)
                        || (old.deleted != new.deleted
                            && new.structure_revision == old.structure_revision)
                    {
                        return Err(invalid("committed target history regressed"));
                    }
                }
                for (id, new) in &incoming {
                    if !prior.contains_key(id)
                        && (new.revision.0 <= before || new.structure_revision.0 <= before)
                    {
                        return Err(invalid("new target lacks current creation revision"));
                    }
                }
                let metadata = WordCraftAdapter
                    .describe_checkpoint(checked.checkpoint_bytes())
                    .map_err(|_| invalid("unqualified committed checkpoint metadata"))?;
                if metadata.format_version != result.checkpoint.format_version
                    || metadata.versions != result.checkpoint.versions
                    || metadata.assets != result.checkpoint.assets
                {
                    return Err(invalid("committed checkpoint metadata mismatch"));
                }
                let inventory = WordCraftAdapter
                    .validate_checkpoint(checked.checkpoint_bytes())
                    .map_err(|_| invalid("invalid committed application checkpoint"))?;
                let actual = inventory_set(&inventory)?;
                let declared: BTreeMap<_, _> = result
                    .targets
                    .iter()
                    .map(|target| (target.target_id.as_str(), target.deleted))
                    .collect();
                if actual != declared {
                    return Err(invalid("incomplete committed target inventory"));
                }
                let document = Document::restore(checked.checkpoint_bytes())
                    .map_err(|_| invalid("committed live document restore"))?;
                Some(ConfirmedDocument::committed(
                    document,
                    result.checkpoint.clone(),
                    result.targets.clone(),
                ))
            };
            if !self.may_own(clock()?) {
                return Err(invalid("owner expired during settlement"));
            }
            Ok(replacement)
        })();
        if result.is_err() {
            self.close();
        }
        result
    }
}
impl Drop for OwnerController {
    fn drop(&mut self) {
        self.close();
    }
}
