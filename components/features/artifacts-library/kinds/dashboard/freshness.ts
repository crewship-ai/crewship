import type { DashboardCollection, DashboardDataset, DashboardDatasets } from "@/lib/pages/artifact-dashboard"
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
 * the stated mode and maximum age. The newest write is the last successful
 * write the server confirmed; producer runs that wrote nothing leave no trace.
 */
export function dashboardFreshness(collections: readonly DashboardCollection[], datasets: DashboardDatasets = { status: "absent" }, now: Date = new Date()): DashboardFreshness {
  const rows = collections.map(collection => {
    const updatedAt = newest(collection.documents.map(document => new Date(document.updated_at)))
    // Own keys only: a collection may be named like an Object.prototype member ("constructor").
    const dataset = datasets.status === "ok" && Object.hasOwn(datasets.datasets, collection.collection) ? datasets.datasets[collection.collection] : null
    const stale = Boolean(dataset && dataset.mode !== "fixed" && dataset.maxAgeSeconds && updatedAt && now.getTime() - updatedAt.getTime() > dataset.maxAgeSeconds * 1000)
    return {
      name: collection.collection,
      documents: collection.documents.length,
      updatedAt,
      sources: unique(collection.documents.map(document => document.source_label)),
      dataset, stale,
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

/** A stated maximum age, with no rounding that changes its threshold. */
export function maxAgeLabel(seconds: number): string {
  const units = [[86400, "d"], [3600, "h"], [60, "min"], [1, "s"]] as const
  return units.flatMap(([size, label]) => {
    const amount = Math.floor(seconds / size)
    seconds %= size
    return amount ? [`${amount} ${label}`] : []
  }).join(" ")
}

export const DATASET_MODE_LABEL = { fixed: "Fixed · declared", planned: "Planned refresh · declared", live: "Live · declared" } as const

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
 * sample; Fixed when every dataset is declared fixed; otherwise the chip
 * reports the state of the read, not the source. There is no producer run state, so
 * Refreshing never appears, and without a stated age nothing is Stale.
 */
export function dashboardFrameStatus(input: { pending: boolean; error: boolean; limitExceeded: boolean; freshness: DashboardFreshness | null; viewingOld: boolean; shown: number; now?: Date }): FrameStatus | null {
  if (input.limitExceeded) return { key: "error", label: "Too much data", detail: "The data is larger than the preview can show. Nothing was changed." }
  if (input.error) return { key: "error", label: "Data unavailable", detail: "The data could not be read. Reload to check access. Nothing was changed." }
  if (input.pending || !input.freshness) return null
  const versionNote = input.viewingOld ? ` You are viewing v${input.shown}.` : ""
  if (input.freshness.details === "invalid") {
    return { key: "error", label: "Invalid dataset details", detail: `Dataset details in datasets.json are invalid. Saved values can still be shown; their sample, fixed or stale state is unknown.${versionNote}` }
  }
  const rows = input.freshness.collections
  const name = (row: CollectionFreshness) => row.dataset?.title ?? row.name
  const stale = rows.filter(row => row.stale)
  if (stale.length) return { key: "stale", ...(rows.some(row => row.dataset?.sample) ? { label: "Stale · sample data" } : {}), detail: `${stale.map(row => `${name(row)} was last written ${freshnessTime(row.updatedAt!, input.now)}, older than its stated maximum age of ${maxAgeLabel(row.dataset!.maxAgeSeconds!)}`).join("; ")}. Values keep their last written state.${rows.some(row => row.dataset?.sample) ? " Sources identifies the sample data." : ""}${versionNote}` }
  const sample = rows.filter(row => row.dataset?.sample)
  if (sample.length) return { key: "sample", detail: `${sample.length === rows.length ? "The author marked this data" : `The author marked ${sample.map(name).join(", ")}`} as sample data, not measurements.${versionNote}` }
  if (rows.length && rows.every(row => row.dataset?.mode === "fixed")) return { key: "fixed", detail: `The author declared a fixed snapshot with no automatic refresh.${input.freshness.updatedAt ? ` Last written ${freshnessTime(input.freshness.updatedAt, input.now)}.` : ""}${versionNote}` }
  if (!input.freshness.updatedAt) return { key: "live", label: "No data yet", detail: `Connected. No values have been written yet.${versionNote}` }
  return { key: "live", label: "Data connected", detail: `Reads the latest saved values every few seconds. Last write ${freshnessTime(input.freshness.updatedAt, input.now)}.${versionNote}` }
}
