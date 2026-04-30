// Stable placeholder returned for any formatter when its input doesn't
// parse to a real date. Beats "NaNd ago" / "Invalid Date" leaking into
// the UI when the backend returns a stale or empty timestamp.
const INVALID_DATE_PLACEHOLDER = "—"

function parseDate(dateStr: string): number | null {
  if (!dateStr) return null
  const t = new Date(dateStr).getTime()
  return Number.isFinite(t) ? t : null
}

/**
 * Format a date string as a human-readable relative time
 * (e.g. "5m ago", "2h ago", "yesterday", "2d ago").
 */
export function timeAgo(dateStr: string): string {
  const then = parseDate(dateStr)
  if (then === null) return INVALID_DATE_PLACEHOLDER
  const diff = Date.now() - then
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return "just now"
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days === 1) return "yesterday"
  return `${days}d ago`
}

/**
 * Format a duration in milliseconds as a compact string (e.g. "45s", "3m 12s").
 */
export function formatDuration(ms: number): string {
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const remainder = s % 60
  return remainder > 0 ? `${m}m ${remainder}s` : `${m}m`
}

/**
 * Format a timeout value in seconds as a human-readable string (e.g. "30 min", "2h").
 */
export function formatTimeout(seconds: number): string {
  if (seconds >= 3600) return `${Math.round(seconds / 3600)}h`
  return `${Math.round(seconds / 60)} min`
}

/** Formats a date string as "Mon D, YYYY" using the user's locale. */
export function formatDate(dateStr: string): string {
  const t = parseDate(dateStr)
  if (t === null) return INVALID_DATE_PLACEHOLDER
  return new Date(t).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  })
}

/** Formats a date string as "Mon D" without the year. */
export function formatShortDate(dateStr: string): string {
  const t = parseDate(dateStr)
  if (t === null) return INVALID_DATE_PLACEHOLDER
  return new Date(t).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
  })
}

/** Formats a date string as "Mon D, YYYY, H:MM AM/PM". */
export function formatDateTime(dateStr: string): string {
  const t = parseDate(dateStr)
  if (t === null) return INVALID_DATE_PLACEHOLDER
  return new Date(t).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
  })
}

/** Formats a date as relative time with second-level precision (e.g., "45s ago"). */
export function formatRelativeTime(dateStr: string): string {
  const date = parseDate(dateStr)
  if (date === null) return INVALID_DATE_PLACEHOLDER
  const diffMs = Date.now() - date

  const seconds = Math.floor(diffMs / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

/** Formats a comment timestamp: relative for recent, absolute date after 7 days. */
export function formatCommentTime(dateStr: string): string {
  const date = parseDate(dateStr)
  if (date === null) return INVALID_DATE_PLACEHOLDER
  const diffMin = Math.floor((Date.now() - date) / 60000)
  if (diffMin < 1) return "just now"
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHours = Math.floor(diffMin / 60)
  if (diffHours < 24) return `${diffHours}h ago`
  const diffDays = Math.floor(diffHours / 24)
  if (diffDays < 7) return `${diffDays}d ago`
  return new Date(date).toLocaleDateString()
}
