// Shared formatting helpers for the Runs view (and the future Runs tab
// inside /journal). Extracted from app/(dashboard)/runs/page.tsx so the
// same renderers can be reused by the journal-tab variant — see
// components/features/journal/runs-view.tsx.

/**
 * Map a backend run status string to the canonical token used by
 * `<StatusBadge>` and `STATUS_BADGE_CLASSES`. Backend uses RUNNING /
 * TIMEOUT etc.; the badge palette uses IN_PROGRESS / FAILED.
 */
export function toCanonicalStatus(status: string): string {
  switch (status) {
    case "RUNNING":
      return "IN_PROGRESS"
    case "TIMEOUT":
      return "FAILED"
    default:
      return status
  }
}

/** Human-friendly label for a run status. */
export function statusLabel(status: string): string {
  switch (status) {
    case "PENDING":
      return "Pending"
    case "RUNNING":
      return "Running"
    case "COMPLETED":
      return "Completed"
    case "FAILED":
      return "Failed"
    case "CANCELLED":
      return "Cancelled"
    case "TIMEOUT":
      return "Timeout"
    default:
      return status
  }
}

// Duration / relative-time formatters that used to live here now have a
// single home in lib/time.ts: `formatDuration(start, end)` is
// `formatDurationBetween`, and `formatRelativeShort` moved verbatim.
