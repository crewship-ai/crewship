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

/**
 * Where the wizard restores to. "crew_in_place" is a crew archive restored as
 * itself, without --as-crew (the server refuses --replace for a crew archive);
 * the checks know it as "crew".
 */
export type WizardTarget = RestoreTarget | "crew_in_place"

export interface RestoreRequest {
  path: string
  target: WizardTarget
  identity?: string
  passphrase?: string
  as_workspace?: string
  as_crew?: string
  dry_run: boolean
}

/** POST …/restore/checks — space, format, runtime, unsafe settings, conflicts. */
export function restoreChecks(req: Omit<RestoreRequest, "dry_run">): Promise<SendResult<RestoreChecks>> {
  // In place is the backup under its own names: the server's in_place target,
  // judged by the same rule the restore applies to a restore with no flags.
  return send(`${INSTANCE_BACKUPS}/restore/checks`, "POST", { ...req, target: req.target === "crew_in_place" ? "in_place" : req.target, as_crew: req.target === "crew" ? req.as_crew : undefined })
}

interface RowCountMismatch { table: string; recorded: number; actual: number }

/**
 * What POST /admin/backups/restore answers (backupRestoreResponse in
 * internal/api/backup.go). `result` is the server's own verdict
 * (backup.ClassifyRestore); the rest names what did not land. A failed restore
 * answers with an error status instead, handled by the caller.
 */
interface LegacyRestoreReport {
  dry_run?: boolean
  result?: "ok" | "partial" | "failed"
  attachments_missing?: number
  attachments_conflicts?: number
  dropped_crew_filesystems?: string[] | null
  rows_inserted_shortfalls?: RowCountMismatch[] | null
  payload_row_count_mismatches?: RowCountMismatch[] | null
  incomplete?: { kind: string; detail: string; count: number; workspace?: string }[] | null
  security_level_clamps?: { credential_id: string; name?: string; from: string; to: number }[] | null
  dropped_columns?: { table: string; column: string; rows: number }[] | null
  /** The crews' files were not copied into containers (a restore under a new name). */
  docker_phase_skipped?: boolean
  warnings?: string[]
  /** Complete container environments: restored, rebuilt or skipped. */
  environments?: EnvironmentOutcome[] | null
  [k: string]: unknown
}

export function legacyReport(r: LegacyRestoreReport): RestoreReport {
  const envs = r.environments ?? []
  const notRestored = envs.filter((e) => e.result !== "restored")
  const crews = r.dropped_crew_filesystems ?? []
  const warnings = [
    ...(r.warnings ?? []),
    ...(r.attachments_missing ? [`${r.attachments_missing} attachment file${r.attachments_missing === 1 ? "" : "s"} missing`] : []),
    ...(r.attachments_conflicts ? [`${r.attachments_conflicts} attachment file${r.attachments_conflicts === 1 ? "" : "s"} conflict with existing ones`] : []),
    ...(crews.length ? [`crew files not restored: ${crews.join(", ")}`] : []),
    ...(r.rows_inserted_shortfalls ?? []).map((m) => `${m.table}: ${m.actual} of ${m.recorded} rows landed`),
    ...(r.payload_row_count_mismatches ?? []).map((m) => `${m.table}: archive holds ${m.actual} rows, its manifest says ${m.recorded}`),
    ...(r.incomplete ?? []).map((i) => i.detail || `${i.count} × ${i.kind}`),
    ...(r.security_level_clamps ?? []).map((c) => `credential ${c.name || c.credential_id} clamped to security level ${c.to}`),
    ...notRestored.map((e) => `environment ${e.crew} ${e.result === "rebuilt" ? "rebuilt instead of restored" : "skipped"}${e.reason ? `: ${e.reason}` : ""}`),
  ]
  const unsafe = [...new Set(envs.flatMap((e) => e.unsafe ?? []))]
  const notes = [
    ...(r.dropped_columns ?? []).map((c) => `column dropped: ${c.table}.${c.column} (${c.rows} row${c.rows === 1 ? "" : "s"})`),
    ...(r.docker_phase_skipped ? ["Crew files are not in the containers yet: start each crew, then bring back its files"] : []),
    ...(envs.length ? ["Processes start fresh; what was only in memory is not restored"] : []),
    ...(unsafe.length ? [`Not carried over for safety: ${unsafe.join(" · ")}`] : []),
  ]
  // The server's verdict wins; a reason found here can only make it worse.
  const result = r.result === "failed" ? "failed" : r.result === "partial" || warnings.length ? "partial" : "ok"
  return { result, summary: r.dry_run ? "Dry run finished" : "Restore finished", warnings, notes }
}

/**
 * A workspace or crew restore (and its dry run), through the per-workspace
 * POST /admin/backups/restore, which restores, forks and dry-runs. The
 * workspace is always named: an instance admin may restore one they are not a
 * member of. A whole-instance target is never sent — the server has no
 * instance restore route; that restore runs offline with `crewship recover`.
 */
/** A restore's report plus what the wizard still has to offer after it. */
export interface RestoreOutcome extends RestoreReport {
  /** The workspace the data landed in (a new one under a new name). */
  restoredWorkspaceId?: string | null
  /** Crew files are not in the containers yet: bring them back (files_only). */
  crewFilesPending?: boolean
  /** A complete environment was rebuilt or skipped instead of restored. */
  environmentsPending?: boolean
}

export async function restore(req: RestoreRequest, workspaceId: string | null): Promise<SendResult<RestoreOutcome>> {
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
    const body = (await res.json().catch(() => ({}))) as LegacyRestoreReport & { restored_workspace_id?: string }
    return {
      ok: true,
      data: {
        ...legacyReport({ dry_run: req.dry_run, ...body }),
        restoredWorkspaceId: body.restored_workspace_id || null,
        crewFilesPending: !req.dry_run && body.docker_phase_skipped === true,
        environmentsPending: (body.environments ?? []).some((e) => e.result !== "restored"),
      },
    }
  } catch (e) {
    return { ok: false, unavailable: false, error: e instanceof Error ? e.message : "Network error" }
  }
}

/**
 * The last step of a restore under a new name: copy each crew's files into
 * the restored workspace's containers, once they exist (POST
 * /admin/backups/restore with files_only). The server authorises it by the
 * provenance the restore wrote, never by the flag alone (#1716).
 */
export async function bringBackCrewFiles(req: Pick<RestoreRequest, "path" | "identity" | "passphrase">, workspaceId: string): Promise<SendResult<RestoreReport>> {
  try {
    const res = await apiFetch(`/api/v1/admin/backups/restore?workspace_id=${encodeURIComponent(workspaceId)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: req.path, identity: req.identity, passphrase: req.passphrase, files_only: true }),
    })
    if (!res.ok) return { ok: false, unavailable: isUnavailableStatus(res.status), error: await readError(res, `HTTP ${res.status}`) }
    return { ok: true, data: legacyReport((await res.json().catch(() => ({}))) as LegacyRestoreReport) }
  } catch (e) {
    return { ok: false, unavailable: false, error: e instanceof Error ? e.message : "Network error" }
  }
}
