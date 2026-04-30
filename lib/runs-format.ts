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

/**
 * Format a duration between two ISO timestamps as a compact "Xs / Xm Ys
 * / Xh Ym" string. When end is null/missing we measure to "now".
 *
 * Returns the same em-dash placeholder as the other helpers when
 * either timestamp is unparseable or the pair is inverted (end before
 * start). Without this guard a malformed row renders as "NaNs" or a
 * negative duration.
 */
export function formatDuration(start: string | null, end: string | null): string {
  if (!start) return "—"
  const startMs = new Date(start).getTime()
  if (isNaN(startMs)) return "—"
  const endMs = end ? new Date(end).getTime() : Date.now()
  if (isNaN(endMs)) return "—"
  const seconds = Math.floor((endMs - startMs) / 1000)
  if (seconds < 0) return "—"
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
}

/** "5s ago" / "12m ago" / "2h ago" / "3d ago" — null/empty → em-dash. */
export function formatRelativeShort(iso: string | null | undefined): string {
  if (!iso) return "—"
  const ts = new Date(iso).getTime()
  if (isNaN(ts)) return "—"
  const diffSec = Math.floor((Date.now() - ts) / 1000)
  if (diffSec < 60) return `${diffSec}s ago`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
  return `${Math.floor(diffSec / 86400)}d ago`
}
