"use client"

import * as React from "react"

import { useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { buildDownloadUrl, type BackupListEntry, type VerifyBackupResponse } from "@/hooks/use-backups"
import { runsFixture } from "./__fixtures__/backups"
import { scopeQuery, type BackupRun, type BackupScope, type ScopeWorkspace } from "./backups-model"
import { INSTANCE_BACKUPS, isUnavailableStatus, listOf, readError, send, useResource, type Resource, type SendResult } from "./use-backups-data"

/** A bundle from the legacy per-workspace list, as a history row. */
export function legacyRun(b: BackupListEntry, workspace: { id: string; name: string } | null): BackupRun {
  return {
    id: b.path, plan_id: null, plan_name: null, trigger: "manual", note: null,
    scope: b.scope === "instance" ? "instance" : "workspaces",
    workspace_id: workspace?.id ?? null, workspace_name: b.scope === "crew" ? `${workspace?.name ?? "crew"} · crew` : workspace?.name ?? null,
    kind: "full", status: "done", phases: [], bundle_path: b.path, size_bytes: b.size_bytes, incomplete: [],
    started_at: b.created_at ?? "", ended_at: null, proof_level: 0, pinned: false,
    recipients: [], format_version: b.format_version ?? null, restorable: null, legacy: true,
  }
}

/**
 * GET /admin/instance/backups/runs, and — until that exists — the bundles the
 * legacy list already knows for the workspace the admin sits in
 * (GET /admin/backups), so the history is never blank on a server that has
 * backups. Legacy rows carry no phases and no proof beyond what a check adds.
 */
export function useBackupRuns(scope: BackupScope, selected: Set<string>, all: ScopeWorkspace[]): Resource<BackupRun[]> & { source: "runs" | "legacy" } {
  const { workspaceId } = useWorkspace()
  const runs = useResource<BackupRun[]>(`${INSTANCE_BACKUPS}/runs?${scopeQuery(scope, selected, all)}&limit=100`, () => runsFixture(new Date()), listOf)
  const [legacy, setLegacy] = React.useState<Resource<BackupRun[]> | null>(null)
  const [tick, setTick] = React.useState(0)
  const wsName = all.find((w) => w.id === workspaceId)?.name ?? null

  React.useEffect(() => {
    if (runs.status !== "unavailable" || !workspaceId) return
    const controller = new AbortController()
    void (async () => {
      try {
        const res = await apiFetch(`/api/v1/admin/backups?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal })
        if (controller.signal.aborted) return
        if (!res.ok) {
          setLegacy({ status: isUnavailableStatus(res.status) ? "unavailable" : "error", data: null, error: await readError(res, `HTTP ${res.status}`), demo: false, reload: () => setTick((t) => t + 1) })
          return
        }
        const rows = listOf<BackupListEntry>(await res.json()).map((b) => legacyRun(b, workspaceId ? { id: workspaceId, name: wsName ?? "This workspace" } : null))
        setLegacy({ status: "ready", data: rows, error: null, demo: false, reload: () => setTick((t) => t + 1) })
      } catch (e) {
        if (!controller.signal.aborted) setLegacy({ status: "error", data: null, error: e instanceof Error ? e.message : "Network error", demo: false, reload: () => setTick((t) => t + 1) })
      }
    })()
    return () => controller.abort()
  }, [runs.status, workspaceId, wsName, tick])

  if (runs.status === "unavailable" && legacy) return { ...legacy, source: "legacy" }
  return { ...runs, source: "runs" }
}

/** Pin or unpin a bundle so rotation never deletes it. */
export function pinBundle(path: string, pin: boolean): Promise<SendResult<unknown>> {
  return send(`${INSTANCE_BACKUPS}/bundles/${pin ? "pin" : "unpin"}`, "POST", { path })
}

/**
 * Check contents (proof 2): decrypt with a key and read every section. On a
 * server without the endpoint, the legacy verify still proves the checksum
 * (proof 1), and the result says which one ran.
 */
export async function checkBundle(path: string, key: { identity?: string; passphrase?: string }, workspaceId: string | null): Promise<SendResult<{ level: 1 | 2; ok: boolean; detail: string }>> {
  const r = await send<{ ok: boolean; proof_level: number; detail?: string; error?: string }>(`${INSTANCE_BACKUPS}/bundles/check`, "POST", { path, ...key })
  if (r.ok) return { ok: true, data: { level: 2, ok: r.data.ok, detail: r.data.detail ?? r.data.error ?? "" } }
  if (!r.unavailable || !workspaceId) return r
  try {
    const res = await apiFetch(`/api/v1/admin/backups/verify?workspace_id=${encodeURIComponent(workspaceId)}&path=${encodeURIComponent(path)}`)
    if (!res.ok) return { ok: false, unavailable: isUnavailableStatus(res.status), error: await readError(res, `HTTP ${res.status}`) }
    const v = (await res.json()) as VerifyBackupResponse
    return { ok: true, data: { level: 1, ok: v.valid, detail: v.valid ? "checksum matches (contents check not available on this server yet)" : v.error } }
  } catch (e) {
    return { ok: false, unavailable: false, error: e instanceof Error ? e.message : "Network error" }
  }
}

/** The download link: the legacy streaming endpoint, which works today. */
export function downloadHref(path: string, workspaceId: string | null): string | null {
  return workspaceId ? buildDownloadUrl(workspaceId, path) : null
}

/**
 * POST /admin/instance/backups/run — a run for the scope on screen. The
 * server queues it in the backup service and answers at once: `id` is the
 * first run, `run_ids` every run (one per workspace for scope=workspaces).
 */
export function runNow(body: { plan_id?: string; scope: BackupScope; workspace_ids?: string[]; preset?: string }): Promise<SendResult<{ id: string; run_id: string; run_ids: string[]; status: string }>> {
  return send(`${INSTANCE_BACKUPS}/run`, "POST", body)
}

