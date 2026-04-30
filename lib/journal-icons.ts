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
  Brain,
  Briefcase,
  CheckCircle,
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
  Wand2,
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
}

/**
 * Return the lucide icon for an entry type, falling back to a neutral
 * icon for unknown types so new backend events don't break the UI.
 */
export function iconForEntryType(entryType: string): LucideIcon {
  return (JOURNAL_ENTRY_ICONS[entryType as JournalEntryType] ?? Activity) as LucideIcon
}
