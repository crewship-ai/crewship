import type { DashboardCollection, DashboardDataset, DashboardDatasets } from "@/lib/pages/artifact-dashboard"
import type { DashboardRunSummary } from "@/lib/pages/artifact-dashboard-runs"
import type { FrameStatus } from "../../frame"

export interface CollectionFreshness {
  name: string
  documents: number
  /** The newest `updated_at` the server sent for this collection; null when it holds nothing. */
  updatedAt: Date | null
  /** Who wrote the values, as the server named them (`source_label`). */
  sources: string[]
  /** What the author states about this collection in datasets.json; null when nothing is stated. */
  dataset: DashboardDataset | null
  /** Planned or live data older than its stated maximum age. Never true without a stated age. */
  stale: boolean
  /** The server's producer run record for this collection; null when no producer recorded a run. */
  run: DashboardRunSummary | null
  /** The last successful producer run; with no run record, Stale falls back to the last write. */
  lastSuccessAt: Date | null
  /** A run still marked running after the server's configured threshold (run.stalledAfterMs): the producer most likely died. Derived here, never stored. */
  stalled: boolean
}


export interface DashboardFreshness {
  updatedAt: Date | null
  sources: string[]
  documents: number
  collections: CollectionFreshness[]
  /** Whether datasets.json is absent, valid, or present but rejected (shown, never silently dropped). */
  details: DashboardDatasets["status"]
}

const newest = (dates: (Date | null)[]): Date | null => dates.reduce<Date | null>((top, date) => date && (!top || date > top) ? date : top, null)
const unique = (labels: string[]) => [...new Set(labels)]

/**
 * Freshness from the documents the data read returned and the author's
 * datasets.json; nothing is inferred beyond their `updated_at`, source and
 * the stated mode and maximum age, and the producer run record when the
 * server has one. With a run record, Stale is measured from the last
 * successful run (a stray write does not make data fresh); without one,
 * from the newest write. The server binds a success to the data generation
 * its producer wrote (or claims unchanged, checked only against writes made
 * during the run) and refuses it while another run overlaps. Batches apply as they are written; nothing is staged, so
 * values may change while a refresh runs and a failed one may leave some.
 */
export function dashboardFreshness(collections: readonly DashboardCollection[], datasets: DashboardDatasets = { status: "absent" }, now: Date = new Date(), runs: readonly DashboardRunSummary[] | null = null): DashboardFreshness {
  const rows = collections.map(collection => {
    const updatedAt = newest(collection.documents.map(document => new Date(document.updated_at)))
    // Own keys only: a collection may be named like an Object.prototype member ("constructor").
    const dataset = datasets.status === "ok" && Object.hasOwn(datasets.datasets, collection.collection) ? datasets.datasets[collection.collection] : null
    const run = runs?.find(entry => entry.collection === collection.collection) ?? null
    const lastSuccessAt = run?.lastSuccessAt ?? null
    const reference = run ? lastSuccessAt : updatedAt
    const stale = Boolean(dataset && dataset.mode !== "fixed" && dataset.maxAgeSeconds && reference && now.getTime() - reference.getTime() > dataset.maxAgeSeconds * 1000)
    const stalled = run?.state === "running" && now.getTime() - run.startedAt.getTime() > run.stalledAfterMs
    return {
      name: collection.collection,
      documents: collection.documents.length,
      updatedAt,
      sources: unique(collection.documents.map(document => document.source_label)),
      dataset, stale, run, lastSuccessAt, stalled,
    }
  })
  return {
    updatedAt: newest(rows.map(row => row.updatedAt)),
    sources: unique(rows.flatMap(row => row.sources)),
    documents: rows.reduce((sum, row) => sum + row.documents, 0),
    collections: rows,
    details: datasets.status,
  }
}

/** "45 min", "6 h", "2 d": a stated maximum age, rounded down. */
export function maxAgeLabel(seconds: number): string {
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} h`
  return `${Math.floor(seconds / 86400)} d`
}

export const DATASET_MODE_LABEL = { fixed: "Fixed · no automatic refresh", planned: "Planned refresh", live: "Live" } as const

/** "10:32" today, "3 Nov" before. */
export function freshnessTime(date: Date, now: Date = new Date()): string {
  return date.toDateString() === now.toDateString()
    ? date.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
    : date.toLocaleDateString("en-GB", { day: "numeric", month: "short" })
}

/** The output slot's line: "Updated 10:32 · HubSpot". */
export function freshnessLabel(freshness: DashboardFreshness, now?: Date): string {
  if (!freshness.updatedAt) return "No data yet"
  const source = freshness.sources.length === 1 ? ` · ${freshness.sources[0]}` : freshness.sources.length > 1 ? ` · ${freshness.sources.length} sources` : ""
  return `Updated ${freshnessTime(freshness.updatedAt, now)}${source}`
}

/**
 * The chip for a Dashboard from its data read and datasets.json. An error
 * when the read fails; Stale when planned or live data is older than its
 * stated maximum age; Sample data when the author marked a dataset as
 * sample; Fixed when every dataset is a fixed snapshot; otherwise Live
 * while the read answers (it polls). Producer runs come first after a read
 * error: a failed last attempt (with the reason only when the server gave
 * one), a stalled run, then Refreshing. Without a stated age nothing is Stale.
 */
export function dashboardFrameStatus(input: { pending: boolean; error: boolean; limitExceeded: boolean; freshness: DashboardFreshness | null; viewingOld: boolean; shown: number; now?: Date }): FrameStatus | null {
  if (input.limitExceeded) return { key: "error", label: "Too much data", detail: "The data is larger than the preview can show. Nothing was changed." }
  if (input.error) return { key: "error", label: "Data unavailable", detail: "The data could not be read. Reload to check access. Nothing was changed." }
  if (input.pending || !input.freshness) return null
  // An invalid datasets.json must not leave the chip looking authoritative: every state says so.
  const versionNote = `${input.freshness.details === "invalid" ? " Dataset details in datasets.json are invalid, so sample, fixed and stale states cannot be shown." : ""}${input.viewingOld ? ` You are viewing v${input.shown}.` : ""}`
  const rows = input.freshness.collections
  const name = (row: CollectionFreshness) => row.dataset?.title ?? row.name
  const at = (date: Date) => freshnessTime(date, input.now)
  const lastSuccess = (row: CollectionFreshness) => row.lastSuccessAt ? `the last successful refresh was ${at(row.lastSuccessAt)}` : "no refresh has succeeded yet"
  const failed = rows.filter(row => row.run?.state === "failed")
  if (failed.length) return { key: "stale", label: "Refresh failed", detail: `${failed.map(row => `${name(row)}: the refresh at ${at(row.run!.lastAttemptAt)} failed${row.run!.reason ? `: ${row.run!.reason}` : ""}. ${row.run!.partial ? "Data changed while this refresh ran, so some values may be from it" : "It changed no values"}; ${lastSuccess(row)}.`).join(" ")}${versionNote}` }
  const stalled = rows.filter(row => row.stalled)
  if (stalled.length) return { key: "stale", label: "Refresh stalled", detail: `${stalled.map(row => `${name(row)} has been refreshing since ${at(row.run!.startedAt)} without finishing. Values may be partly updated; ${lastSuccess(row)}.`).join(" ")}${versionNote}` }
  const running = rows.filter(row => row.run?.state === "running")
  if (running.length) return { key: "refreshing", detail: `${running.map(row => `${name(row)} is refreshing since ${at(row.run!.startedAt)}. Values update as the refresh writes them; ${lastSuccess(row)}.`).join(" ")}${versionNote}` }
  const stale = rows.filter(row => row.stale)
  if (stale.length) return { key: "stale", detail: `${stale.map(row => row.run ? `${name(row)} last refreshed successfully ${row.lastSuccessAt ? at(row.lastSuccessAt) : "never"}, older than its stated maximum age of ${maxAgeLabel(row.dataset!.maxAgeSeconds!)}` : `${name(row)} was last written ${at(row.updatedAt!)}, older than its stated maximum age of ${maxAgeLabel(row.dataset!.maxAgeSeconds!)}`).join("; ")}. Values keep their last written state.${versionNote}` }
  const sample = rows.filter(row => row.dataset?.sample)
  if (sample.length) return { key: "sample", detail: `${sample.length === rows.length ? "The author marked this data" : `The author marked ${sample.map(name).join(", ")}`} as sample data, not measurements.${versionNote}` }
  if (rows.length && rows.every(row => row.dataset?.mode === "fixed")) return { key: "fixed", detail: `A fixed snapshot with no automatic refresh.${input.freshness.updatedAt ? ` Last written ${freshnessTime(input.freshness.updatedAt, input.now)}.` : ""}${versionNote}` }
  if (!input.freshness.updatedAt) return { key: "live", detail: `Connected. No values have been written yet.${versionNote}` }
  const success = rows.reduce<Date | null>((top, row) => row.lastSuccessAt && (!top || row.lastSuccessAt > top) ? row.lastSuccessAt : top, null)
  return { key: "live", detail: `Reads the latest values every few seconds. Last change ${at(input.freshness.updatedAt)}.${success ? ` Last successful refresh ${at(success)}.` : ""}${versionNote}` }
}
