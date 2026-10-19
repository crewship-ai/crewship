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
  reason_code?: string
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

/** Capacity is a server classification, never inferred from attempt count. */
export function isWaitingForCapacity(start: PendingStart): boolean {
  return pendingStartStatus(start) === "pending" && start.reason_code === "routine_capacity"
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

const reasonDetails: Record<string, string> = {
  version_missing: "The accepted recipe version is no longer available.",
  routine_inactive: "The routine is no longer active.",
  dispatch_failed: "The start could not be dispatched. Inspect server logs using its ID.",
  capacity_expired: "Its time limit passed before execution capacity became available.",
  expired: "Its time limit passed before dispatch.",
}

export function pendingStartPresentation(start: PendingStart, now = Date.now()): PendingStartPresentation {
  const attempts = start.dispatch_attempts ?? 0
  const reason = start.reason_code ?? "unknown"
  if (reason === "recovery_unknown") return { label: "Needs attention", tone: "warn", detail: "The previous start could not be verified. Inspect the run history and server logs using this start ID before starting it again." }
  switch (pendingStartStatus(start)) {
    case "fired":
      return start.run_id
        ? { label: "Started", tone: "blue", detail: "A run was recorded for this start." }
        : { label: "Dispatching", tone: "blue", detail: "Handed to the routine engine. The run link appears once the run is recorded." }
    case "failed":
      return { label: "Did not run", tone: "danger", detail: start.last_error || reasonDetails[reason] || "The start could not be dispatched." }
    case "expired":
      return {
        label: "Expired",
        tone: "warn",
        detail: start.last_error || reasonDetails[reason] || "Its time limit passed before it could start.",
      }
    case "cancelled":
      return { label: "Removed", tone: "muted", detail: "Removed before it started." }
    case "pending": {
      if (reason === "paused") return { label: "Paused", tone: "muted", detail: "Queued starts are temporarily paused." }
      if (reason === "retry_backoff") return { label: "Waiting to retry", tone: "warn", detail: "Dispatch was interrupted before the run started; it will be retried." }
      if (reason === "recovered") return { label: "Recovered", tone: "blue", detail: "Recovered before execution started; waiting for dispatch." }
      if (reason === "routine_capacity") {
        const next = formatUntil(start.next_attempt_at, now)
        const left = formatUntil(start.expires_at, now)
        return {
          label: "Waiting for capacity",
          tone: "warn",
          detail:
            (attempts > 0 ? `Another run holds its slot; tried ${attempts} ${attempts === 1 ? "time" : "times"}.` : "Another run holds its slot.") +
            (next ? ` Next try in ${next} at the earliest.` : "") +
            (left ? ` Gives up in ${left} if no slot frees.` : ""),
        }
      }
      if (reason !== "unknown" && reason !== "scheduled") return { label: "Waiting", tone: "muted", detail: "The server reported a reason this client does not recognize." }
      if (attempts > 0) return { label: "Waiting", tone: "muted", detail: "The start is waiting; its reason is not available." }
      const due = Date.parse(start.fire_at)
      if (Number.isFinite(due) && due > now) return { label: "Planned", tone: "purple", detail: `Starts in ${formatUntil(start.fire_at, now)}.` }
      return { label: "Starting", tone: "blue", detail: `Due ${formatAgo(start.fire_at, now)}; the dispatcher picks it up within a few seconds.` }
    }
  }
}
