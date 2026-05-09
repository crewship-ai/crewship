// Tiny time formatters shared by /activity surfaces.
//
// Both runs-view.tsx and run-timeline-rail.tsx had byte-identical
// copies of these. The next surface to land (any kind of activity
// digest, scheduled-run preview, etc.) inevitably wants them too —
// so they live here.

/** Returns "just now", "Nm ago / in Nm", "Nh ago / in Nh", or
 * "Nd ago / in Nd". Future timestamps (clock skew, scheduled runs)
 * get the "in …" prefix instead of getting mislabelled as past
 * with the same magnitude. `iso` may be undefined or invalid;
 * callers don't have to pre-validate. */
export function relTime(iso?: string): string {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
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

/** Compact duration label: "Nms" for sub-second, "N.Ns" for sub-min,
 * "Nm Ns" otherwise. Caller passes raw milliseconds. */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const mins = Math.floor(ms / 60_000)
  const secs = Math.floor((ms % 60_000) / 1000)
  return `${mins}m ${secs}s`
}
