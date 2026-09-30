"use client"

import * as React from "react"

import { retentionFixture } from "./__fixtures__/backups"
import type { RetentionChange, RetentionPutResponse, RetentionResponse, RetentionRow } from "./backups-model"
import { send, useResource, type SendResult } from "./use-backups-data"

const RETENTION = "/api/v1/admin/instance/retention"

/** The server's answer to GET /admin/instance/retention (internal/api/admin_instance_retention.go). */
interface RetentionApi {
  workspaces: { workspace_id: string; workspace_name: string; workspace_slug: string; windows: Record<string, number | null> }[]
  keys: { key: string; label: string; detail?: string | null; forever_allowed: boolean }[]
  housekeeping: { key: string; label: string; value: string; detail?: string | null }[]
}

/** The server's answer to PUT /admin/instance/retention. */
interface RetentionPutApi {
  dry_run: boolean
  workspaces: { workspace_id: string; workspace_name: string; changes: { key: string; from: number | null; to: number | null; rows_affected_next_sweep: number }[] }[]
}

/**
 * One row per window, in the server's display order. With several workspaces
 * a row shows the value they share, or "mixed" when they differ.
 */
export function toRetentionRows(api: RetentionApi): RetentionResponse {
  const rows: RetentionRow[] = api.keys.map((k) => {
    const values = api.workspaces.map((w) => w.windows[k.key] ?? null)
    const first = values.length ? values[0] : null
    const mixed = values.some((v) => v !== first)
    return { key: k.key, label: k.label, note: k.detail ?? null, days: mixed ? null : first, mixed, foreverAllowed: k.forever_allowed }
  })
  for (const h of api.housekeeping) {
    rows.push({ key: h.key, label: h.label, note: h.detail ?? null, days: null, housekeeping: true, fixed: h.value })
  }
  return { rows }
}

export function toRetentionChanges(api: RetentionPutApi): RetentionPutResponse {
  const changes: RetentionChange[] = api.workspaces.flatMap((w) => w.changes.map((c) => ({
    workspace_id: w.workspace_id, workspace_name: w.workspace_name, key: c.key, from: c.from, to: c.to, rows_affected: c.rows_affected_next_sweep,
  })))
  return { dry_run: api.dry_run, changes }
}

/** GET /admin/instance/retention?ws= for the ticked workspaces. */
export function useDataRetention(selected: Set<string>) {
  const ws = React.useMemo(() => [...selected].sort().join(","), [selected])
  return useResource<RetentionResponse>(selected.size ? `${RETENTION}?ws=${encodeURIComponent(ws)}` : null, retentionFixture,
    (json) => toRetentionRows(json as RetentionApi))
}

/**
 * PUT /admin/instance/retention. `workspaceIds` null is every existing
 * workspace — a selection only; it never changes the defaults new workspaces
 * start with (that is putRetentionDefaults, a separate operation). With
 * dry_run the server lists every value it would overwrite and how many rows
 * the next sweep deletes; nothing changes.
 */
export async function putRetention(workspaceIds: string[] | null, changes: { key: string; days: number | null }[], dryRun: boolean): Promise<SendResult<RetentionPutResponse>> {
  const windows = Object.fromEntries(changes.map((c) => [c.key, c.days]))
  const r = await send<RetentionPutApi>(RETENTION, "PUT", { workspace_ids: workspaceIds, windows, dry_run: dryRun })
  return r.ok ? { ok: true, data: toRetentionChanges(r.data) } : r
}

const DEFAULTS = `${RETENTION}/defaults`

export interface RetentionDefaults { defaults: Record<string, number | null>; configured: string[] }
export interface RetentionDefaultsPut { applied: boolean; dry_run: boolean; changes: { key: string; from: number | null; to: number | null }[] }

/** GET /admin/instance/retention/defaults: what a new workspace starts with. */
export function useRetentionDefaults(enabled: boolean) {
  return useResource<RetentionDefaults>(enabled ? DEFAULTS : null, () => ({
    defaults: { routine_runs_days: 90, approvals_days: 90, audit_days: null, credential_audit_days: 90, memory_versions_days: 30, page_panel_data_days: 7, inbox_days: null, chats_days: null, keeper_decisions_days: null },
    configured: [],
  }))
}

/**
 * PUT /admin/instance/retention/defaults. Changes only what a workspace
 * created from now on starts with; no existing workspace or row is touched.
 */
export function putRetentionDefaults(windows: Record<string, number | null>, dryRun: boolean): Promise<SendResult<RetentionDefaultsPut>> {
  return send<RetentionDefaultsPut>(DEFAULTS, "PUT", { windows, dry_run: dryRun })
}
