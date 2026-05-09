// Tiny time formatters shared by /activity surfaces.
//
// Both runs-view.tsx and run-timeline-rail.tsx had byte-identical
// copies of these. The next surface to land (any kind of activity
// digest, scheduled-run preview, etc.) inevitably wants them too —
// so they live here.

/** Returns "just now", "Nm ago", "Nh ago", or "Nd ago". `iso` may be
 * undefined or invalid; callers don't have to pre-validate. */
export function relTime(iso?: string): string {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  const mins = Math.round(Math.abs(diff) / 60_000)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.round(hrs / 24)}d ago`
}

/** Compact duration label: "Nms" for sub-second, "N.Ns" for sub-min,
 * "Nm Ns" otherwise. Caller passes raw milliseconds. */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const mins = Math.floor(ms / 60_000)
  const secs = Math.floor((ms % 60_000) / 1000)
  return `${mins}m ${secs}s`
}
