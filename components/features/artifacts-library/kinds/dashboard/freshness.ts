import type { DashboardCollection } from "@/lib/pages/artifact-dashboard"
import type { FrameStatus } from "../../frame"

export interface CollectionFreshness {
  name: string
  documents: number
  /** The newest `updated_at` the server sent for this collection; null when it holds nothing. */
  updatedAt: Date | null
  /** Who wrote the values, as the server named them (`source_label`). */
  sources: string[]
}

export interface DashboardFreshness {
  updatedAt: Date | null
  sources: string[]
  documents: number
  collections: CollectionFreshness[]
}

const newest = (dates: (Date | null)[]): Date | null => dates.reduce<Date | null>((top, date) => date && (!top || date > top) ? date : top, null)
const unique = (labels: string[]) => [...new Set(labels)]

/** Freshness from the documents the data read returned; nothing is inferred beyond their `updated_at` and source. */
export function dashboardFreshness(collections: readonly DashboardCollection[]): DashboardFreshness {
  const rows = collections.map(collection => ({
    name: collection.collection,
    documents: collection.documents.length,
    updatedAt: newest(collection.documents.map(document => new Date(document.updated_at))),
    sources: unique(collection.documents.map(document => document.source_label)),
  }))
  return {
    updatedAt: newest(rows.map(row => row.updatedAt)),
    sources: unique(rows.flatMap(row => row.sources)),
    documents: rows.reduce((sum, row) => sum + row.documents, 0),
    collections: rows,
  }
}

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
 * The chip for a Dashboard from its data read. Live while the read answers
 * (it polls), an error when it does not. Dashboards declare no refresh
 * window or sample data today, so Stale, Refreshing and Sample data never
 * appear here.
 */
export function dashboardFrameStatus(input: { pending: boolean; error: boolean; limitExceeded: boolean; freshness: DashboardFreshness | null; viewingOld: boolean; shown: number; now?: Date }): FrameStatus | null {
  if (input.limitExceeded) return { key: "error", label: "Too much data", detail: "The data is larger than the preview can show. Nothing was changed." }
  if (input.error) return { key: "error", label: "Data unavailable", detail: "The data could not be read. Reload to check access. Nothing was changed." }
  if (input.pending || !input.freshness) return null
  const versionNote = input.viewingOld ? ` You are viewing v${input.shown}.` : ""
  if (!input.freshness.updatedAt) return { key: "live", detail: `Connected. No values have been written yet.${versionNote}` }
  return { key: "live", detail: `Reads the latest values every few seconds. Last change ${freshnessTime(input.freshness.updatedAt, input.now)}.${versionNote}` }
}
