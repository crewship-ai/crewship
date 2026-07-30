import { ArrowUpRight, CircleDot, MessageSquare } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import type { InboxItem } from "@/hooks/use-inbox"

import type { Actor, Bucket } from "./inbox-types"

// Derivations the inbox surface shares. Everything here reads a field the Go
// producers actually write; the call sites are named beside each one.

export type WorkspaceRole = "OWNER" | "ADMIN" | "MANAGER" | "MEMBER" | "VIEWER"

/**
 * Mirrors internal/api/helpers.go canRole. "create" is MANAGER and up,
 * "manage" is OWNER/ADMIN only — the gap that makes a MANAGER-targeted skill
 * proposal undecidable by the very role it was addressed to.
 */
export function canRole(role: WorkspaceRole | null, action: "create" | "manage"): boolean {
  if (!role) return false
  if (action === "manage") return role === "OWNER" || role === "ADMIN"
  return role === "OWNER" || role === "ADMIN" || role === "MANAGER"
}

/** Mirrors internal/notify/categories.go categoryByKind. */
export const CATEGORY_BY_KIND: Record<string, string> = {
  waitpoint: "agents.approval",
  escalation: "agents.escalation",
  failed_run: "routines.failed",
  message: "chat.replies",
  memory_consolidation: "memory",
  schedule_missed: "routines.missed",
  schedule_circuit_breaker_tripped: "routines.missed",
}

export function payloadString(item: InboxItem, key: string): string {
  const v = item.payload?.[key]
  return typeof v === "string" ? v : ""
}

export function payloadNumber(item: InboxItem, key: string): number | null {
  const v = item.payload?.[key]
  return typeof v === "number" ? v : null
}

export function categoryOf(item: InboxItem): string {
  return CATEGORY_BY_KIND[item.kind] ?? item.kind
}

export function bucketOf(item: InboxItem): Bucket {
  if (item.blocking || item.kind === "waitpoint" || item.kind === "escalation") return "decisions"
  if (item.kind === "message" && payloadString(item, "chat_url")) return "replies"
  if (item.kind === "message" && payloadString(item, "issue_identifier")) return "review"
  if (item.kind === "message" && payloadString(item, "subkind") === "routine_update") return "routines"
  if (item.kind.startsWith("schedule_")) return "routines"
  return "other"
}

/**
 * Who the row is ABOUT.
 *
 * Ten producers write their rows as sender_type=system ("Keeper", "Skill
 * Curator", "Memory Health") while carrying agent_name in the payload — the
 * agent the row concerns. Keying identity off sender_type alone, as the live
 * inbox does, puts a system glyph on "casey requested GH_TOKEN".
 */
export function subjectOf(item: InboxItem): Actor {
  const agent = payloadString(item, "agent_name") || payloadString(item, "agent_slug")
  if (agent) return { kind: "agent", id: agent, label: agent, seed: agent }
  if (item.sender_type === "agent" && item.sender_name) {
    return { kind: "agent", id: item.sender_name, label: item.sender_name, seed: item.avatar_seed || item.sender_name }
  }
  if (item.sender_type === "pipeline" && item.sender_name) {
    return { kind: "routine", id: item.sender_name, label: item.sender_name }
  }
  if (item.sender_type === "crew" && item.sender_name) {
    return { kind: "crew", id: item.sender_name, label: item.sender_name }
  }
  return { kind: "system", id: item.sender_name ?? "system", label: item.sender_name ?? "system" }
}

/** The human who closed it — archive only. */
export function resolverOf(item: InboxItem): Actor | null {
  if (!item.resolved_by_user_id) return null
  return { kind: "user", id: item.resolved_by_user_id, label: item.resolved_by_user_id }
}

/** Fixed-clock relative time — see Date.now(). */
export function since(iso?: string): string {
  if (!iso) return "—"
  const mins = Math.round((Date.now() - Date.parse(iso)) / 60_000)
  if (mins < 1) return "just now"
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return new Date(iso).toLocaleDateString("en-GB", { day: "numeric", month: "short" })
}

export function absolute(iso?: string): string {
  if (!iso) return "—"
  return new Date(iso).toLocaleString("en-GB", {
    day: "numeric", month: "short", hour: "2-digit", minute: "2-digit",
  })
}

export function durationLabel(minutes: number | null): string {
  if (minutes == null) return "—"
  if (minutes < 60) return `${minutes}m`
  return `${Math.round(minutes / 60)}h`
}

/** Minutes left on a waitpoint, from the payload key the current UI drops. */
export function expiresIn(item: InboxItem): number | null {
  const raw = payloadString(item, "timeout_at")
  if (!raw) return null
  const mins = Math.round((Date.parse(raw) - Date.now()) / 60_000)
  return Number.isFinite(mins) ? mins : null
}

export interface DecisionMeta {
  heading: string
  tone: "warn" | "default"
  /** Which canRole action the server demands to resolve this. */
  requires: "create" | "manage"
  /** Set when the endpoint this card implies does not exist yet. */
  missingEndpoint?: string
}

/**
 * What this row asks of a human, and which role the server lets do it.
 *
 * The `requires` values are read off the router: waitpoint approve, escalation
 * resolve and routine approve are roleCreate (MANAGER+), while skill-proposal
 * and consolidation approve are roleManage (OWNER/ADMIN). That mismatch — a
 * MANAGER-targeted row whose decision needs ADMIN — is why the card names who
 * decides instead of offering a button that returns 403.
 *
 * The buttons themselves come from KindActions, which owns the endpoints.
 */
export function decisionMetaFor(item: InboxItem): DecisionMeta | null {
  const sub = payloadString(item, "kind")

  if (item.kind === "waitpoint") {
    return { heading: "Waiting on your decision", tone: "warn", requires: "create" }
  }

  if (item.kind === "escalation") {
    if (sub === "skill_proposal") {
      return { heading: "Proposed skill", tone: "warn", requires: "manage" }
    }
    if (sub === "routine_proposal") {
      return { heading: "Proposed routine", tone: "warn", requires: "create" }
    }
    return {
      heading: "Access request",
      tone: "warn",
      requires: "create",
      missingEndpoint: payloadString(item, "request_type") === "access"
        ? "a keeper request has no resolve endpoint yet"
        : undefined,
    }
  }

  if (item.kind === "schedule_circuit_breaker_tripped") {
    return { heading: "Routine is disabled", tone: "warn", requires: "create" }
  }

  if (item.kind === "schedule_missed") {
    return { heading: "Missed occurrences", tone: "warn", requires: "create" }
  }

  if (item.kind === "memory_consolidation") {
    return { heading: "Proposed memory consolidation", tone: "default", requires: "manage" }
  }

  return null
}

/** Non-decision rows still have somewhere to go. */
export function jumpFor(item: InboxItem): { label: string; icon: LucideIcon } | null {
  if (payloadString(item, "chat_url")) return { label: "Open chat", icon: MessageSquare }
  const issue = payloadString(item, "issue_identifier")
  if (issue) return { label: `Open ${issue}`, icon: CircleDot }
  if (payloadString(item, "pipeline_run_id")) return { label: "Open run", icon: ArrowUpRight }
  return null
}

export const OUTCOME_LABEL: Record<string, string> = {
  approved: "approved",
  rejected: "rejected",
  archived: "archived",
  retried: "retried",
  dismissed: "dismissed",
  expired: "expired",
}

export const OUTCOME_TONE: Record<string, "success" | "destructive" | "warn" | "blue" | "default"> = {
  approved: "success",
  rejected: "destructive",
  archived: "default",
  retried: "blue",
  expired: "warn",
}
