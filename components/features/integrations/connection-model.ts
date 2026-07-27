// The unified row model behind the Integrations page.
//
// The page shows two things that a user thinks of as one — "what has this
// workspace been connected to" — but which come from two unrelated backends:
// notification channels (internal/notify) and managed tool accounts
// (Composio). Rendering them as two stacked panels is what the previous page
// did, and it is why nothing on it was findable: the same question ("is Slack
// hooked up?") had two different places to look, with two different layouts.
//
// So both are normalised to ConnectionRow here, once, and every view and facet
// downstream works on that. `source` and `raw` survive the normalisation so a
// row can still open the editor that actually owns it.

import type { NotificationChannel } from "@/hooks/use-notification-channels"
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"
import type { ConnectedAccount } from "./composio/types"

/** Coarse kind — the KIND facet, and the icon a row gets. */
export type ConnectionKind = "chat" | "push" | "incident" | "email" | "webhook" | "tools"

/**
 * Row health.
 *
 * `unknown` is a real, distinct value and not a fallback for "probably fine":
 * the delivery log is ADMIN-only, so a MEMBER genuinely cannot be told whether
 * a channel is delivering. Rendering that as "delivering" would be inventing
 * an answer, which is the failure this page exists to stop repeating.
 */
export type ConnectionStatus = "delivering" | "failing" | "never_used" | "disabled" | "unknown"

export interface ConnectionRow {
  id: string
  kind: ConnectionKind
  /** Primary label — what the user named it, or the destination itself. */
  name: string
  /** Second line: provider · target. Monospace in the table. */
  detail: string
  /** Provider key for the PROVIDER facet: discord | slack | email | composio … */
  provider: string
  /** Provider label for display. */
  providerLabel: string
  scope: "workspace" | "personal"
  enabled: boolean
  /** Admin allowlist; empty = every category. */
  categories: string[]
  status: ConnectionStatus
  /** Sends in the delivery window, when the caller may read the log. */
  sent24h: number | null
  /** ISO timestamp of the most recent delivery, when known. */
  lastDelivery: string | null
  source: "channel" | "composio"
  /**
   * true = this row belongs to somebody else and the caller is only seeing it
   * because they are an admin looking at `?scope=all`. Its destination was
   * redacted server-side, and none of the row actions apply — you do not get
   * to test or delete another member's personal channel from an overview.
   */
  readOnly: boolean
  channel?: NotificationChannel
  account?: ConnectedAccount
}

/** Every facet the sidebar exposes. `null`/empty = no constraint. */
export interface ConnectionFilters {
  kind: ConnectionKind | "all"
  status: ConnectionStatus | "all"
  scope: "workspace" | "personal" | "all"
  provider: string | null
}

export const EMPTY_FILTERS: ConnectionFilters = {
  kind: "all",
  status: "all",
  scope: "all",
  provider: null,
}

export function filtersActive(f: ConnectionFilters): number {
  let n = 0
  if (f.kind !== "all") n++
  if (f.status !== "all") n++
  if (f.scope !== "all") n++
  if (f.provider) n++
  return n
}

/**
 * The kind a notification channel belongs to.
 *
 * `type: "shoutrrr"` is the stored value for every chat/push/incident
 * destination — a delivery-library name that predates the provider catalogue.
 * Which of the three it actually is comes from the provider's own category,
 * served by GET /api/v1/notification-providers. When that lookup misses (older
 * server, provider since removed) it degrades to "chat" rather than dropping
 * the row: an unfamiliar destination is still a destination.
 */
export function channelKind(
  ch: NotificationChannel,
  providerCategory: (provider: string) => string | undefined,
): ConnectionKind {
  if (ch.type === "email") return "email"
  if (ch.type === "webhook") return "webhook"
  const cat = ch.provider ? providerCategory(ch.provider) : undefined
  if (cat === "push") return "push"
  if (cat === "incident") return "incident"
  return "chat"
}

/**
 * Derive a row's health from the delivery log.
 *
 * `deliveries` is the caller's slice of the log for this channel; pass null
 * when the caller may not read it (see ConnectionStatus.unknown). Order of the
 * checks matters: a disabled channel is disabled no matter what its history
 * says, and a recent failure outranks an older success — the point of the
 * column is to surface what needs attention, not to average it away.
 */
export function deriveStatus(
  enabled: boolean,
  deliveries: NotificationDelivery[] | null,
): ConnectionStatus {
  if (!enabled) return "disabled"
  if (deliveries === null) return "unknown"
  if (deliveries.length === 0) return "never_used"
  const newestFirst = [...deliveries].sort((a, b) => b.created_at.localeCompare(a.created_at))
  const latest = newestFirst[0]
  if (latest.status === "failed") return "failing"
  if (newestFirst.some((d) => d.status === "sent")) return "delivering"
  // Everything present was dropped by preference or rate limit, or is still
  // pending — nothing has actually left the building.
  return "never_used"
}

/**
 * Is this channel somebody else's personal one?
 *
 * Only ever true on the admin `?scope=all` listing. `currentUserId` may be
 * empty while the session is still loading — treat that as "not mine to
 * judge yet" and return false, so a row does not flicker from editable to
 * read-only (or, far worse, the other way).
 */
export function isForeignPersonal(
  ch: NotificationChannel,
  currentUserId: string | null | undefined,
): boolean {
  if (ch.scope !== "user") return false
  if (!currentUserId) return false
  return Boolean(ch.owner_user_id) && ch.owner_user_id !== currentUserId
}

export const STATUS_LABEL: Record<ConnectionStatus, string> = {
  delivering: "Delivering",
  failing: "Failing",
  never_used: "Never used",
  disabled: "Disabled",
  unknown: "Enabled",
}

export const KIND_LABEL: Record<ConnectionKind, string> = {
  chat: "Chat",
  push: "Push",
  incident: "Incident",
  email: "E-mail",
  webhook: "Webhook",
  tools: "Tools (MCP)",
}

/** Does a row match the free-text search box? */
export function rowMatches(row: ConnectionRow, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  return (
    row.name.toLowerCase().includes(q) ||
    row.detail.toLowerCase().includes(q) ||
    row.provider.toLowerCase().includes(q) ||
    row.providerLabel.toLowerCase().includes(q) ||
    row.categories.some((c) => c.toLowerCase().includes(q))
  )
}

/** Apply the facets (not the search box — see rowMatches). */
export function applyFilters(rows: ConnectionRow[], f: ConnectionFilters): ConnectionRow[] {
  return rows.filter((r) => {
    if (f.kind !== "all" && r.kind !== f.kind) return false
    if (f.status !== "all" && r.status !== f.status) return false
    if (f.scope !== "all" && r.scope !== f.scope) return false
    if (f.provider && r.provider !== f.provider) return false
    return true
  })
}
