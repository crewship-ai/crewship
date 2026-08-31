/**
 * Shared visual mapping for journal entries — used by the Crow's Nest
 * Logs view (toolbar chips, severity bar, type pill, stats rail) and any
 * future surface that wants the same Grafana-style colour language.
 *
 * Severity → solid colour (fixed Tailwind palette).
 * Entry type → "group" → group colour.
 *   - The group is the bucket the user filters by in the chips row.
 *   - The colour is applied to the type pill and to the dot in the chip.
 *
 * Anything not in TYPE_TO_GROUP falls back to "other".
 */

import type { JournalSeverity } from "@/lib/types/journal"

export type EntryGroup =
  | "exec"
  | "network"
  | "file"
  | "container"
  | "run"
  | "keeper"
  | "peer"
  | "assignment"
  | "approval"
  | "mission"
  | "cost"
  | "skill"
  | "memory"
  | "system"
  | "audit"
  | "provisioning"
  | "chat"
  | "routine"
  | "page"
  | "other"

/**
 * Higher-level bundles for the Timeline type-chip row. Bundles let
 * users filter by domain ("show me everything Security-flavoured")
 * without having to click 4 different chips. Each base group belongs
 * to exactly one bundle; bundle membership is stable enough that a
 * Record<EntryGroup, EntryBundle> can encode it without lookup tables.
 */
export type EntryBundle =
  | "runtime"     // exec / network / file / container
  | "lifecycle"   // run / routine / mission / assignment / approval / provisioning
  | "security"    // keeper / audit
  | "ai"          // chat / cost / skill / memory
  | "workspace"   // peer / page / system / other

export const SEVERITY_COLOR: Record<JournalSeverity, string> = {
  info: "#38bdf8",   // sky-400
  notice: "#a78bfa", // violet-400
  warn: "#fbbf24",   // amber-400
  error: "#f87171",  // red-400
}

export const SEVERITY_BG_CLASS: Record<JournalSeverity, string> = {
  info: "bg-sky-400",
  notice: "bg-violet-400",
  warn: "bg-amber-400",
  error: "bg-red-400",
}

export const GROUP_COLOR: Record<EntryGroup, string> = {
  exec: "#34d399",        // emerald
  network: "#22d3ee",     // cyan
  file: "#94a3b8",        // slate
  container: "#818cf8",   // indigo
  run: "#fb923c",         // orange
  keeper: "#c084fc",      // purple
  peer: "#f472b6",        // pink
  assignment: "#60a5fa",  // blue
  approval: "#fbbf24",    // amber
  mission: "#fb7185",     // rose
  cost: "#fde047",        // yellow
  skill: "#5eead4",       // teal
  memory: "#a3e635",      // lime
  system: "#9ca3af",      // gray
  audit: "#e879f9",       // fuchsia — distinct from keeper purple
  provisioning: "#7dd3fc", // sky-300 — neighbours indigo/cyan family for "container building"
  chat: "#fdba74",        // orange-300 — warm, distinct from cost yellow
  // green-400. The palette's one wide free hue gap is between memory lime
  // (83°) and exec emerald (160°), and 142° splits it. Emerald is the closest
  // neighbour at ΔE76 ≈ 19 — comfortably the widest separation still
  // available, and more than 3× the palette's existing file↔system pair, but
  // green and emerald are not unmistakable at the 6px dot, so the chip's text
  // label carries the identification. Not any orange: `run` is #fb923c and a
  // routine run must not look like an agent run at a glance.
  routine: "#4ade80",
  // violet-300 — paler than container indigo-400 (#818cf8) and keeper
  // purple-400 (#c084fc), and deliberately NOT #a78bfa, which is already
  // SEVERITY_COLOR.notice: a group dot must never borrow a severity colour.
  page: "#c4b5fd",
  other: "#9ca3af",
}

export const GROUP_LABEL: Record<EntryGroup, string> = {
  exec: "exec",
  network: "network",
  file: "file",
  container: "container",
  run: "run",
  keeper: "keeper",
  peer: "peer",
  assignment: "assignment",
  approval: "approval",
  mission: "mission",
  cost: "cost",
  skill: "skill",
  memory: "memory",
  system: "system",
  audit: "audit",
  provisioning: "provisioning",
  chat: "chat",
  routine: "routine",
  page: "page",
  other: "other",
}

/**
 * Render order in the type-chip filter row, and — via journal-perf.ts —
 * the seed for the per-group counters. `readonly` because those are two
 * jobs for one array: a consumer calling `.sort()` on it would silently
 * reorder the chips AND the counter seed together.
 */
export const GROUP_ORDER: readonly EntryGroup[] = [
  "exec",
  "network",
  "file",
  "container",
  "provisioning",
  "run",
  "routine",
  "mission",
  "assignment",
  "approval",
  "chat",
  "peer",
  "keeper",
  "audit",
  "cost",
  "skill",
  "memory",
  "page",
  "system",
  "other",
]

/**
 * Bundle membership — used by the Timeline toolbar's "5-bundle" chip
 * mode to collapse the base groups into 5 user-meaningful domains.
 * Toggling a bundle toggles every base group inside it.
 */
export const GROUP_TO_BUNDLE: Record<EntryGroup, EntryBundle> = {
  exec: "runtime",
  network: "runtime",
  file: "runtime",
  container: "runtime",
  run: "lifecycle",
  routine: "lifecycle",
  mission: "lifecycle",
  assignment: "lifecycle",
  approval: "lifecycle",
  provisioning: "lifecycle",
  keeper: "security",
  audit: "security",
  chat: "ai",
  cost: "ai",
  skill: "ai",
  memory: "ai",
  peer: "workspace",
  page: "workspace",
  system: "workspace",
  other: "workspace",
}

export const BUNDLE_LABEL: Record<EntryBundle, string> = {
  runtime: "Runtime",
  lifecycle: "Lifecycle",
  security: "Security",
  ai: "AI",
  workspace: "Workspace",
}

/** Render order for the bundle row when the toolbar is in bundle mode. */
export const BUNDLE_ORDER: EntryBundle[] = ["runtime", "lifecycle", "security", "ai", "workspace"]

export const TYPE_TO_GROUP: Record<string, EntryGroup> = {
  "exec.command": "exec",
  "exec.output_chunk": "exec",
  "network.egress": "network",
  "network.port_opened": "network",
  "network.port_closed": "network",
  "file.written": "file",
  "container.metrics": "container",
  "container.snapshot": "container",
  "agent.status_change": "container",
  "run.started": "run",
  "run.completed": "run",
  "run.failed": "run",
  "run.cancelled": "run",
  "run.timeout": "run",
  "keeper.request": "keeper",
  "keeper.decision": "keeper",
  "guardrail.input_blocked": "keeper",
  "guardrail.output_blocked": "keeper",
  "peer.conversation": "peer",
  "peer.escalation": "peer",
  "message.broadcast": "peer",
  "agent.mentioned": "peer",
  "assignment.created": "assignment",
  "assignment.running": "assignment",
  "assignment.completed": "assignment",
  "assignment.failed": "assignment",
  "task.delegated": "assignment",
  "approval.requested": "approval",
  "approval.granted": "approval",
  "approval.denied": "approval",
  "approval.timeout": "approval",
  "approval.cancelled": "approval",
  "mission.status_change": "mission",
  "mission.comment": "mission",
  "crew.action": "mission",
  "cost.incurred": "cost",
  "budget.warning": "cost",
  "budget.exceeded": "cost",
  "llm.call": "cost",
  "llm.cache_hit": "cost",
  "skill.assigned": "skill",
  "skill.unassigned": "skill",
  "skill.imported": "skill",
  "skill.deleted": "skill",
  "memory.updated": "memory",
  "memory.consolidated": "memory",
  "memory.priority_changed": "memory",
  "summary.generated": "memory",
  "system.compaction": "system",
  "system.migration": "system",
  "system.hook_toggled": "system",
  "system.consolidation_triggered": "system",
  "system.consolidation_completed": "system",
  "checkpoint.created": "system",
  "checkpoint.restored": "system",
  "fork.created": "system",
  "hook.fired": "system",
  "hook.blocked": "system",
  "eval.run_started": "system",
  "eval.metric": "system",
  "eval.regression_detected": "system",
  "credential.auto_assign_failed": "keeper",
  "credential.auto_assign_empty": "keeper",
  // Audit — workspace CRUD lifecycle (dual-emit from WriteAuditLog).
  "audit.entity_created": "audit",
  "audit.entity_updated": "audit",
  "audit.entity_deleted": "audit",
  "audit.entity_restored": "audit",
  // Provisioning — container build lifecycle.
  "provisioning.queued": "provisioning",
  "provisioning.building": "provisioning",
  "provisioning.complete": "provisioning",
  "provisioning.failed": "provisioning",
  // Chat — user↔agent conversation turns.
  "chat.user_message": "chat",
  "chat.agent_response": "chat",
  // Agent runtime errors (panic, provider stream errors, etc.).
  "agent.error": "system",

  // ── Routines. pipeline.* IS the routine engine, and automation.* is the
  // substrate that fires it: internal/journal/types.go says of both
  // automation entries that "someone asking 'why did my routine not run'
  // needs to find these", so they share the chip rather than sitting in
  // System where they would be filed and forgotten. Same call
  // lib/activity-stream.ts's "Routines" facet already makes.
  "pipeline.run.started": "routine",
  "pipeline.run.completed": "routine",
  "pipeline.run.failed": "routine",
  "pipeline.step.started": "routine",
  "pipeline.step.completed": "routine",
  "pipeline.step.failed": "routine",
  "pipeline.step.validation_failed": "routine",
  "pipeline.step.skipped": "routine",
  "pipeline.step.retrying": "routine",
  "pipeline.step.container_ready": "routine",
  "pipeline.dry_run": "routine",
  "pipeline.schedule.circuit_breaker_tripped": "routine",
  "pipeline.schedule.missed_occurrences": "routine",
  "pipeline.runs_swept": "routine",
  "automation.throttled": "routine",
  "automation.depth_exceeded": "routine",

  // ── Pages. The whole surface in one chip: panels, freshness verdicts,
  // wake gates, action dispatches, spec edits, grants, ownership, and the
  // three ways a page leaves the product (public link, public view,
  // webhook). Splitting grants into `audit` and publishing into `keeper`
  // would answer "what happened to this page" with a third of the evidence.
  "page.produce_denied": "page",
  "page.panel.updated": "page",
  "page.panel.stale": "page",
  "page.panel.recovered": "page",
  "page.wake.fired": "page",
  "page.action.dispatched": "page",
  "page.spec.changed": "page",
  "page.grant_added": "page",
  "page.grant_removed": "page",
  "page.owner_transferred": "page",
  "page.published": "page",
  "page.link_revoked": "page",
  "page.public_view": "page",
  "page.webhook_issued": "page",
  "page.webhook_revoked": "page",

  // ── Memory, beyond the three that were already mapped. The skill_*
  // trio is the memory→Skills bridge, so it groups with memory (where the
  // proposal came from) rather than with skill (the imported artefact).
  "memory.write_rejected": "memory",
  "memory.write_verifier_blocked": "memory",
  "memory.consolidation_proposed": "memory",
  "memory.searched": "memory",
  "memory.versions_swept": "memory",
  "memory.config_updated": "memory",
  "memory.skill_proposed": "memory",
  "memory.skill_approved": "memory",
  "memory.skill_rejected": "memory",

  // ── Credentials and keeper policy. credential.auto_assign_* already
  // lived in keeper; the reveal/lease/classification events are the same
  // question ("who got reach they did not have") one level up.
  "credential.revealed": "keeper",
  "credential.reveal_policy_changed": "keeper",
  "credential.sensitivity_lowered": "keeper",
  "credential.lease_issued": "keeper",
  "keeper.rule_auto_tuned": "keeper",

  // ── Approvals. A trust grant is a STANDING approval decision and an
  // auto-tuning reset wipes the window those decisions are scored against
  // — both belong with the one-off decisions, not in a namespace of their
  // own that hides why later runs stopped asking.
  "approval.trust_granted": "approval",
  "approval.trust_revoked": "approval",
  "approval.auto_tuning_reset": "approval",

  // ── Outbound notification sends. Grouped with peer messaging for the
  // same reason activity-stream.ts's "Messages" facet holds all of them:
  // a delivery is a message that left the instance.
  "notification.delivered": "peer",
  "notification.failed": "peer",
  "notification.dropped": "peer",

  // ── Chat. A compaction is the conversation losing its own history, so
  // it belongs beside the turns it dropped.
  "conversation.compacted": "chat",

  // ── Missions.
  "mission.created": "mission",
  "mission.assigned": "mission",

  // ── Runs. Session provenance and tool spans describe one run each, so
  // they sit with the run lifecycle rather than under System.
  "run.session_init": "run",
  "run.agent_span": "run",

  // ── Provisioning / runtime freshness. sidecar.stale and image.stale are
  // both "this container is serving something older than the last deploy",
  // which is the provisioning story, not a system one.
  "provisioning.build_failed": "provisioning",
  "provisioning.step": "provisioning",
  "sidecar.stale": "provisioning",
  "image.stale": "provisioning",

  // ── Skills.
  "skill.invoked": "skill",

  // ── System. hook.dispatch_error was already in journal-groups.ts's
  // system exclusion list but missing here, so muting the System chip
  // excluded it server-side while the client still called it "other".
  "hook.dispatch_error": "system",
  "onboarding.proposal_applied": "system",
  "queue.sweeper_pumped": "system",
  "policy.changed": "system",
}

/** Short, dense label rendered inside the type pill on every log row. */
export const TYPE_PILL_LABEL: Record<string, string> = {
  "exec.command": "exec",
  "exec.output_chunk": "stdout",
  "network.egress": "egress",
  "network.port_opened": "port↑",
  "network.port_closed": "port↓",
  "file.written": "file",
  "container.metrics": "stats",
  "container.snapshot": "snapshot",
  "agent.status_change": "status",
  "run.started": "run·start",
  "run.completed": "run·done",
  "run.failed": "run·fail",
  "run.cancelled": "run·cancel",
  "run.timeout": "run·timeout",
  "peer.conversation": "peer",
  "peer.escalation": "escalate",
  "message.broadcast": "broadcast",
  "agent.mentioned": "mention",
  "keeper.decision": "keeper",
  "keeper.request": "keeper·req",
  "guardrail.input_blocked": "guard·in",
  "guardrail.output_blocked": "guard·out",
  "mission.status_change": "mission",
  "mission.comment": "mission·c",
  "crew.action": "crew",
  "assignment.created": "assign",
  "assignment.running": "assign·run",
  "assignment.completed": "assign·done",
  "assignment.failed": "assign·fail",
  "task.delegated": "delegate",
  "approval.requested": "approval",
  "approval.granted": "approval·ok",
  "approval.denied": "approval·no",
  "approval.timeout": "approval·to",
  "approval.cancelled": "approval·x",
  "cost.incurred": "cost",
  "budget.warning": "budget·warn",
  "budget.exceeded": "budget·over",
  "llm.call": "llm",
  "llm.cache_hit": "llm·cache",
  "skill.assigned": "skill+",
  "memory.updated": "memory",
  "memory.consolidated": "memory·c",
  "summary.generated": "summary",
  "system.compaction": "compact",
  "system.migration": "migration",
  "system.hook_toggled": "hook·tgl",
  "system.consolidation_triggered": "consol·start",
  "system.consolidation_completed": "consol·done",
  "checkpoint.created": "ckpt+",
  "checkpoint.restored": "ckpt↺",
  "fork.created": "fork+",
  "hook.fired": "hook",
  "hook.blocked": "hook·blk",
  "eval.run_started": "eval",
  "eval.metric": "eval·m",
  "eval.regression_detected": "eval·reg",
  "skill.unassigned": "skill−",
  "skill.imported": "skill·imp",
  "skill.deleted": "skill·del",
  "memory.priority_changed": "memory·prio",
  "credential.auto_assign_failed": "cred·auto·fail",
  "credential.auto_assign_empty": "cred·auto·empty",
  "audit.entity_created": "audit+",
  "audit.entity_updated": "audit~",
  "audit.entity_deleted": "audit−",
  "audit.entity_restored": "audit↺",
  "provisioning.queued": "prov·queue",
  "provisioning.building": "prov·build",
  "provisioning.complete": "prov·done",
  "provisioning.failed": "prov·fail",
  "chat.user_message": "chat·u",
  "chat.agent_response": "chat·a",
  "agent.error": "agent·err",
  // Routines
  "pipeline.run.started": "routine·start",
  "pipeline.run.completed": "routine·done",
  "pipeline.run.failed": "routine·fail",
  "pipeline.step.started": "step·start",
  "pipeline.step.completed": "step·done",
  "pipeline.step.failed": "step·fail",
  "pipeline.step.validation_failed": "step·invalid",
  "pipeline.step.skipped": "step·skip",
  "pipeline.step.retrying": "step·retry",
  "pipeline.step.container_ready": "step·ready",
  "pipeline.dry_run": "routine·dry",
  "pipeline.schedule.circuit_breaker_tripped": "sched·breaker",
  "pipeline.schedule.missed_occurrences": "sched·missed",
  "pipeline.runs_swept": "routine·swept",
  "automation.throttled": "auto·throttled",
  "automation.depth_exceeded": "auto·depth",
  // Pages
  "page.produce_denied": "page·denied",
  "page.panel.updated": "panel",
  "page.panel.stale": "panel·stale",
  "page.panel.recovered": "panel·ok",
  "page.wake.fired": "page·wake",
  "page.action.dispatched": "page·action",
  "page.spec.changed": "page·spec",
  "page.grant_added": "page·grant+",
  "page.grant_removed": "page·grant−",
  "page.owner_transferred": "page·owner",
  "page.published": "page·pub",
  "page.link_revoked": "page·link−",
  "page.public_view": "page·view",
  "page.webhook_issued": "page·hook+",
  "page.webhook_revoked": "page·hook−",
  // Memory
  "memory.write_rejected": "memory·rej",
  "memory.write_verifier_blocked": "memory·verif",
  "memory.consolidation_proposed": "consol·prop",
  "memory.searched": "memory·search",
  "memory.versions_swept": "memory·swept",
  "memory.config_updated": "memory·cfg",
  "memory.skill_proposed": "skill·prop",
  "memory.skill_approved": "skill·ok",
  "memory.skill_rejected": "skill·no",
  // Credentials / keeper
  "credential.revealed": "cred·reveal",
  "credential.reveal_policy_changed": "cred·policy",
  "credential.sensitivity_lowered": "cred·lower",
  "credential.lease_issued": "cred·lease",
  "keeper.rule_auto_tuned": "keeper·tune",
  // Approvals
  "approval.trust_granted": "trust+",
  "approval.trust_revoked": "trust−",
  "approval.auto_tuning_reset": "tune·reset",
  // Notifications
  "notification.delivered": "notify",
  "notification.failed": "notify·fail",
  "notification.dropped": "notify·drop",
  // Chat
  "conversation.compacted": "chat·compact",
  // Missions
  "mission.created": "mission+",
  "mission.assigned": "mission·assign",
  // Runs
  "run.session_init": "run·session",
  "run.agent_span": "span",
  // Provisioning / runtime freshness
  "provisioning.build_failed": "prov·build·fail",
  "provisioning.step": "prov·step",
  "sidecar.stale": "sidecar·stale",
  "image.stale": "image·stale",
  // Skills
  "skill.invoked": "skill·run",
  // System
  "hook.dispatch_error": "hook·err",
  "onboarding.proposal_applied": "onboard·apply",
  "queue.sweeper_pumped": "queue·sweep",
  "policy.changed": "policy",
}

export function groupOf(entryType: string): EntryGroup {
  return TYPE_TO_GROUP[entryType] ?? "other"
}

export function pillLabelOf(entryType: string): string {
  return TYPE_PILL_LABEL[entryType] ?? entryType
}

export function severityOf(s: string | undefined): JournalSeverity {
  if (s === "info" || s === "notice" || s === "warn" || s === "error") return s
  return "info"
}
