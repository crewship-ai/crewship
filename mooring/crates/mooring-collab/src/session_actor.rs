//! Unselected inherited-host actor: correlated ticket admission, immutable
//! typed submissions, the independent Rust timer and verified host settlement.
//! This is not complete participant delivery or production admission authority.
use crate::{
    dispatch::invalid, session_confirmed::OwnerController, session_driver::Driver,
    session_lane::Route, session_startup::ConfirmedDocument,
};
use mooring_core::{actor::Actor, batch::Change};
use mooring_protocol::{
    boundary::{PendingAtomic, Transport},
    control, session_control, AtomicChange, Counter, OwnerGrant, SessionParticipantInput,
    SessionParticipantInputMessage, SessionParticipantOpen,
};
use std::{
    collections::{BTreeMap, VecDeque},
    io,
    num::NonZeroUsize,
    sync::Arc,
    time::Duration,
};

type OwnedActor = Actor<ConfirmedDocument, Arc<AtomicChange>>;
pub type Recovery = (ConfirmedDocument, u64, Vec<Change<Arc<AtomicChange>>>);
type GroupKey = (String, String);
struct PublicGroup {
    outer_request: String,
    submission: Arc<mooring_protocol::SubmitChanges>,
    _budget: crate::session_lane::RetainedControl,
}
struct Queued {
    connection: String,
    change: Change<Arc<AtomicChange>>,
    group: Option<GroupKey>,
}

struct PendingBatch {
    sequence: u64,
    request_id: String,
    changes: Vec<Arc<AtomicChange>>,
    membership: Vec<PendingAtomic>,
    group: Option<GroupKey>,
}

struct PeerUpdate {
    request_id: String,
    from: Counter,
    to: Counter,
    data: String,
}
struct TerminalDelivery {
    request_id: String,
    receipt: Arc<mooring_protocol::CommitResult>,
    participants: std::collections::BTreeSet<String>,
    peer_receipt: Arc<mooring_protocol::CommitResult>,
    updates: Vec<PeerUpdate>,
    peer_positions: BTreeMap<String, usize>,
    _budget: crate::session_lane::RetainedControl,
}

// Stable order of NEW nodes only. Dependencies outside this batch stay exact
// and unresolved for the host; an earlier SENT node is never called Saved here.
// Cycles and their descendants stay last in original order for host rejection.
fn order_changes(changes: Vec<Arc<AtomicChange>>) -> Vec<Arc<AtomicChange>> {
    let operations: BTreeMap<_, _> = changes
        .iter()
        .enumerate()
        .flat_map(|(i, c)| c.operations.iter().map(move |op| (op.op_id.as_str(), i)))
        .collect();
    let dependencies: Vec<std::collections::BTreeSet<usize>> = changes
        .iter()
        .enumerate()
        .map(|(i, c)| {
            c.operations
                .iter()
                .flat_map(|op| &op.dependencies)
                .filter_map(|dependency| operations.get(dependency.as_str()).copied())
                .filter(|other| *other != i)
                .collect()
        })
        .collect();
    let mut remaining: Vec<_> = dependencies.iter().map(|deps| deps.len()).collect();
    let mut downstream = vec![Vec::new(); changes.len()];
    for (index, deps) in dependencies.iter().enumerate() {
        for dependency in deps {
            downstream[*dependency].push(index);
        }
    }
    let mut ready: std::collections::BTreeSet<_> = remaining
        .iter()
        .enumerate()
        .filter(|(_, count)| **count == 0)
        .map(|(index, _)| index)
        .collect();
    let mut done = vec![false; changes.len()];
    let mut order = Vec::with_capacity(changes.len());
    while let Some(index) = ready.pop_first() {
        done[index] = true;
        order.push(changes[index].clone());
        for dependent in &downstream[index] {
            remaining[*dependent] -= 1;
            if remaining[*dependent] == 0 {
                ready.insert(*dependent);
            }
        }
    }
    order.extend(
        changes
            .into_iter()
            .enumerate()
            .filter(|(i, _)| !done[*i])
            .map(|(_, c)| c),
    );
    order
}

/// Not Clone. Owns the original driver/FD/worker and exactly one restored Doc.
/// There is no mutable confirmed accessor or public raw transport getter.
pub struct RunningDriver {
    driver: Driver,
    owner: OwnerController,
    actor: OwnedActor,
    wake: mooring_core::schedule::native::ReadyWake,
    issuer: String,
    participants: crate::session_participants::Participants,
    reads: crate::session_reads::Reads,
    input: VecDeque<Queued>,
    input_bytes: usize,
    gate_bytes: BTreeMap<String, usize>,
    pending: VecDeque<PendingBatch>,
    originals: BTreeMap<String, Arc<AtomicChange>>,
    operation_changes: BTreeMap<String, String>,
    refused: Vec<Change<Arc<AtomicChange>>>,
    deliveries: VecDeque<TerminalDelivery>,
    groups: BTreeMap<GroupKey, PublicGroup>,
    feeding_group: Option<GroupKey>,
    envelope_reserve: usize,
}
impl RunningDriver {
    /// One bounded live step. Only authenticated inherited-host controls can
    /// reach this method; host sealed-op admission remains mandatory at COMMIT.
    /// A nonterminal failure closes every lane and keeps original owned intents.
    pub fn step(&mut self, host: &Transport<'_>, wait: Duration) -> io::Result<()> {
        let result = self.step_inner(host, wait);
        if result.is_err() {
            self.close();
        }
        result
    }
    fn step_inner(&mut self, host: &Transport<'_>, wait: Duration) -> io::Result<()> {
        self.wake.bind_current();
        if !matches!(host, Transport::AuthenticatedHost { issuer, .. } if *issuer == self.issuer) {
            return Err(invalid("actor needs original authenticated host"));
        }
        self.check_owner()?;
        self.renew_due()?;
        self.renew_participants()?;
        self.flush_deliveries()?;
        self.flush_input()?;
        self.dispatch_due()?;
        let now = (self.driver.clock)()?;
        let wait = self.driver.maintenance.wait(now, wait);
        let wait = self.participants.wait(&self.owner, now, wait);
        if let Some(raw) = self
            .driver
            .lane
            .inbox(Route::Actor)
            .receive_parked(wait, &self.wake)?
        {
            self.check_owner()?;
            let value: serde_json::Value =
                serde_json::from_slice(&raw).map_err(|_| invalid("actor control"))?;
            match value["type"].as_str() {
                Some("owner_grant") => self.accept_owner(&raw, host)?,
                Some("delivery_grant") => self.participants.accept_renewal(
                    &raw,
                    host,
                    &self.owner,
                    (self.driver.clock)()?,
                )?,
                Some("session_participant_open") => {
                    let opening: SessionParticipantOpen =
                        serde_json::from_slice(&raw).map_err(|_| invalid("participant opening"))?;
                    let request =
                        self.participants
                            .begin(opening, &self.owner, (self.driver.clock)()?)?;
                    self.send_control(&request)?;
                }
                Some("protocol_error") => {
                    self.participants
                        .decline(&raw, host, &self.owner, (self.driver.clock)()?)?
                }
                Some("ticket_validated") => {
                    let (opening, connection) = self.participants.accept(
                        &raw,
                        host,
                        &self.owner,
                        (self.driver.clock)()?,
                    )?;
                    self.joined(&opening, &connection)?;
                }
                Some("session_participant_input") => {
                    let input: SessionParticipantInput =
                        serde_json::from_slice(&raw).map_err(|_| invalid("participant input"))?;
                    self.accept_input(input)?;
                }
                Some("session_commit_result") => self.committed(&raw, host)?,
                // No unqualified ACK or rights refresh is inferred. Existing
                // host send fences remain decisive while recovery closes gates.
                _ => return Err(invalid("unsupported live actor control")),
            }
        }
        self.renew_participants()?;
        self.flush_deliveries()?;
        self.flush_input()?;
        self.dispatch_due()?;
        self.check_owner()
    }
    fn renew_participants(&mut self) -> io::Result<()> {
        let requests = self
            .participants
            .renewals(&self.owner, (self.driver.clock)()?)?;
        for (connection, raw) in requests {
            self.driver.lane.output().send_control_checked(&raw, || {
                self.participants
                    .may_send(&connection, &self.owner, (self.driver.clock)()?)
            })?;
        }
        Ok(())
    }
    fn renew_due(&mut self) -> io::Result<()> {
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
            self.send_control(&raw)?;
        }
        Ok(())
    }
    fn accept_owner(&mut self, raw: &[u8], host: &Transport<'_>) -> io::Result<()> {
        if raw == self.driver.maintenance.completed {
            return Ok(());
        }
        let grant: OwnerGrant = serde_json::from_slice(raw).map_err(|_| invalid("owner reply"))?;
        self.owner
            .accept_renewal(raw, host, (self.driver.clock)()?)?;
        self.driver.maintenance.completed(
            Duration::from_millis(grant.lease_duration_ms.0),
            raw.to_vec(),
        )
    }
    fn send_control(&self, raw: &[u8]) -> io::Result<()> {
        self.driver.lane.output().send_control_checked(raw, || {
            if self.owner.may_own((self.driver.clock)()?) {
                Ok(())
            } else {
                Err(invalid("owner expired before actor control send"))
            }
        })
    }
    fn output(
        &mut self,
        connection: &str,
        request: &str,
        message: serde_json::Value,
    ) -> io::Result<()> {
        let raw = self.output_frame(connection, request, message)?;
        self.driver
            .lane
            .output()
            .send_participant_checked(&raw, || {
                self.participants
                    .may_send(connection, &self.owner, (self.driver.clock)()?)
            })
    }
    fn output_frame(
        &self,
        connection: &str,
        request: &str,
        message: serde_json::Value,
    ) -> io::Result<Vec<u8>> {
        let participant = self
            .participants
            .live
            .get(connection)
            .ok_or_else(|| invalid("missing output participant"))?;
        let value = serde_json::json!({"type":"session_participant_output","request_id":request,
            "control_profile":session_control::PROFILE,"scope":self.owner.scope(),"session_id":self.owner.session_id(),
            "runtime_ref":self.owner.runtime_ref(),"fence":self.owner.initial_owner().fence,
            "connection_ref":connection,"participant_ref":participant.participant,"gate_id":participant.gate,
            "permission_version":participant.permission,"message":message});
        serde_json::to_vec(&value).map_err(|_| invalid("participant output encoding"))
    }
    fn joined(&mut self, opening: &SessionParticipantOpen, connection: &str) -> io::Result<()> {
        let p = self
            .participants
            .live
            .get(connection)
            .ok_or_else(|| invalid("missing joined participant"))?;
        let request = match &opening.join {
            mooring_protocol::SessionParticipantOpenJoin::SessionJoin(join) => &join.request_id,
            mooring_protocol::SessionParticipantOpenJoin::SessionJoinBounded(join) => {
                &join.request_id
            }
        };
        let joined = serde_json::json!({"type":"session_joined","request_id":request,"scope":self.owner.scope(),
            "session_id":self.owner.session_id(),"participant_ref":p.participant,"generation":self.owner.initial_owner().fence.generation,
            "permission_version":p.permission,"versions":self.owner.versions(),"gate_id":p.gate});
        let message = if p.bounded_transfer {
            serde_json::json!({"type":"session_joined_bounded","request_id":request,"joined":joined,"selected_transfer_profile":"bounded_transfer_v1"})
        } else {
            joined
        };
        self.output(connection, &opening.request_id, message)
    }
    fn accept_input(&mut self, input: SessionParticipantInput) -> io::Result<()> {
        if let SessionParticipantInputMessage::SessionLeave(leave) = &input.message {
            if leave.scope != *self.owner.scope() || leave.session_id != self.owner.session_id() {
                return Err(invalid("leave binding"));
            }
            let result = self
                .participants
                .leave(&input, &self.owner, (self.driver.clock)()?);
            if result.is_ok() {
                self.reads.forget(&input.connection_ref);
            }
            return result;
        }
        let participant = self
            .participants
            .input(&input, &self.owner, (self.driver.clock)()?)?;
        if let SessionParticipantInputMessage::ReadRequest(request) = &input.message {
            if participant.granted.read != Some(true)
                || request.scope != *self.owner.scope()
                || request.session_id != self.owner.session_id()
            {
                return Err(invalid("read participant binding"));
            }
            let overhead = self
                .output_frame(
                    &input.connection_ref,
                    &input.request_id,
                    serde_json::Value::Null,
                )?
                .len()
                .checked_sub(4)
                .ok_or_else(|| invalid("read envelope accounting"))?;
            let budget = session_control::MAX_BATCH_BYTES
                .checked_sub(overhead)
                .ok_or_else(|| invalid("read envelope capacity"))?;
            let response = self.reads.respond(
                &input.connection_ref,
                &input.request_id,
                request,
                self.actor.confirmed().0,
                budget,
            );
            let message = match response {
                Ok(message) => message,
                Err(crate::session_reads::Failure::State) => {
                    return Err(invalid("invalid native read projection"))
                }
                Err(error) => {
                    let (code, message) = match error {
                        crate::session_reads::Failure::Invalid => {
                            ("invalid_request", "Invalid document read request.")
                        }
                        crate::session_reads::Failure::Unavailable => (
                            "precondition_failed",
                            "Read snapshot is unavailable. Start a new read.",
                        ),
                        _ => (
                            "capacity_exhausted",
                            "Document read exceeds the available byte budget.",
                        ),
                    };
                    serde_json::json!({"type":"protocol_error","request_id":request.request_id,"scope":self.owner.scope(),
                        "reason":{"code":code,"message":message,"retry":"new_operation_after_refresh"}})
                }
            };
            return self.output(&input.connection_ref, &input.request_id, message);
        }
        if participant.granted.write != Some(true) {
            return Err(invalid("participant lacks write grant"));
        }
        let SessionParticipantInputMessage::SubmitChanges(submission) = input.message else {
            return Err(invalid("unsupported participant input"));
        };
        if submission.scope != *self.owner.scope()
            || submission.session_id != self.owner.session_id()
            || submission.versions != *self.owner.versions()
        {
            return Err(invalid("submission binding"));
        }
        let group_key = self
            .owner
            .native_humans()
            .then(|| (input.participant_ref.clone(), submission.request_id.clone()));
        if let Some(previous) = group_key.as_ref().and_then(|key| self.groups.get(key)) {
            if previous.outer_request != input.request_id
                || previous.submission.as_ref() != &submission
            {
                return Err(invalid("original public group changed"));
            }
            let ids: Vec<_> = submission
                .changes
                .iter()
                .map(|c| c.change_id.clone())
                .collect();
            return self.output(&input.connection_ref,&input.request_id,serde_json::json!({"type":"changes_received","request_id":submission.request_id,"scope":self.owner.scope(),"session_id":self.owner.session_id(),"change_ids":ids,"state":"pending"}));
        }
        let original_submission = group_key.as_ref().map(|_| Arc::new(submission.clone()));
        let mut additions = Vec::new();
        let mut bytes = 0usize;
        let mut change_ids = Vec::new();
        let mut unique_changes = std::collections::BTreeSet::new();
        let mut operation_ids = std::collections::BTreeSet::new();
        for change in submission.changes {
            if !unique_changes.insert(change.change_id.clone()) {
                return Err(invalid("duplicate submitted change identity"));
            }
            change_ids.push(change.change_id.clone());
            if submission
                .task_id
                .as_ref()
                .is_some_and(|task| change.task_id.as_ref() != Some(task))
                || change.operations.iter().any(|op| {
                    op.participant_ref != input.participant_ref
                        || !operation_ids.insert(op.op_id.clone())
                })
            {
                return Err(invalid("submission immutable authorship/task"));
            }
            let raw = serde_json::to_vec(&change).map_err(|_| invalid("change encoding"))?;
            let fingerprint =
                mooring_protocol::canonical::fingerprint_atomic(self.owner.scope(), &raw)
                    .map_err(|_| invalid("invalid atomic identity"))?;
            if !fingerprint.matches_wire() {
                return Err(invalid("atomic fingerprint mismatch"));
            }
            if let Some(previous) = self.originals.get(&change.change_id) {
                if group_key.is_some() {
                    return Err(invalid("original public group adopts pending identity"));
                }
                if previous.as_ref() != &change {
                    return Err(invalid("pending atomic identity changed"));
                }
                continue; // Still PendingEarlier, never locally Saved/Unknown.
            }
            if change
                .operations
                .iter()
                .any(|op| self.operation_changes.contains_key(&op.op_id))
            {
                return Err(invalid("operation pending under different membership"));
            }
            let canonical = mooring_protocol::canonical::normalize(&raw)
                .map_err(|_| invalid("change canonical encoding"))?;
            let charge = raw
                .len()
                .max(canonical.bytes().len())
                .checked_add(1)
                .ok_or_else(|| invalid("change charge overflow"))?;
            bytes = bytes
                .checked_add(charge)
                .ok_or_else(|| invalid("input charge overflow"))?;
            additions.push(Change {
                operations: NonZeroUsize::new(change.operations.len())
                    .ok_or_else(|| invalid("empty atomic change"))?,
                bytes: NonZeroUsize::new(charge).unwrap(),
                value: Arc::new(change),
            });
        }
        let per_gate = self
            .gate_bytes
            .get(&input.connection_ref)
            .copied()
            .unwrap_or(0);
        if bytes > session_control::MAX_GLOBAL_INPUT_BYTES - self.input_bytes
            || bytes > session_control::MAX_GATE_INPUT_BYTES - per_gate
        {
            self.refused.extend(additions);
            return Err(invalid("bounded participant input capacity"));
        }
        if let (Some(key), Some(original)) = (&group_key, original_submission) {
            let operations = additions.iter().map(|c| c.operations.get()).sum::<usize>();
            if operations > 256 || bytes > 262144 - self.envelope_reserve {
                self.refused.extend(additions);
                return Err(invalid("whole public group cannot fit batch"));
            }
            let charge = serde_json::to_vec(&(&input.request_id, original.as_ref(), &change_ids))
                .map_err(|_| invalid("public group charge"))?
                .len();
            let budget = match self.driver.lane.output().reserve_control(charge) {
                Ok(b) => b,
                Err(e) => {
                    self.refused.extend(additions);
                    return Err(e);
                }
            };
            self.groups.insert(
                key.clone(),
                PublicGroup {
                    outer_request: input.request_id.clone(),
                    submission: original,
                    _budget: budget,
                },
            );
        }
        self.input_bytes += bytes;
        *self
            .gate_bytes
            .entry(input.connection_ref.clone())
            .or_default() += bytes;
        for change in additions {
            for op in &change.value.operations {
                self.operation_changes
                    .insert(op.op_id.clone(), change.value.change_id.clone());
            }
            self.originals
                .insert(change.value.change_id.clone(), change.value.clone());
            self.input.push_back(Queued {
                connection: input.connection_ref.clone(),
                change,
                group: group_key.clone(),
            });
        }
        self.output(&input.connection_ref, &input.request_id, serde_json::json!({"type":"changes_received",
            "request_id":submission.request_id,"scope":self.owner.scope(),"session_id":self.owner.session_id(),
            "change_ids":change_ids,"state":"pending"}))
    }
    fn flush_input(&mut self) -> io::Result<()> {
        if self.owner.native_humans() {
            if self.feeding_group.is_some() || self.input.is_empty() {
                return Ok(());
            }
            let group = self
                .input
                .front()
                .and_then(|q| q.group.clone())
                .ok_or_else(|| invalid("missing queued public group"))?;
            let count = self
                .input
                .iter()
                .take_while(|q| q.group.as_ref() == Some(&group))
                .count();
            let queued: Vec<_> = self.input.drain(..count).collect();
            let mut metadata = Vec::with_capacity(count);
            let mut changes = Vec::with_capacity(count);
            for q in queued {
                metadata.push((q.connection, q.change.bytes.get()));
                changes.push(q.change);
            }
            match self.actor.enqueue_group(changes) {
                Ok(()) => {
                    for (connection, bytes) in metadata {
                        self.input_bytes -= bytes;
                        *self.gate_bytes.get_mut(&connection).unwrap() -= bytes;
                    }
                    self.feeding_group = Some(group);
                }
                Err(refused) => {
                    let capacity = matches!(
                        refused.reason,
                        mooring_core::schedule::EnqueueReason::Backpressure
                    );
                    for ((connection, _), change) in metadata.into_iter().zip(refused.changes).rev()
                    {
                        self.input.push_front(Queued {
                            connection,
                            change,
                            group: Some(group.clone()),
                        });
                    }
                    if !capacity {
                        return Err(invalid("actor scheduler refused original group"));
                    }
                }
            }
            return Ok(());
        }
        while let Some(queued) = self.input.pop_front() {
            let bytes = queued.change.bytes.get();
            match self.actor.enqueue(queued.change) {
                Ok(()) => {
                    self.input_bytes -= bytes;
                    *self.gate_bytes.get_mut(&queued.connection).unwrap() -= bytes;
                }
                Err(refused) => {
                    let capacity = matches!(
                        refused.reason,
                        mooring_core::schedule::EnqueueReason::Backpressure
                    );
                    self.input.push_front(Queued {
                        connection: queued.connection,
                        change: refused.change,
                        group: queued.group,
                    });
                    if capacity {
                        break;
                    }
                    return Err(invalid("actor scheduler refused original input"));
                }
            }
        }
        Ok(())
    }
    fn dispatch_due(&mut self) -> io::Result<()> {
        if self.owner.native_humans()
            && (self.feeding_group.is_none()
                || self.input.iter().any(|q| q.group == self.feeding_group))
        {
            return Ok(());
        }
        let basis = self.confirmed().checkpoint();
        let basis = mooring_protocol::ConfirmedCheckpointBasis {
            revision: basis.revision,
            digest: basis.digest.clone(),
        };
        let Some(dispatch) = self
            .actor
            .dispatch(Duration::ZERO)
            .map_err(|_| invalid("actor dispatch failed"))?
        else {
            return Ok(());
        };
        let sequence = dispatch.sequence;
        let charge = dispatch.batch.bytes.get();
        let changes: Vec<_> = dispatch
            .batch
            .body
            .iter()
            .map(|change| change.value.clone())
            .collect();
        let group = self.feeding_group.clone();
        let changes = if self.owner.native_humans() {
            let expected = self
                .groups
                .get(
                    group
                        .as_ref()
                        .ok_or_else(|| invalid("missing public group"))?,
                )
                .ok_or_else(|| invalid("lost public group"))?;
            if changes.len() != expected.submission.changes.len()
                || changes
                    .iter()
                    .zip(&expected.submission.changes)
                    .any(|(a, b)| a.as_ref() != b)
            {
                return Err(invalid("split or reordered public group"));
            }
            changes
        } else {
            order_changes(changes)
        };
        let request_id = format!("actor-submit:{sequence}");
        let membership = changes
            .iter()
            .map(|c| PendingAtomic {
                change_id: c.change_id.clone(),
                op_ids: c.operations.iter().map(|op| op.op_id.clone()).collect(),
            })
            .collect();
        let message = serde_json::json!({"type":"session_submit_batch","request_id":request_id,"control_profile":session_control::PROFILE,
            "scope":self.owner.scope(),"session_id":self.owner.session_id(),"runtime_ref":self.owner.runtime_ref(),
            "fence":self.owner.initial_owner().fence,"versions":self.owner.versions(),"confirmed_basis":basis,"changes":changes});
        let raw = serde_json::to_vec(&message).map_err(|_| invalid("batch encoding"))?;
        let checked =
            session_control::validate(&raw, mooring_protocol::control::Peer::InheritedRuntime)
                .map_err(|_| invalid("batch profile limit"))?;
        if raw.len() > charge || checked.bytes().len() > charge {
            return Err(invalid("batch exceeded charged envelope"));
        }
        self.pending.push_back(PendingBatch {
            sequence,
            request_id,
            changes,
            membership,
            group,
        });
        self.feeding_group = None;
        self.send_control(checked.bytes())
    }
    fn committed(&mut self, raw: &[u8], host: &Transport<'_>) -> io::Result<()> {
        let pending = self
            .pending
            .front()
            .ok_or_else(|| invalid("unsolicited host receipt"))?;
        let document = self.owner.verify_committed(
            self.confirmed(),
            raw,
            crate::session_confirmed::PendingSettlement {
                request_id: &pending.request_id,
                changes: &pending.membership,
            },
            host,
            || (self.driver.clock)(),
        )?;
        // Reserve the shared control budget BEFORE installing a host snapshot
        // or removing originals. A racing reader can consume the free slot;
        // reservation failure is nonterminal recovery, never a local rejection.
        let result: mooring_protocol::SessionCommitResult =
            serde_json::from_slice(raw).map_err(|_| invalid("verified receipt shape"))?;
        let (delivery_request, delivery_receipt) = if self.owner.native_humans() {
            let key = pending
                .group
                .as_ref()
                .ok_or_else(|| invalid("missing original public group"))?;
            let group = self
                .groups
                .get(key)
                .ok_or_else(|| invalid("lost original public group"))?;
            let public = result
                .public_receipt
                .as_deref()
                .ok_or_else(|| invalid("missing host public receipt"))?;
            let mut expected = result.receipt.clone();
            expected.request_id = group.submission.request_id.clone();
            if public != &expected {
                return Err(invalid("host public receipt correlation"));
            }
            (group.outer_request.clone(), public.clone())
        } else {
            (pending.request_id.clone(), result.receipt.clone())
        };
        let participants: std::collections::BTreeSet<_> = pending
            .changes
            .iter()
            .flat_map(|change| {
                change
                    .operations
                    .iter()
                    .map(|op| op.participant_ref.clone())
            })
            .collect();
        if participants.is_empty() || participants.len() > session_control::MAX_PARTICIPANTS {
            return Err(invalid("receipt recipient capacity"));
        }
        let updates = if self.owner.native_humans() {
            self.verified_updates(pending, &result)?
        } else {
            Vec::new()
        };
        let peer_positions: BTreeMap<String, usize> = if updates.is_empty() {
            BTreeMap::new()
        } else {
            self.participants
                .live
                .values()
                .map(|p| (p.participant.clone(), 0))
                .collect()
        };
        let update_charge: Vec<_> = updates
            .iter()
            .map(|u| (&u.request_id, u.from, u.to, &u.data))
            .collect();
        let charge = serde_json::to_vec(&(
            &delivery_request,
            &delivery_receipt,
            &participants,
            &result.receipt,
            &update_charge,
            &peer_positions,
        ))
        .map_err(|_| invalid("receipt accounting"))?
        .len();
        let budget = self.driver.lane.output().reserve_control(charge)?;
        let delivery = TerminalDelivery {
            request_id: delivery_request,
            receipt: Arc::new(delivery_receipt),
            participants,
            peer_receipt: Arc::new(result.receipt),
            updates,
            peer_positions,
            _budget: budget,
        };
        let replacement = document.map(|doc| {
            let revision = doc.checkpoint().revision.0;
            (doc, revision)
        });
        self.actor
            .settle_host_response(pending.sequence, replacement)
            .map_err(|_| invalid("out-of-order settlement"))?;
        let pending = self.pending.pop_front().unwrap();
        if let Some(group) = pending.group {
            self.groups.remove(&group);
        }
        for change in &pending.changes {
            self.originals.remove(&change.change_id);
            for op in &change.operations {
                self.operation_changes.remove(&op.op_id);
            }
        }
        self.deliveries.push_back(delivery);
        self.flush_deliveries()
    }
    fn flush_deliveries(&mut self) -> io::Result<()> {
        let mut blocked = std::collections::BTreeSet::new();
        let mut index = 0;
        while index < self.deliveries.len() {
            let recipients: std::collections::BTreeSet<_> = self.deliveries[index]
                .participants
                .iter()
                .chain(self.deliveries[index].peer_positions.keys())
                .cloned()
                .collect();
            for participant in recipients {
                if blocked.contains(&participant) {
                    continue;
                }
                let now = (self.driver.clock)()?;
                let connections: Vec<_> = self
                    .participants
                    .live
                    .iter()
                    .filter(|(_, p)| p.participant == participant)
                    .map(|(c, _)| c.clone())
                    .collect();
                let connection = connections
                    .into_iter()
                    .find(|c| self.participants.may_send(c, &self.owner, now).is_ok());
                let Some(connection) = connection else {
                    blocked.insert(participant);
                    continue;
                };
                if let Some(mut position) = self.deliveries[index]
                    .peer_positions
                    .get(&participant)
                    .copied()
                {
                    while position < self.deliveries[index].updates.len() {
                        self.participants.may_send(
                            &connection,
                            &self.owner,
                            (self.driver.clock)()?,
                        )?;
                        let delivery = &self.deliveries[index];
                        let update = &delivery.updates[position];
                        let request = update.request_id.clone();
                        let message = serde_json::json!({"type":"document_update","request_id":request,"scope":self.owner.scope(),"session_id":self.owner.session_id(),"generation":self.owner.initial_owner().fence.generation,"from_revision":update.from,"to_revision":update.to,"encoding":"json","data":update.data,"receipt":delivery.peer_receipt});
                        // The transport request namespace is global, whereas
                        // this SAME committed cut is delivered to many gates.
                        // Keep its inner cut ID unchanged and scope ONLY the
                        // outer notification ID to the actual recipient.
                        let p = self
                            .participants
                            .live
                            .get(&connection)
                            .ok_or_else(|| invalid("lost peer recipient"))?;
                        let recipient = serde_json::to_vec(&[
                            connection.as_str(),
                            p.participant.as_str(),
                            p.gate.as_str(),
                        ])
                        .map_err(|_| invalid("peer recipient encoding"))?;
                        let digest = mooring_protocol::canonical::normalize(&recipient)
                            .map_err(|_| invalid("peer recipient encoding"))?
                            .digest();
                        let notification = format!("{request}:{digest}");
                        self.output(&connection, &notification, message)?;
                        position += 1;
                        self.deliveries[index]
                            .peer_positions
                            .insert(participant.clone(), position);
                    }
                    self.deliveries[index].peer_positions.remove(&participant);
                }
                if self.deliveries[index].participants.contains(&participant) {
                    self.participants
                        .may_send(&connection, &self.owner, (self.driver.clock)()?)?;
                    let request = self.deliveries[index].request_id.clone();
                    let receipt = self.deliveries[index].receipt.clone();
                    self.output(
                        &connection,
                        &request,
                        serde_json::to_value(receipt.as_ref())
                            .map_err(|_| invalid("receipt forwarding"))?,
                    )?;
                    self.deliveries[index].participants.remove(&participant);
                }
            }
            if self.deliveries[index].participants.is_empty()
                && self.deliveries[index].peer_positions.is_empty()
            {
                self.deliveries.remove(index);
            } else {
                index += 1;
            }
        }
        Ok(())
    }

    fn verified_updates(
        &self,
        pending: &PendingBatch,
        result: &mooring_protocol::SessionCommitResult,
    ) -> io::Result<Vec<PeerUpdate>> {
        let deltas = result
            .native_deltas
            .as_ref()
            .ok_or_else(|| invalid("missing committed native deltas"))?;
        let receipt =
            serde_json::to_value(&result.receipt).map_err(|_| invalid("receipt cut encoding"))?;
        let previous = self.confirmed().checkpoint().revision.0;
        let mut cuts = Vec::new();
        for change in receipt["changes"]
            .as_array()
            .ok_or_else(|| invalid("receipt changes"))?
        {
            if change["status"] != "saved" {
                continue;
            }
            let revision = change["last_revision"]
                .as_str()
                .and_then(|s| s.parse::<u64>().ok())
                .ok_or_else(|| invalid("receipt cut revision"))?;
            if revision <= previous {
                continue;
            }
            let id = change["change_id"]
                .as_str()
                .ok_or_else(|| invalid("receipt cut identity"))?;
            let original = pending
                .changes
                .iter()
                .find(|c| c.change_id == id)
                .ok_or_else(|| invalid("unmatched receipt atomic"))?;
            cuts.push((revision, original));
        }
        cuts.sort_by_key(|(revision, _)| *revision);
        if cuts.len() != deltas.len() {
            return Err(invalid("incomplete committed delta membership"));
        }
        let mut updates = Vec::new();
        let mut from = previous;
        for ((revision, original), entry) in cuts.into_iter().zip(deltas) {
            let expected = from
                .checked_add(original.operations.len() as u64)
                .ok_or_else(|| invalid("cut revision overflow"))?;
            if revision != expected
                || entry.revision.0 != revision
                || entry.change_id != original.change_id
                || entry.membership_hash != original.membership_hash
            {
                return Err(invalid("committed delta cut binding"));
            }
            let raw = mooring_protocol::boundary::decode_base64(&entry.native_delta.update, 262144)
                .map_err(|_| invalid("native delta encoding"))?;
            let actual = mooring_wordcraft::document_update::allocation_origins(&raw)
                .map_err(|_| invalid("native allocation inventory"))?;
            let declared: Vec<_> = entry
                .native_delta
                .allocation_origins
                .iter()
                .map(|n| n.0)
                .collect();
            if entry.native_delta.writer_origin.0 == 0
                || entry.native_delta.writer_origin.0 >= (1 << 53)
                || actual != declared
                || actual
                    .iter()
                    .any(|n| *n != entry.native_delta.writer_origin.0)
            {
                return Err(invalid("native delta allocation binding"));
            }
            let data =
                mooring_wordcraft::document_update::encode(&raw, &entry.native_delta.targets)
                    .map_err(|_| invalid("native public update capacity"))?;
            updates.push(PeerUpdate {
                request_id: format!("actor-update:{revision}"),
                from: Counter(from),
                to: Counter(revision),
                data,
            });
            from = revision;
        }
        if from != result.checkpoint.revision.0 {
            return Err(invalid("native deltas omit committed state"));
        }
        if let Some(last) = deltas.last() {
            let state = mooring_protocol::boundary::decode_base64(
                &result.checkpoint.state,
                control::MAX_CHECKPOINT_BYTES,
            )
            .map_err(|_| invalid("committed state encoding"))?;
            if control::encode_checkpoint(&state)
                .map_err(|_| invalid("committed raw state digest"))?
                .digest
                != last.checkpoint_state_digest
            {
                return Err(invalid("final native cut state binding"));
            }
        }
        Ok(updates)
    }
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
        let wake = mooring_core::schedule::native::ReadyWake::current();
        driver.lane.attach_actor_wake(wake.clone())?;
        let actor = Actor::new_waking(
            document,
            revision,
            NonZeroUsize::new(reserve).ok_or_else(|| invalid("actor envelope limit"))?,
            // Leave validation/encoding headroom within the unchanged100ms maximum.
            // The bounded release matrix includes255ops (not immediate-full).
            Duration::from_millis(40),
            wake.clone(),
        )
        .map_err(|_| invalid("actor startup limit"))?;
        Ok(Self {
            driver,
            owner,
            actor,
            wake,
            issuer,
            participants: crate::session_participants::Participants::new(),
            reads: crate::session_reads::Reads::default(),
            input: VecDeque::new(),
            input_bytes: 0,
            gate_bytes: BTreeMap::new(),
            pending: VecDeque::new(),
            originals: BTreeMap::new(),
            operation_changes: BTreeMap::new(),
            refused: Vec::new(),
            deliveries: VecDeque::new(),
            groups: BTreeMap::new(),
            feeding_group: None,
            envelope_reserve: reserve,
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
        self.reads.clear();
        self.participants.close();
        self.owner.close();
        self.actor.close();
        self.driver.lane.close();
    }
    /// The actual original Doc and all scheduler/session-owned immutable intents
    /// survive a nonterminal close. This produces no terminal outcome or Saved.
    pub fn into_recovery(mut self) -> Recovery {
        self.close();
        let (document, revision, mut originals) = self.actor.into_recovery();
        originals.extend(self.input.into_iter().map(|queued| queued.change));
        originals.extend(self.refused);
        (document, revision, originals)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn change(id: &str, dependencies: &[&str]) -> Arc<AtomicChange> {
        Arc::new(serde_json::from_value(serde_json::json!({"change_id":id,"membership_hash":"test-only",
            "operations":[{"op_id":id,"fingerprint":"test-only","participant_ref":"participant","base_revision":"0",
                "dependencies":dependencies,"read_preconditions":[],"payload":{}}]})).unwrap())
    }
    #[test]
    fn orders_only_new_nodes_preserving_external_pending_and_atomic_membership() {
        let a = change("A", &["C", "earlier-sent-not-confirmed"]);
        let b = change("B", &[]);
        let c = change("C", &[]);
        let ordered = order_changes(vec![a.clone(), b, c]);
        assert_eq!(
            ordered
                .iter()
                .map(|c| c.change_id.as_str())
                .collect::<Vec<_>>(),
            ["B", "C", "A"]
        );
        assert_eq!(ordered[2].as_ref(), a.as_ref());
        let mut atomic = (*change("atomic", &["forward-member"])).clone();
        let mut member = change("forward-member", &[]).operations[0].clone();
        member.op_id = "forward-member".into();
        atomic.operations.push(member);
        let ordered = order_changes(vec![Arc::new(atomic.clone())]);
        assert_eq!(ordered[0].as_ref(), &atomic); // Host rejects intrinsic forward refs; runtime never reauthors.
    }
    #[test]
    fn cycles_do_not_obstruct_ordering_independent_eligible_changes() {
        let ordered = order_changes(vec![
            change("X", &["Y"]),
            change("Y", &["X"]),
            change("D", &["E"]),
            change("E", &[]),
        ]);
        assert_eq!(
            ordered
                .iter()
                .map(|c| c.change_id.as_str())
                .collect::<Vec<_>>(),
            ["E", "D", "X", "Y"]
        );
        assert_eq!(ordered[2].operations[0].dependencies, ["Y"]);
        assert_eq!(ordered[3].operations[0].dependencies, ["X"]);
    }
}
