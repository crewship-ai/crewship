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
  | "lifecycle"   // run / mission / assignment / approval / provisioning
  | "security"    // keeper / audit
  | "ai"          // chat / cost / skill / memory
  | "workspace"   // peer / system / other

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
  other: "other",
}

/** Render order in the type-chip filter row. */
export const GROUP_ORDER: EntryGroup[] = [
  "exec",
  "network",
  "file",
  "container",
  "provisioning",
  "run",
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
  "system",
  "other",
]

/**
 * Bundle membership — used by the Timeline toolbar's "5-bundle" chip
 * mode to collapse 18 base groups into 5 user-meaningful domains.
 * Toggling a bundle toggles every base group inside it.
 */
export const GROUP_TO_BUNDLE: Record<EntryGroup, EntryBundle> = {
  exec: "runtime",
  network: "runtime",
  file: "runtime",
  container: "runtime",
  run: "lifecycle",
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

const TYPE_TO_GROUP: Record<string, EntryGroup> = {
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
}

/** Short, dense label rendered inside the type pill on every log row. */
const TYPE_PILL_LABEL: Record<string, string> = {
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
