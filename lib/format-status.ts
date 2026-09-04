/**
 * One place that turns a status enum into a word and a tone.
 *
 * Before this existed the crews area alone carried thirteen local pill maps,
 * none matching the dashboard's, and raw enums (IN_PROGRESS, pending_review,
 * SEALED) reached the screen. Every status pill in the product reads from
 * here; a status this map does not know renders as a readable version of its
 * own name ("awaiting approval") in the muted tone, never as the raw enum.
 */
export type StatusTone = "success" | "blue" | "warn" | "danger" | "muted" | "purple"

export interface StatusMeta {
  label: string
  tone: StatusTone
}

const STATUS: Record<string, StatusMeta> = {
  // work
  BACKLOG: { label: "Backlog", tone: "muted" },
  TODO: { label: "Todo", tone: "muted" },
  PLANNING: { label: "Planning", tone: "muted" },
  IN_PROGRESS: { label: "In progress", tone: "blue" },
  REVIEW: { label: "Review", tone: "warn" },
  COMPLETED: { label: "Done", tone: "success" },
  DONE: { label: "Done", tone: "success" },
  FAILED: { label: "Failed", tone: "danger" },
  CANCELLED: { label: "Cancelled", tone: "muted" },
  DUPLICATE: { label: "Duplicate", tone: "muted" },
  // tasks
  PENDING: { label: "Pending", tone: "muted" },
  BLOCKED: { label: "Blocked", tone: "warn" },
  SKIPPED: { label: "Skipped", tone: "muted" },
  AWAITING_APPROVAL: { label: "Awaiting approval", tone: "warn" },
  // runs and routines
  RUNNING: { label: "Running", tone: "blue" },
  QUEUED: { label: "Queued", tone: "muted" },
  PAUSED: { label: "Paused", tone: "warn" },
  WAITING: { label: "Waiting", tone: "warn" },
  INTERRUPTED: { label: "Interrupted", tone: "warn" },
  TIMEOUT: { label: "Timed out", tone: "danger" },
  SUCCEEDED: { label: "Succeeded", tone: "success" },
  SUCCESS: { label: "Succeeded", tone: "success" },
  OK: { label: "OK", tone: "success" },
  ERROR: { label: "Error", tone: "danger" },
  // agents
  IDLE: { label: "Idle", tone: "success" },
  ACTIVE: { label: "Active", tone: "success" },
  OFFLINE: { label: "Offline", tone: "muted" },
  // credentials, approvals, misc
  MISSING: { label: "Missing", tone: "warn" },
  EXPIRED: { label: "Expired", tone: "danger" },
  REVOKED: { label: "Revoked", tone: "muted" },
  PENDING_REVIEW: { label: "Pending review", tone: "warn" },
  APPROVED: { label: "Approved", tone: "success" },
  REJECTED: { label: "Rejected", tone: "danger" },
  DENIED: { label: "Denied", tone: "danger" },
  HELD: { label: "Held", tone: "warn" },
  DISABLED: { label: "Disabled", tone: "muted" },
  ENABLED: { label: "Enabled", tone: "success" },
  STALE: { label: "Stale", tone: "warn" },
  FRESH: { label: "Fresh", tone: "success" },
  NEVER_PRODUCED: { label: "Never produced", tone: "muted" },
  CONNECTED: { label: "Connected", tone: "success" },
  CONNECTING: { label: "Connecting", tone: "warn" },
  DEGRADED: { label: "Degraded", tone: "warn" },
  STANDARD: { label: "Standard", tone: "muted" },
  SEALED: { label: "Sealed", tone: "purple" },
  INITIATED: { label: "Initiated", tone: "blue" },
}

/** Human words for a raw status. Case-insensitive; dashes and spaces are
 *  treated as underscores. Unknown values become "Title case words". */
export function formatStatus(raw: string | null | undefined): StatusMeta {
  if (!raw) return { label: "Unknown", tone: "muted" }
  const key = raw.trim().toUpperCase().replace(/[-\s]+/g, "_")
  const hit = STATUS[key]
  if (hit) return hit
  const words = key.toLowerCase().split("_").filter(Boolean)
  const label = words.length ? words[0][0].toUpperCase() + words[0].slice(1) + (words.length > 1 ? " " + words.slice(1).join(" ") : "") : "Unknown"
  return { label, tone: "muted" }
}

export function isKnownStatus(raw: string): boolean {
  return Object.hasOwn(STATUS, raw.trim().toUpperCase().replace(/[-\s]+/g, "_"))
}
