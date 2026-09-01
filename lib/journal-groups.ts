/**
 * Group ↔ entry-type plumbing for server-side filtering.
 *
 * The Timeline UI lets users mute groups (e.g., "container") via the
 * type-chip row. The mute state used to be applied purely client-side,
 * which silently broke when the loaded buffer hit its 5,000-entry cap:
 * muting "container" might leave only stale `exec.command` entries
 * because the server already returned the most recent 5,000 rows
 * (mostly container metrics). Pushing the mute to the server fixes it.
 *
 * This module owns the inverse map: given a set of muted groups,
 * return the list of entry types to EXCLUDE on the server side.
 */
import { type EntryGroup } from "./journal-style"

// Source-of-truth list of every entry type that maps to a non-"other"
// group. Kept here rather than re-deriving from journal-style.ts at
// runtime — listing the types explicitly means a stray new entry type
// without a TYPE_TO_GROUP mapping doesn't accidentally vanish from
// "Workspace" filtering (since it would default to group="other").
//
// The duplication is deliberate but it is NOT unchecked: the two maps
// drifted (hook.dispatch_error was excluded server-side here while the
// client still called it "other"), so lib/__tests__/journal-groups.test.ts
// now asserts they agree in both directions and that every entry type the
// Go backend can emit appears in both.
export const ENTRY_TYPES_BY_GROUP: Record<EntryGroup, string[]> = {
  exec: ["exec.command", "exec.output_chunk"],
  network: ["network.egress", "network.port_opened", "network.port_closed"],
  file: ["file.written"],
  container: ["container.metrics", "container.snapshot", "agent.status_change"],
  run: [
    "run.started",
    "run.completed",
    "run.failed",
    "run.cancelled",
    "run.timeout",
    "run.session_init",
    "run.agent_span",
  ],
  routine: [
    "pipeline.run.started",
    "pipeline.run.completed",
    "pipeline.run.failed",
    "pipeline.step.started",
    "pipeline.step.completed",
    "pipeline.step.failed",
    "pipeline.step.validation_failed",
    "pipeline.step.skipped",
    "pipeline.step.retrying",
    "pipeline.step.container_ready",
    "pipeline.dry_run",
    "pipeline.schedule.circuit_breaker_tripped",
    "pipeline.schedule.missed_occurrences",
    "pipeline.runs_swept",
    "automation.throttled",
    "automation.depth_exceeded",
  ],
  keeper: [
    "keeper.request",
    "keeper.decision",
    "keeper.rule_auto_tuned",
    "guardrail.input_blocked",
    "guardrail.output_blocked",
    "credential.auto_assign_failed",
    "credential.auto_assign_empty",
    "credential.revealed",
    "credential.reveal_policy_changed",
    "credential.sensitivity_lowered",
    "credential.lease_issued",
  ],
  peer: [
    "peer.conversation",
    "peer.escalation",
    "message.broadcast",
    "agent.mentioned",
    "notification.delivered",
    "notification.failed",
    "notification.dropped",
  ],
  assignment: [
    "assignment.created",
    "assignment.running",
    "assignment.completed",
    "assignment.failed",
    "task.delegated",
  ],
  approval: [
    "approval.requested",
    "approval.granted",
    "approval.denied",
    "approval.timeout",
    "approval.cancelled",
    "approval.trust_granted",
    "approval.trust_revoked",
    "approval.auto_tuning_reset",
  ],
  mission: [
    "mission.status_change",
    "mission.comment",
    "crew.action",
    "mission.created",
    "mission.assigned",
  ],
  cost: ["cost.incurred", "budget.warning", "budget.exceeded", "llm.call", "llm.cache_hit"],
  skill: [
    "skill.assigned",
    "skill.unassigned",
    "skill.imported",
    "skill.deleted",
    "skill.invoked",
  ],
  memory: [
    "memory.updated",
    "memory.consolidated",
    "memory.priority_changed",
    "summary.generated",
    "memory.write_rejected",
    "memory.write_verifier_blocked",
    "memory.consolidation_proposed",
    "memory.searched",
    "memory.versions_swept",
    "memory.config_updated",
    "memory.skill_proposed",
    "memory.skill_approved",
    "memory.skill_rejected",
  ],
  system: [
    "system.compaction",
    "system.migration",
    "system.hook_toggled",
    "system.consolidation_triggered",
    "system.consolidation_completed",
    "checkpoint.created",
    "checkpoint.restored",
    "fork.created",
    "backup.chain_resigned",
    "hook.fired",
    "hook.blocked",
    "hook.dispatch_error",
    "eval.run_started",
    "eval.metric",
    "eval.regression_detected",
    "agent.error",
    "onboarding.proposal_applied",
    "queue.sweeper_pumped",
    "policy.changed",
  ],
  audit: [
    "audit.entity_created",
    "audit.entity_updated",
    "audit.entity_deleted",
    "audit.entity_restored",
  ],
  provisioning: [
    "provisioning.queued",
    "provisioning.building",
    "provisioning.complete",
    "provisioning.failed",
    "provisioning.build_failed",
    "provisioning.step",
    "sidecar.stale",
    "image.stale",
  ],
  chat: ["chat.user_message", "chat.agent_response", "conversation.compacted"],
  page: [
    "page.produce_denied",
    "page.panel.updated",
    "page.panel.stale",
    "page.panel.recovered",
    "page.wake.fired",
    "page.action.dispatched",
    "page.spec.changed",
    "page.grant_added",
    "page.grant_removed",
    "page.owner_transferred",
    "page.published",
    "page.link_revoked",
    "page.public_view",
    "page.webhook_issued",
    "page.webhook_revoked",
  ],
  // "other" is a catch-all — anything not matched above. We can't
  // expand it on the server side because we don't know what the
  // unmapped types might be. Muting "other" therefore stays
  // client-side: returns empty here so we don't accidentally exclude
  // every type.
  other: [],
}

/**
 * Given a set of muted groups, return the entry types to exclude
 * server-side. Empty result → no `exclude_entry_type` query param needed.
 *
 * "other" can't be reliably translated to a type list (its membership
 * is the complement of every TYPE_TO_GROUP entry, which would mean
 * sending an exclusion list of "everything we know about" — fragile and
 * misses any newly-added types). When "other" is muted, the caller
 * still has to apply the mute client-side via LogsPanel. That's the
 * existing path so behaviour doesn't regress.
 */
export function entryTypesForGroups(muted: ReadonlySet<EntryGroup>): string[] {
  if (muted.size === 0) return []
  const out: string[] = []
  for (const g of muted) {
    if (g === "other") continue
    out.push(...ENTRY_TYPES_BY_GROUP[g])
  }
  return out
}
