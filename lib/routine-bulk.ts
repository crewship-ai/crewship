// Bulk actions of the Routines panel (PNL-03, routines.md R01).
//
//   Turn off  POST …/pipelines/{slug}/disable, one routine at a time (the
//             server has no batch; Owner and Admin).
//   Run now   POST …/pipelines/{slug}/run with no inputs, only for routines
//             that need no answer from a person: a required input without
//             a default is skipped and named. The list row carries no
//             definition, so each one is read first.
//
// Each call stands alone: one refusal does not stop the rest, and the result
// names what happened to every routine.

import { routineInputSpecs } from "@/lib/routine-inputs"

type Fetcher = (url: string, init?: RequestInit) => Promise<Response>

export interface BulkRunResult {
  started: string[]
  skipped: { slug: string; reason: string }[]
  failed: { slug: string; reason: string }[]
}

export interface BulkTurnOffResult {
  done: string[]
  failed: { slug: string; reason: string }[]
}

/** The names of the inputs a start must be given: required, no default. */
export function requiredInputsWithoutDefault(definition: Record<string, unknown> | null | undefined): string[] {
  if (!definition) return []
  return routineInputSpecs(definition)
    .filter((s) => s.required && (s.default === undefined || s.default === null || s.default === ""))
    .map((s) => s.name)
}

const pipelinesBase = (workspaceId: string) => `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines`

async function reason(res: Response): Promise<string> {
  try {
    const body = await res.json()
    if (body && typeof body === "object") {
      const b = body as { error?: unknown; detail?: unknown; title?: unknown }
      for (const v of [b.error, b.detail, b.title]) if (typeof v === "string" && v) return v
    }
  } catch {
    /* not JSON */
  }
  return `refused (${res.status})`
}

const newKey = () =>
  typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `bulk-${Date.now()}-${Math.random().toString(36).slice(2)}`

export async function bulkRunNow(workspaceId: string, slugs: string[], fetcher: Fetcher): Promise<BulkRunResult> {
  const base = pipelinesBase(workspaceId)
  const out: BulkRunResult = { started: [], skipped: [], failed: [] }
  for (const slug of slugs) {
    const path = `${base}/${encodeURIComponent(slug)}`
    try {
      const detail = await fetcher(path)
      if (!detail.ok) {
        out.failed.push({ slug, reason: await reason(detail) })
        continue
      }
      const routine = (await detail.json()) as { definition?: Record<string, unknown> | null }
      const needs = requiredInputsWithoutDefault(routine.definition ?? null)
      if (needs.length) {
        out.skipped.push({ slug, reason: `needs ${needs.join(", ")}` })
        continue
      }
      const res = await fetcher(`${path}/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Prefer: "respond-async", "Idempotency-Key": newKey() },
        body: JSON.stringify({ inputs: {} }),
      })
      if (!res.ok) out.failed.push({ slug, reason: await reason(res) })
      else out.started.push(slug)
    } catch (e) {
      out.failed.push({ slug, reason: e instanceof Error ? e.message : String(e) })
    }
  }
  return out
}

export async function bulkTurnOff(workspaceId: string, slugs: string[], fetcher: Fetcher): Promise<BulkTurnOffResult> {
  const base = pipelinesBase(workspaceId)
  const out: BulkTurnOffResult = { done: [], failed: [] }
  for (const slug of slugs) {
    try {
      const res = await fetcher(`${base}/${encodeURIComponent(slug)}/disable`, { method: "POST", headers: { "Content-Type": "application/json" } })
      if (res.ok) out.done.push(slug)
      else out.failed.push({ slug, reason: await reason(res) })
    } catch (e) {
      out.failed.push({ slug, reason: e instanceof Error ? e.message : String(e) })
    }
  }
  return out
}

/** One line for the toast: what started, what was skipped and why, what was refused. */
export function bulkSummary(r: BulkRunResult): string {
  const parts = [`Started ${r.started.length} ${r.started.length === 1 ? "run" : "runs"}`]
  if (r.skipped.length) parts.push(`skipped ${r.skipped.length} that ${r.skipped.length === 1 ? "needs" : "need"} an answer`)
  if (r.failed.length) parts.push(`${r.failed.length} refused`)
  return parts.join(" · ")
}
