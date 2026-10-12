import type { StatusTone } from "@/lib/format-status"
import { formatAgo, formatUntil } from "@/lib/routine-run-presentation"

// An accepted deferred start (`pending_runs`) as GET /pipelines/pending and
// /pipeline-pending/{id} return it — the receipt behind a 202 `pending_id`.
// Fields after `fire_at` are absent on a server that predates receipt
// inspection; such a row is a plain pending start, which is all that server
// lists.
export interface PendingStart {
  id: string
  pipeline_slug: string
  fire_at: string
  inputs?: Record<string, unknown>
  pinned_version?: number | null
  debounce_key?: string
  priority?: number
  status?: PendingStartStatus | string
  /** Empty while a fired start's run is still executing; not a failure. */
  run_id?: string
  dispatch_attempts?: number
  last_error?: string
  expires_at?: string | null
  /** When the next dispatch attempt becomes eligible — not a promised start. */
  next_attempt_at?: string | null
  can_cancel?: boolean
}

export type PendingStartStatus = "pending" | "fired" | "failed" | "expired" | "cancelled"

export function pendingStartStatus(start: PendingStart): PendingStartStatus {
  const s = start.status || "pending"
  return s === "fired" || s === "failed" || s === "expired" || s === "cancelled" ? s : "pending"
}

/** Waiting because capacity refused it at least once, not because its time
 * has not come. */
export function isWaitingForCapacity(start: PendingStart): boolean {
  return pendingStartStatus(start) === "pending" && (start.dispatch_attempts ?? 0) > 0
}

/** Accepted, but it will not run: the reader has to learn why. */
export function didNotRun(start: PendingStart): boolean {
  const s = pendingStartStatus(start)
  return s === "failed" || s === "expired"
}

export interface PendingStartPresentation {
  label: string
  tone: StatusTone
  /** One sentence in the reader's words; never an ETA or a queue position. */
  detail: string
}

export function pendingStartPresentation(start: PendingStart, now = Date.now()): PendingStartPresentation {
  const attempts = start.dispatch_attempts ?? 0
  switch (pendingStartStatus(start)) {
    case "fired":
      return start.run_id
        ? { label: "Started", tone: "blue", detail: "A run was recorded for this start." }
        : { label: "Started", tone: "blue", detail: "Handed to the routine engine. The run link appears once the run is recorded." }
    case "failed":
      return { label: "Did not run", tone: "danger", detail: start.last_error || "The start could not be dispatched." }
    case "expired":
      return {
        label: "Expired",
        tone: "warn",
        detail: start.last_error || "Its time limit passed before it could start.",
      }
    case "cancelled":
      return { label: "Removed", tone: "muted", detail: "Removed before it started." }
    case "pending": {
      if (attempts > 0) {
        const next = formatUntil(start.next_attempt_at, now)
        const left = formatUntil(start.expires_at, now)
        return {
          label: "Waiting for capacity",
          tone: "warn",
          detail:
            `Another run holds its slot; tried ${attempts} ${attempts === 1 ? "time" : "times"}.` +
            (next ? ` Next try in ${next} at the earliest.` : "") +
            (left ? ` Gives up in ${left} if no slot frees.` : ""),
        }
      }
      const due = Date.parse(start.fire_at)
      if (Number.isFinite(due) && due > now) return { label: "Planned", tone: "purple", detail: `Starts in ${formatUntil(start.fire_at, now)}.` }
      return { label: "Starting", tone: "blue", detail: `Due ${formatAgo(start.fire_at, now)}; the dispatcher picks it up within a few seconds.` }
    }
  }
}
