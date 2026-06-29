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
  // Clock skew or future-dated timestamps would otherwise emit "-12s ago",
  // which renders as nonsense in the UI. Clamp the diff so the formatter
  // collapses anything in the future to "0s ago" rather than negatives.
  const diffMs = Math.max(0, Date.now() - date)

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

// ===========================================================================
// Duration formatters
//
// Crewship's surfaces grew several incompatible duration formats — they
// disagree on rounding (floor vs round), granularity (seconds vs minutes),
// which units roll over (hours? days?), and whether a zero tail is dropped.
// Each variant below reproduces one historical inline/lib copy BYTE-FOR-BYTE.
// Do NOT collapse them into one without auditing every call site: that would
// silently change what the UI renders. Pick the export whose doc-comment
// matches the output you need; new code should prefer `formatDuration`.
// ===========================================================================

/**
 * Like {@link formatDuration} but surfaces sub-second values as "Nms".
 * Identical to formatDuration for durations >= 1s (e.g. "820ms", "45s",
 * "3m 12s", "2m"). Used by the chat assistant result card.
 */
export function formatDurationMillis(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  return formatDuration(ms)
}

/**
 * Rounded duration with sub-second "Nms" and an always-present seconds
 * field in the minute form: "820ms", "2s", "3m 12s" (never drops "0s").
 * No hours rollover. Used by the mission timeline.
 */
export function formatDurationRounded(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m ${s % 60}s`
}

/**
 * Floored duration, seconds + minutes only (no hours rollover):
 * "45s", "3m 12s". Used by mission board and (via {@link formatDurationSpan})
 * the agent canvas.
 */
export function formatDurationFloor(ms: number): string {
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m ${s % 60}s`
}

/**
 * Floored duration with an hours rollover: "45s", "3m 12s", "2h 30m".
 * Used by crew peer conversations and (via {@link formatDurationBetween})
 * the runs view and crew assignments.
 */
export function formatDurationClock(ms: number): string {
  const seconds = Math.floor(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${minutes % 60}m`
}

/**
 * One-decimal-second duration: "820ms", "1.2s", "1m 5s" (floors the
 * minute/second split). Used by the activity / routines surfaces and the
 * orchestration agent node.
 */
export function formatDurationDecimal(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
}

/**
 * Defensive one-decimal-second duration that never renders "60.0s" or
 * "1m 60s": "820ms", "1.2s", "1m 0s". Returns null for missing/negative
 * input (free-form journal payloads). Source of truth for the run-activity
 * timeline.
 */
export function formatDurationPrecise(ms: number | undefined): string | null {
  if (ms === undefined || !Number.isFinite(ms) || ms < 0) return null
  if (ms < 1000) return `${Math.round(ms)}ms`
  // One-decimal seconds, but if rounding reaches 60.0 spill into the minute
  // form so we never render "60.0s" or (below) "1m 60s".
  const oneDecimalSec = Math.round(ms / 100) / 10
  if (oneDecimalSec < 60) return `${oneDecimalSec.toFixed(1)}s`
  const totalSec = Math.round(ms / 1000)
  const mins = Math.floor(totalSec / 60)
  const secs = totalSec % 60
  return `${mins}m ${secs}s`
}

/**
 * Floored duration with hour rollover that drops a zero tail:
 * "45s", "3m", "2h 30m", "2h". Used by the backup lock-held banner.
 */
export function formatDurationHm(ms: number): string {
  const sec = Math.floor(ms / 1000)
  if (sec < 60) return `${sec}s`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m`
  const hr = Math.floor(min / 60)
  const remMin = min % 60
  return remMin > 0 ? `${hr}h ${remMin}m` : `${hr}h`
}

/**
 * Duration between two ISO timestamps, formatted via
 * {@link formatDurationClock} ("45s" / "3m 12s" / "2h 30m"). A null/omitted
 * `endIso` measures to "now" (still-running rows). Returns the em-dash
 * placeholder when `startIso` is missing, either timestamp is unparseable,
 * or the pair is inverted (end before start).
 */
export function formatDurationBetween(startIso: string | null, endIso?: string | null): string {
  if (!startIso) return INVALID_DATE_PLACEHOLDER
  const startMs = new Date(startIso).getTime()
  if (isNaN(startMs)) return INVALID_DATE_PLACEHOLDER
  const endMs = endIso ? new Date(endIso).getTime() : Date.now()
  if (isNaN(endMs)) return INVALID_DATE_PLACEHOLDER
  const diffMs = endMs - startMs
  if (diffMs < 0) return INVALID_DATE_PLACEHOLDER
  return formatDurationClock(diffMs)
}

/**
 * Duration between two ISO timestamps, formatted via
 * {@link formatDurationFloor} (seconds + minutes, no hours). Returns the
 * empty string when the pair is unparseable or inverted. Used by the agent
 * canvas run cards.
 */
export function formatDurationSpan(startIso: string, endIso: string): string {
  const ms = new Date(endIso).getTime() - new Date(startIso).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ""
  return formatDurationFloor(ms)
}

/**
 * Long-form duration between two ISO timestamps with hour & day rollover,
 * dropping the seconds tail past a minute: "45s", "3m", "2h 30m", "1d 6h".
 * A null/omitted `endIso` measures to "now". Used by the mission header.
 */
export function formatDurationLong(startIso: string, endIso?: string | null): string {
  const start = new Date(startIso).getTime()
  const end = endIso ? new Date(endIso).getTime() : Date.now()
  const diffMs = end - start
  const seconds = Math.floor(diffMs / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ${minutes % 60}m`
  const days = Math.floor(hours / 24)
  return `${days}d ${hours % 24}h`
}

/**
 * Minute-resolution duration between two ISO timestamps: "<1m", "5m",
 * "2h 30m", "2h". A null/omitted `endIso` measures to "now". Used by the
 * agent sessions list.
 */
export function formatDurationMinutes(startIso: string, endIso?: string | null): string {
  const startDate = new Date(startIso)
  const endDate = endIso ? new Date(endIso) : new Date()
  const diffMs = endDate.getTime() - startDate.getTime()
  const minutes = Math.floor(diffMs / 60000)
  if (minutes < 1) return "<1m"
  if (minutes >= 60) {
    const hours = Math.floor(minutes / 60)
    const remaining = minutes % 60
    return remaining > 0 ? `${hours}h ${remaining}m` : `${hours}h`
  }
  return `${minutes}m`
}

// ===========================================================================
// Relative-time formatters (additional variants folded in from sibling libs)
// ===========================================================================

/**
 * "5s ago" / "12m ago" / "2h ago" / "3d ago" — null/empty/invalid → em-dash.
 * Unlike {@link formatRelativeTime} this does NOT clamp future timestamps,
 * so callers must only pass past timestamps. Used by the runs views.
 */
export function formatRelativeShort(iso: string | null | undefined): string {
  if (!iso) return INVALID_DATE_PLACEHOLDER
  const ts = new Date(iso).getTime()
  if (isNaN(ts)) return INVALID_DATE_PLACEHOLDER
  const diffSec = Math.floor((Date.now() - ts) / 1000)
  if (diffSec < 60) return `${diffSec}s ago`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
  return `${Math.floor(diffSec / 86400)}d ago`
}

/**
 * Future-aware relative time: "just now", "Nm ago" / "in Nm", "Nh ago" /
 * "in Nh", "Nd ago" / "in Nd". Future timestamps (clock skew, scheduled
 * runs) get the "in …" prefix instead of being mislabelled as past.
 * `iso` may be undefined or invalid; callers don't have to pre-validate.
 * Used by the activity / routines surfaces.
 */
export function relTime(iso?: string): string {
  if (!iso) return INVALID_DATE_PLACEHOLDER
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return INVALID_DATE_PLACEHOLDER
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  const inFuture = diff < 0
  const abs = Math.abs(diff)
  const mins = Math.round(abs / 60_000)
  if (mins < 60) return inFuture ? `in ${mins}m` : `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return inFuture ? `in ${hrs}h` : `${hrs}h ago`
  const days = Math.round(hrs / 24)
  return inFuture ? `in ${days}d` : `${days}d ago`
}

/**
 * File mod-time: "Just now" / "12m ago" / "5h ago" / "Yesterday" /
 * "4d ago" / locale date past a week. Capitalised "Just now"/"Yesterday"
 * distinguish it from the other relative formatters. Used by the file browser.
 */
export function fmtTime(modTime: string): string {
  const mins = Math.floor((Date.now() - new Date(modTime).getTime()) / 60000)
  if (mins < 1) return "Just now"
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.floor(hrs / 24)
  if (days === 1) return "Yesterday"
  if (days < 7) return `${days}d ago`
  return new Date(modTime).toLocaleDateString()
}
