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

/**
 * A payload list, filtered to the strings in it.
 *
 * Non-strings are dropped rather than stringified: these render as
 * chips a reviewer reads as declarations, and `[object Object]` in that
 * position is worse than one fewer chip.
 */
export function payloadStrings(item: InboxItem, key: string): string[] {
  const v = item.payload?.[key]
  if (!Array.isArray(v)) return []
  return v.filter((x): x is string => typeof x === "string")
}

export function payloadNumber(item: InboxItem, key: string): number | null {
  const v = item.payload?.[key]
  return typeof v === "number" ? v : null
}

/**
 * Mirrors internal/notify.CategoryForItem, not just the flat kind map.
 *
 * A chat reply and a routine's progress notice are both kind=message, and only
 * payload.subkind separates them. The backend reads it; if this did not, the
 * category shown on a row would disagree with the category that actually
 * routed its notification — and the delivery settings link under it would send
 * the reader to the wrong switch.
 */
export function categoryOf(item: InboxItem): string {
  if (item.kind === "message" && payloadString(item, "subkind") === "routine_update") {
    return "routines.completed"
  }
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
  // seed carries the sender SLUG so the avatar can recognise Keeper without
  // depending on the display name (see isKeeper in inbox-actor). The facet id
  // stays the name, which is what the filter menu shows and groups by.
  return {
    kind: "system",
    id: item.sender_name ?? "system",
    label: item.sender_name ?? "system",
    seed: item.sender_id,
  }
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
  // The archive's "all time" window reaches past a year, where a bare
  // day/month makes two different years look like the same date.
  const then = new Date(iso)
  const sameYear = then.getFullYear() === new Date().getFullYear()
  return then.toLocaleDateString("en-GB",
    sameYear ? { day: "numeric", month: "short" } : { day: "numeric", month: "short", year: "numeric" })
}

export function absolute(iso?: string): string {
  if (!iso) return "—"
  return new Date(iso).toLocaleString("en-GB", {
    day: "numeric", month: "short", hour: "2-digit", minute: "2-digit",
  })
}

/**
 * How long is left, in the largest unit that still reads as a number.
 *
 * A waitpoint's default timeout is a day, and "expires in 1428m" is a figure
 * nobody converts in their head — the countdown is supposed to make urgency
 * obvious, and minutes stop doing that within the hour.
 */
export function remainingLabel(minutes: number): string {
  // Past the deadline is the most urgent state there is; the callers used to
  // guard on `> 0` and so rendered nothing at all for it.
  if (minutes <= 0) return "expired"
  if (minutes < 60) return `${minutes}m`
  if (minutes < 48 * 60) return `${Math.round(minutes / 60)}h`
  return `${Math.round(minutes / (24 * 60))}d`
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

/**
 * Is this row inside the archive window?
 *
 * Measured on resolved_at, not created_at: the archive answers "what did we
 * decide lately", and an item raised in March and closed yesterday belongs in
 * the last-7-days view. Rows that somehow have no resolved_at fall back to
 * when they arrived rather than vanishing.
 *
 * Client-side over the loaded page, which is honest only while the page IS the
 * archive; the server-side form is a `since` predicate on the same column.
 */
export function withinPeriod(item: InboxItem, period: string, now = Date.now()): boolean {
  if (period === "all") return true
  const days = Number(period)
  if (!Number.isFinite(days) || days <= 0) return true
  const at = Date.parse(item.resolved_at ?? item.created_at)
  if (Number.isNaN(at)) return true
  return now - at <= days * 24 * 60 * 60 * 1000
}

export interface DecisionMeta {
  heading: string
  tone: "warn" | "default"
  /** Which canRole action the server demands to resolve this. */
  requires: "create" | "manage"
  /** Set when the endpoint this card implies does not exist yet. */
  missingEndpoint?: string
  /** Where a decision on this item is POSTed, when the server can take one. */
  resolveEndpoint?: string
}

/**
 * What this row asks of a human, and which role the server lets do it.
 *
 * The `requires` values are read off the router: waitpoint approve and routine
 * approve are roleCreate (MANAGER+), credential access requests and
 * skill-proposal
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
    // A credential decision is resolved with roleManage — OWNER or ADMIN — and
    // the server addresses the item to ADMIN for exactly that reason. Saying
    // "create" here told a MANAGER the ruling was theirs to make when the server
    // would refuse them, and justified an audience wider than the people who can
    // act. The audience and the authority are one fact; this is its second copy,
    // and internal/api pins the first.
    // "Access request" was the heading for EVERY escalation, including a
    // yes/no question and a link to open. The heading is the first thing a
    // person reads on the card; it should say what is being asked.
    return {
      heading: escalationHeading(item),
      tone: "warn",
      requires: "manage",
      resolveEndpoint: payloadString(item, "request_id")
        ? `/api/v1/admin/keeper/requests/${payloadString(item, "request_id")}/resolve`
        : undefined,
    }
  }

  if (item.kind === "schedule_circuit_breaker_tripped") {
    // Re-enabling is PATCH pipeline-schedules/{id}, which is roleManage.
    return { heading: "Routine is disabled", tone: "warn", requires: "manage" }
  }

  if (item.kind === "schedule_missed") {
    return { heading: "Missed occurrences", tone: "warn", requires: "create" }
  }

  if (item.kind === "memory_consolidation") {
    return { heading: "Proposed memory consolidation", tone: "default", requires: "manage" }
  }

  return null
}

/**
 * The §12 attention class as a badge (B10, #2378; rendered since #2398).
 *
 * Names WHAT KIND OF ASK the row is, independent of `kind` (which names the
 * producer). Only the four values the server writes; anything else — an
 * empty column on a kind that has not adopted the contract, or a value a
 * newer server invents — renders nothing rather than a guess.
 */
export function attentionBadge(
  item: InboxItem,
): { label: string; tone: "warn" | "blue" | "purple" | "destructive" } | null {
  switch (item.attention_class) {
    case "decision": return { label: "Decision", tone: "warn" }
    case "input": return { label: "Input needed", tone: "blue" }
    case "review": return { label: "Review", tone: "purple" }
    case "repair": return { label: "Repair", tone: "destructive" }
  }
  return null
}

/**
 * The author-declared blast radius of an approval, or null when the item
 * carries none.
 *
 * Server-defaulted to "normal" at write time, so anything else here was
 * declared deliberately in the routine's wait step. Nothing infers it from
 * the prompt text: a heuristic looking for "delete" is wrong in both
 * directions, and the direction that matters — calling a destructive action
 * ordinary — fails silently.
 *
 * Returns null for "normal" as well as for absent, because the row only has
 * room to mark the exception.
 */
export function riskLevelOf(item: InboxItem): "destructive" | null {
  return payloadString(item, "risk_level") === "destructive" ? "destructive" : null
}

/**
 * Valid in-app chat deep link from a reply notification's payload, or null.
 *
 * The guard has to reject "//evil.example/x" as well as "https://…": a
 * protocol-relative URL starts with "/" and the browser resolves it against
 * the current scheme, so a payload an agent controls could navigate a manager
 * off-origin from a link that looks internal. One leading slash, not two, and
 * no backslash either — some parsers fold "/\" onto "//".
 *
 * Exported and shared with kind-actions, which had the only copy. Keeping two
 * is exactly the shape that drifts, and the half that had no guard was the one
 * deciding whether a destination got named at all.
 */
export function safeChatURL(item: InboxItem): string | null {
  const v = payloadString(item, "chat_url")
  if (!v || !v.startsWith("/")) return null
  if (v.startsWith("//") || v.startsWith("/\\")) return null
  return v
}

/**
 * Non-decision rows still have somewhere to go.
 *
 * `href` is not optional decoration: this returned a label and an icon only,
 * so the detail pane rendered a button that NAMED a destination and went
 * nowhere — the one control on the pane for reaching the run it describes.
 * A jump target without an href is not a jump target.
 */
export function jumpFor(
  item: InboxItem,
): { label: string; icon: LucideIcon; href: string } | null {
  const chat = safeChatURL(item)
  if (chat) return { label: "Open chat", icon: MessageSquare, href: chat }
  const issue = payloadString(item, "issue_identifier")
  if (issue) {
    return { label: `Open ${issue}`, icon: CircleDot, href: `/issues/${encodeURIComponent(issue)}` }
  }
  const run = payloadString(item, "pipeline_run_id")
  if (run) {
    return { label: "Open run", icon: ArrowUpRight, href: `/activity?run=${encodeURIComponent(run)}` }
  }
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
  dismissed: "default",
  retried: "blue",
  expired: "warn",
}

/** The decision card's heading for an escalation, by what it asks for. */
export function escalationHeading(item: InboxItem): string {
  if (item.payload?.request_type === "access") return "Access request"
  switch (payloadString(item, "escalation_type")) {
    case "LINK": return "A link to open"
    case "CREDENTIAL": return "Credential request"
    case "TEXT": return "Question from an agent"
  }
  return "Escalation"
}

/**
 * Who may decide, in words. The card used to print the role enums verbatim
 * ("OWNER or ADMIN decides this", "MANAGER+"), which README §6 rules out.
 */
export function deciderCopy(requires: "create" | "manage"): string {
  return requires === "manage" ? "An owner or admin decides this" : "A manager or above decides this"
}

/**
 * A valid https link the agent asked to open, from the row's payload. The
 * server validates LINK metadata as https before it lands here; this is the
 * client's own guard so a payload written by anything else cannot navigate a
 * person to javascript: or to a bare host.
 */
export function linkToOpen(item: InboxItem): string | null {
  const v = payloadString(item, "link_url")
  if (!v || !/^https:\/\/[^\s/]+/i.test(v)) return null
  return v
}
