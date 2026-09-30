"use client"

import { apiFetch } from "@/lib/api-fetch"
import { holdsFixture, restoresFixture } from "./__fixtures__/backups"
import type { EnvironmentOutcome, InstanceHold, OffsiteCopyList, OffsiteFetch, RestoreChecks, RestoreRecord, RestoreReport, RestoreTarget } from "./backups-model"
import { INSTANCE_BACKUPS, isUnavailableStatus, listOf, readError, send, useResource, type SendResult } from "./use-backups-data"

/** GET /admin/instance/backups/restores — every restore, dry run and drill. */
export function useRestores() {
  return useResource<RestoreRecord[]>(`${INSTANCE_BACKUPS}/restores`, () => restoresFixture(new Date()), listOf)
}

/** GET /admin/instance/backups/drills */
export function useDrills() {
  return useResource<RestoreRecord[]>(`${INSTANCE_BACKUPS}/drills`, () => restoresFixture(new Date()).filter((r) => r.kind === "drill"), listOf)
}

/** GET /admin/instance/holds — what an instance restore left held. */
export function useHolds() {
  return useResource<InstanceHold[]>("/api/v1/admin/instance/holds", () => holdsFixture(new Date()), listOf)
}

export function resumeHold(key: string): Promise<SendResult<unknown>> {
  return send("/api/v1/admin/instance/holds/resume", "POST", { key })
}

/** GET /admin/instance/backups/copies?destination= — the bundles a destination holds. */
export function useOffsiteCopies(destinationId: string | null) {
  return useResource<OffsiteCopyList>(destinationId ? `${INSTANCE_BACKUPS}/copies?destination=${encodeURIComponent(destinationId)}` : null, () => ({
    destination_id: destinationId ?? "", destination_name: "r2",
    copies: [{ key: "instance/crewship-instance-20260929T010000Z.tar.zst", size: 538_181_632, modified: "2026-09-29T01:04:10Z", scope: "instance", workspace_id: null, local: false, local_path: null }],
  }))
}

/** POST /admin/instance/backups/copies/fetch — starts the download as a server job (202). */
export function fetchOffsiteCopy(destinationId: string, key: string): Promise<SendResult<OffsiteFetch>> {
  return send(`${INSTANCE_BACKUPS}/copies/fetch`, "POST", { destination_id: destinationId, key })
}

/** GET /admin/instance/backups/copies/fetch/{id} — follow a fetch. */
export function offsiteFetchStatus(id: string): Promise<SendResult<OffsiteFetch>> {
  return send(`${INSTANCE_BACKUPS}/copies/fetch/${encodeURIComponent(id)}`, "GET")
}

/**
 * Start a fetch and follow it until it ends. Resolves with the local bundle
 * path, or an error saying why. `signal` stops following (the server job
 * carries on and is still reachable by id).
 */
export async function fetchAndWait(destinationId: string, key: string, opts: { intervalMs?: number; signal?: AbortSignal } = {}): Promise<SendResult<string>> {
  const started = await fetchOffsiteCopy(destinationId, key)
  if (!started.ok) return started
  let job = started.data
  while (job.status === "running") {
    await new Promise((r) => setTimeout(r, opts.intervalMs ?? 1500))
    if (opts.signal?.aborted) return { ok: false, unavailable: false, error: "stopped following the fetch; it carries on on the server" }
    const next = await offsiteFetchStatus(job.id)
    if (!next.ok) return next
    job = next.data
  }
  if (job.status === "failed" || !job.path) return { ok: false, unavailable: false, error: job.error ?? "the fetch failed" }
  return { ok: true, data: job.path }
}

export interface RestoreRequest {
  path: string
  target: RestoreTarget
  identity?: string
  passphrase?: string
  as_workspace?: string
  as_crew?: string
  dry_run: boolean
}

/** POST …/restore/checks — space, format, runtime, unsafe settings, conflicts. */
export function restoreChecks(req: Omit<RestoreRequest, "dry_run">): Promise<SendResult<RestoreChecks>> {
  return send(`${INSTANCE_BACKUPS}/restore/checks`, "POST", req)
}

/** What the legacy restore answers: a report with named lists. */
interface LegacyRestoreReport {
  dry_run?: boolean
  skipped_crew_files?: string[]
  downgraded_credentials?: string[]
  dropped_columns?: string[]
  missing_rows?: string[]
  warnings?: string[]
  /** Complete container environments: restored, rebuilt or skipped. */
  environments?: EnvironmentOutcome[]
  [k: string]: unknown
}

export function legacyReport(r: LegacyRestoreReport): RestoreReport {
  const envs = r.environments ?? []
  const notRestored = envs.filter((e) => e.result !== "restored")
  const warnings = [
    ...(r.warnings ?? []),
    ...(r.skipped_crew_files ?? []).map((f) => `crew file skipped: ${f}`),
    ...(r.downgraded_credentials ?? []).map((c) => `credential downgraded: ${c}`),
    ...(r.missing_rows ?? []).map((m) => `missing rows: ${m}`),
    ...notRestored.map((e) => `environment ${e.crew} ${e.result === "rebuilt" ? "rebuilt instead of restored" : "skipped"}${e.reason ? `: ${e.reason}` : ""}`),
  ]
  const unsafe = [...new Set(envs.flatMap((e) => e.unsafe ?? []))]
  const notes = [
    ...(r.dropped_columns ?? []).map((c) => `column dropped: ${c}`),
    ...(envs.length ? ["Processes start fresh; what was only in memory is not restored"] : []),
    ...(unsafe.length ? [`Not carried over for safety: ${unsafe.join(" · ")}`] : []),
  ]
  return { result: warnings.length ? "partial" : "ok", summary: r.dry_run ? "Dry run finished" : "Restore finished", warnings, notes }
}

/**
 * A workspace or crew restore (and its dry run), through the per-workspace
 * POST /admin/backups/restore, which restores, forks and dry-runs. The
 * workspace is always named: an instance admin may restore one they are not a
 * member of. A whole-instance target is never sent — the server has no
 * instance restore route; that restore runs offline with `crewship recover`.
 */
export async function restore(req: RestoreRequest, workspaceId: string | null): Promise<SendResult<RestoreReport>> {
  if (req.target === "empty_server" || req.target === "isolated") {
    return { ok: false, unavailable: false, error: "A whole-instance restore runs from the command line (crewship recover)" }
  }
  if (!workspaceId) return { ok: false, unavailable: false, error: "Choose the workspace to restore in the bar above" }
  try {
    const res = await apiFetch(`/api/v1/admin/backups/restore?workspace_id=${encodeURIComponent(workspaceId)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        path: req.path, passphrase: req.passphrase, identity: req.identity, dry_run: req.dry_run,
        as_workspace: req.target === "new_workspace" ? req.as_workspace : undefined,
        as_crew: req.target === "crew" ? req.as_crew : undefined,
        replace: req.target === "replace" ? true : undefined,
      }),
    })
    if (!res.ok) return { ok: false, unavailable: isUnavailableStatus(res.status), error: await readError(res, `HTTP ${res.status}`) }
    return { ok: true, data: legacyReport({ dry_run: req.dry_run, ...((await res.json().catch(() => ({}))) as LegacyRestoreReport) }) }
  } catch (e) {
    return { ok: false, unavailable: false, error: e instanceof Error ? e.message : "Network error" }
  }
}
