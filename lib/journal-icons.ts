// Map every JournalEntryType to a lucide icon. Used by the journal
// entry card to give each row a visual hint about *what kind* of event
// it is, alongside the textual entry_type badge.
//
// Keep this file mirrored with internal/journal/types.go EntryType
// constants and lib/types/journal.ts JOURNAL_ENTRY_TYPES — when a new
// type lands on the backend, add an icon here. Unknown types fall back
// to a neutral icon at render time.

import {
  Activity,
  AlertTriangle,
  Ban,
  BookmarkCheck,
  Bot,
  Boxes,
  Brain,
  Briefcase,
  CheckCircle,
  Eye,
  FilePen,
  FilePlus,
  FileX,
  Gauge,
  KeyRound,
  Search,
  SkipForward,
  Trash2,
  UserMinus,
  ClipboardCheck,
  ClipboardList,
  ClipboardX,
  Clock,
  Database,
  DollarSign,
  Flag,
  GitFork,
  Globe,
  Hammer,
  Hash,
  LayoutTemplate,
  Megaphone,
  MessageSquare,
  MessageSquareWarning,
  Microscope,
  Network,
  PackageOpen,
  Play,
  PlugZap,
  RotateCcw,
  ScrollText,
  ShieldAlert,
  ShieldCheck,
  ShieldOff,
  Sparkles,
  Terminal,
  TrendingDown,
  Unplug,
  UserCheck,
  UserPlus,
  Wand2,
  Webhook,
  XCircle,
  Zap,
  type LucideIcon,
} from "lucide-react"

import type { JournalEntryType } from "@/lib/types/journal"

// Indexed Record so the compiler complains if a new EntryType is added
// to lib/types/journal.ts but forgotten here. Missing keys fall through
// to the unknown-type fallback in iconForEntryType.
export const JOURNAL_ENTRY_ICONS: Partial<Record<JournalEntryType, LucideIcon>> = {
  // Communication
  "peer.conversation": MessageSquare,
  "peer.escalation": MessageSquareWarning,
  "message.broadcast": Megaphone,
  "agent.mentioned": Hash,

  // Mission / task
  "mission.status_change": Flag,
  "mission.created": ClipboardList,
  "mission.assigned": UserPlus,
  "mission.comment": ClipboardList,
  "assignment.created": ClipboardList,
  "assignment.running": Play,
  "assignment.completed": ClipboardCheck,
  "assignment.failed": ClipboardX,
  "crew.action": Briefcase,
  "task.delegated": UserCheck,

  // Runs
  "run.started": Play,
  "run.completed": CheckCircle,
  "run.failed": XCircle,
  "run.cancelled": Ban,
  "run.timeout": AlertTriangle,

  // Security
  "keeper.request": ShieldAlert,
  "keeper.decision": ShieldCheck,
  "guardrail.input_blocked": ShieldOff,
  "guardrail.output_blocked": ShieldOff,
  "approval.requested": Hammer,
  "approval.granted": CheckCircle,
  "approval.denied": XCircle,
  "approval.timeout": Clock,
  "approval.cancelled": Ban,

  // Cost
  "llm.call": Sparkles,
  "llm.cache_hit": BookmarkCheck,
  "cost.incurred": DollarSign,
  "budget.exceeded": TrendingDown,
  "budget.warning": AlertTriangle,

  // Memory
  "memory.updated": Brain,
  "memory.consolidated": Database,
  "summary.generated": Wand2,

  // Observability (Crow's Nest)
  "exec.command": Terminal,
  "exec.output_chunk": ScrollText,
  "network.port_opened": PlugZap,
  "network.port_closed": Unplug,
  "network.egress": Globe,
  "file.written": PackageOpen,
  "container.metrics": Activity,
  "container.snapshot": ClipboardCheck,

  // Presence
  "agent.status_change": Network,

  // Checkpointing
  "checkpoint.created": Flag,
  "checkpoint.restored": RotateCcw,
  "fork.created": GitFork,

  // Hooks
  "hook.fired": Zap,
  "hook.blocked": ShieldOff,

  // Eval
  "eval.run_started": Microscope,
  "eval.metric": Activity,
  "eval.regression_detected": TrendingDown,

  // System
  "system.compaction": RotateCcw,
  "system.migration": Database,
  "system.hook_toggled": Zap,
  "system.consolidation_triggered": Wand2,
  "system.consolidation_completed": CheckCircle,

  // Outbound notifications. All three share the Webhook glyph on purpose:
  // the thing worth spotting at a glance is "something left this instance
  // and went somewhere else", and the row's tone (success / error / warn)
  // carries whether it landed. A distinct icon per outcome would make the
  // three read as three unrelated kinds of event.
  "notification.delivered": Webhook,
  "notification.failed": Webhook,
  "notification.dropped": Webhook,

  // ── Routine engine ────────────────────────────────────────────────
  // Deliberately the same glyphs as the run.* family above. A reader
  // scanning the feed is asking "did this work", not "which engine ran
  // it", and giving the pipeline engine its own vocabulary would make
  // one product concept read as two.
  "pipeline.run.started": Play,
  "pipeline.run.completed": CheckCircle,
  "pipeline.run.failed": XCircle,
  "pipeline.step.started": Play,
  "pipeline.step.completed": CheckCircle,
  "pipeline.step.failed": XCircle,
  "pipeline.step.validation_failed": ClipboardX,
  "pipeline.step.skipped": SkipForward,
  "pipeline.step.retrying": RotateCcw,
  "pipeline.step.container_ready": Boxes,
  "pipeline.dry_run": Microscope,
  "pipeline.schedule.circuit_breaker_tripped": ShieldAlert,
  "pipeline.schedule.missed_occurrences": Clock,
  "pipeline.runs_swept": Trash2,

  // ── Automation ────────────────────────────────────────────────────
  // Both are a rule being REFUSED, and the distinction matters: one is
  // "too often" (raise the cap or debounce it), the other is "too deep"
  // (you have built a cycle). One glyph for both would hide which.
  "automation.throttled": Gauge,
  "automation.depth_exceeded": ShieldAlert,

  // ── Chat ──────────────────────────────────────────────────────────
  "chat.user_message": MessageSquare,
  "chat.agent_response": Bot,
  "conversation.compacted": RotateCcw,

  // ── Provisioning ──────────────────────────────────────────────────
  "provisioning.queued": Clock,
  "provisioning.building": Hammer,
  "provisioning.step": Hammer,
  "provisioning.complete": CheckCircle,
  "provisioning.failed": XCircle,
  "provisioning.build_failed": XCircle,
  "sidecar.stale": AlertTriangle,
  "image.stale": AlertTriangle,

  // ── Credentials ───────────────────────────────────────────────────
  "credential.revealed": Eye,
  "credential.lease_issued": KeyRound,
  "credential.reveal_policy_changed": ShieldCheck,
  "credential.sensitivity_lowered": ShieldAlert,
  "credential.auto_assign_failed": XCircle,
  "credential.auto_assign_empty": Ban,

  // ── Skills ────────────────────────────────────────────────────────
  "skill.imported": PackageOpen,
  "skill.deleted": Trash2,
  "skill.assigned": UserPlus,
  "skill.unassigned": UserMinus,
  "skill.invoked": Sparkles,

  // ── Audit ─────────────────────────────────────────────────────────
  // Four distinct ACTIONS, unlike the notification family above, which
  // is one action with three outcomes. Created and deleted are opposites
  // and must not share a glyph.
  "audit.entity_created": FilePlus,
  "audit.entity_updated": FilePen,
  "audit.entity_deleted": FileX,
  "audit.entity_restored": RotateCcw,

  // ── Memory ────────────────────────────────────────────────────────
  "memory.searched": Search,
  "memory.write_rejected": Ban,
  "memory.write_verifier_blocked": ShieldOff,
  "memory.consolidation_proposed": Wand2,
  "memory.versions_swept": Trash2,
  "memory.config_updated": Database,
  "memory.skill_proposed": Sparkles,
  "memory.skill_approved": CheckCircle,
  "memory.skill_rejected": XCircle,

  // ── Agent ─────────────────────────────────────────────────────────
  "run.agent_span": Activity,
  "agent.error": AlertTriangle,

  // ── Pages ─────────────────────────────────────────────────────────
  // Same glyph CONCEPT_ICON.pages uses (lib/concept-icons.ts) for a panel
  // successfully pushed to; denial and grant changes get their own faces
  // so a scan of the feed can tell "data arrived" from "access changed".
  "page.panel.updated": LayoutTemplate,
  "page.produce_denied": ShieldOff,
  "page.grant_added": UserPlus,
  "page.grant_removed": UserMinus,
  // Freshness (§4) reads as a clock going quiet and a clock catching up; a
  // wake gate (§5) is the one entry here that means an agent was started.
  "page.panel.stale": Clock,
  "page.panel.recovered": CheckCircle,
  "page.wake.fired": Zap,
  // A human pressed a button on a page and a routine was enqueued (§8b.2).
  "page.action.dispatched": Play,
}

/**
 * Return the lucide icon for an entry type, falling back to a neutral
 * icon for unknown types so new backend events don't break the UI.
 */
export function iconForEntryType(entryType: string): LucideIcon {
  return (JOURNAL_ENTRY_ICONS[entryType as JournalEntryType] ?? Activity) as LucideIcon
}
