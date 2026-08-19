// Notification category vocabulary — the TS mirror of
// internal/notify/categories.go (CategoryGroups / AllCategories).
//
// Keep the two in sync: the Go side is authoritative (it validates every
// write and drives the DB CHECK), this side only renders. The category keys
// must match byte for byte — a typo here shows an empty column rather than
// erroring, so lib/__tests__/notification-categories.test.ts pins the list.
//
// Taxonomy v2 (2026-07) replaced the original 9 categories, of which four
// (runs.completed, security, budget, system) had no producer at all and were
// switchable rows that could never deliver anything. See the Go file for the
// full reasoning.

export interface NotificationCategory {
  /** Stable key — must equal the Go constant's value. */
  key: string
  /** Row label in the preference matrix. */
  label: string
  /** One-line explanation of when this fires, shown on hover. */
  hint: string
}

export interface NotificationCategoryGroup {
  key: string
  label: string
  categories: NotificationCategory[]
}

export const NOTIFICATION_CATEGORY_GROUPS: NotificationCategoryGroup[] = [
  {
    key: "routines",
    label: "Routines",
    categories: [
      { key: "routines.completed", label: "Completed", hint: "A routine run finished successfully" },
      { key: "routines.failed", label: "Failed", hint: "A routine run ended in an error" },
      { key: "routines.skipped", label: "Skipped", hint: "A scheduled run was skipped by its catch-up policy" },
      {
        key: "routines.missed",
        label: "Not running",
        hint: "A schedule missed occurrences, or was auto-disabled after repeated failures",
      },
    ],
  },
  {
    key: "issues",
    label: "Issues",
    categories: [
      { key: "issues.created", label: "Created", hint: "A new issue was opened" },
      { key: "issues.state", label: "Status changed", hint: "An issue moved between states, including blocked" },
      { key: "issues.assigned", label: "Assigned", hint: "An issue was assigned to someone" },
      { key: "issues.comment", label: "Commented", hint: "Someone commented on an issue" },
    ],
  },
  {
    key: "agents",
    label: "Agents",
    categories: [
      { key: "agents.approval", label: "Approval needed", hint: "An agent is blocked waiting for your decision" },
      { key: "agents.escalation", label: "Escalation", hint: "An agent escalated something to a human" },
      { key: "agents.error", label: "Errors", hint: "An agent container failed to start or crashed" },
      { key: "agents.budget", label: "Budget", hint: "A spend limit was reached or exceeded" },
    ],
  },
  {
    key: "system",
    label: "System",
    categories: [
      { key: "system.health", label: "Instance health", hint: "The instance itself reported a problem" },
      { key: "system.migration", label: "Migrations", hint: "A schema migration ran on this instance" },
      { key: "security", label: "Security", hint: "A guardrail fired, or an egress attempt was blocked" },
      { key: "pages.stale", label: "Stale pages", hint: "A page you own has a panel past its freshness SLA" },
    ],
  },
  {
    key: "other",
    label: "Chat & memory",
    categories: [
      { key: "chat.replies", label: "Chat replies", hint: "An agent replied while you were away" },
      { key: "memory", label: "Memory", hint: "A memory consolidation needs review" },
    ],
  },
]

/** Flat, ordered category list — mirrors notify.AllCategories. */
export const NOTIFICATION_CATEGORIES: NotificationCategory[] =
  NOTIFICATION_CATEGORY_GROUPS.flatMap((g) => g.categories)

/** Just the keys, for allowlist checkboxes and API payloads. */
export const NOTIFICATION_CATEGORY_KEYS: string[] = NOTIFICATION_CATEGORIES.map((c) => c.key)

/**
 * The per-channel "mute everything" sentinel. Not a real category — it is a
 * cell state that overrides every other cell on that channel.
 */
export const MUTE_CATEGORY = "*"

/**
 * Retired category names mapped to their replacement, so a delivery-log row
 * written before taxonomy v2 still renders a meaningful label instead of a
 * raw key. Migration v169 rewrote the preference and allowlist tables, but
 * notification_deliveries is a historical log and keeps its original values
 * on purpose — rewriting it would falsify what was actually delivered.
 */
export const LEGACY_CATEGORY_LABELS: Record<string, string> = {
  approvals: "Approval needed",
  escalations: "Escalation",
  "runs.failed": "Failed",
  "runs.completed": "Completed",
  budget: "Budget",
  system: "System",
}

/** Human label for a category key, tolerating retired and unknown values. */
export function labelForCategory(key: string): string {
  const found = NOTIFICATION_CATEGORIES.find((c) => c.key === key)
  if (found) return found.label
  return LEGACY_CATEGORY_LABELS[key] ?? key
}
