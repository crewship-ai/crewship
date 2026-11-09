import { dashboardName } from "./artifact-dashboard"

/**
 * What the server recorded about a collection's producer runs: the newest
 * run, its last attempt and the last success. `reason` is present only for
 * the owner and editors; a reader learns that a refresh failed, not why.
 * `partial` marks a failed run during which the collection's data changed:
 * batches apply as they are written, so some values may be from that run.
 */
export interface DashboardRunSummary {
  collection: string
  state: "running" | "succeeded" | "failed"
  startedAt: Date
  finishedAt: Date | null
  lastAttemptAt: Date
  lastSuccessAt: Date | null
  reason: string | null
  startedBy: string
  partial: boolean
}

const STATES = new Set(["running", "succeeded", "failed"])
const MAX_REASON = 500
const record = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value)
const date = (value: unknown): Date | null | undefined => {
  if (value === undefined) return null
  if (typeof value !== "string") return undefined
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? undefined : parsed
}

/**
 * Parses GET /artifact/data-runs for the shown code version. The whole
 * response is rejected when any entry is malformed or names another version,
 * so a broken read never shows a partial state; collections the version does
 * not declare are dropped.
 */
export function parseDashboardRuns(raw: unknown, codeVersion: number, collections: readonly string[]): DashboardRunSummary[] | null {
  if (!record(raw) || raw.code_version !== codeVersion || !Array.isArray(raw.runs) || raw.runs.length > 32) return null
  const out: DashboardRunSummary[] = []
  for (const entry of raw.runs) {
    if (!record(entry) || !record(entry.latest) || !dashboardName(entry.collection) || entry.latest.collection !== entry.collection) return null
    const latest = entry.latest
    const startedAt = date(latest.started_at), finishedAt = date(latest.finished_at), lastAttemptAt = date(entry.last_attempt_at), lastSuccessAt = date(entry.last_success_at)
    if (typeof latest.state !== "string" || !STATES.has(latest.state) || !startedAt || finishedAt === undefined || !lastAttemptAt || lastSuccessAt === undefined) return null
    if ((latest.state === "running") !== (finishedAt === null)) return null
    const reason = latest.failure_reason
    if (reason !== undefined && (typeof reason !== "string" || reason.length > MAX_REASON || latest.state !== "failed")) return null
    if (typeof latest.started_by !== "string" || latest.started_by.length > 64) return null
    const partial = latest.partial ?? false
    if (typeof partial !== "boolean" || (partial && latest.state !== "failed")) return null
    if (!collections.includes(entry.collection)) continue
    out.push({ collection: entry.collection, state: latest.state as DashboardRunSummary["state"], startedAt, finishedAt, lastAttemptAt, lastSuccessAt, reason: reason || null, startedBy: latest.started_by, partial })
  }
  return out
}
