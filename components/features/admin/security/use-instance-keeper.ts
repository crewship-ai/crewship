"use client"

import * as React from "react"

import { apiFetch } from "@/lib/api-fetch"
import type { KeeperLogEntry } from "@/app/(dashboard)/admin/types"

/**
 * Admin › Security across workspaces: the instance routes, which answer for
 * every workspace whether or not the admin belongs to it.
 *
 *   GET /admin/instance/keeper/governance   what each workspace has switched on
 *   GET /admin/instance/keeper/requests     the decision log, rows with their workspace
 *   GET /admin/instance/keeper/health       each workspace's decision window
 *   PUT /admin/instance/keeper/governance   one, several or all; dry_run previews
 */

export interface InstanceGovSettings {
  enabled: boolean
  security_contact_user_id: string
  deny_notify_min_risk: number
  watch_spec: string
  watch_presets: string[] | null
  require_second_approver: boolean
  gov_model_provider: string
  gov_model_id: string
  gov_model_credential_id: string
  auto_lease_seconds: number
  behavior_sample_every: number
}

export interface InstanceGovRow extends InstanceGovSettings {
  workspace_id: string
  workspace_name: string
  workspace_slug: string
  /** false = never set; the row shows the instance defaults it runs on. */
  configured: boolean
  effective_second_approver?: {
    min_security_level: number
    min_security_level_label?: string
    source: string
    tier_floor_security_level?: number
    tier_floor_label?: string
  }
}

export interface InstanceGovList {
  defaults: InstanceGovSettings & { configured: boolean }
  workspaces: InstanceGovRow[]
}

export interface GovFieldChange { field: string; before: unknown; after: unknown }

export interface GovSaveResult {
  applied: boolean
  changed: number
  defaults_updated: boolean
  workspaces: { workspace_id: string; workspace_name: string; workspace_slug: string; changes: GovFieldChange[]; warnings?: string[] }[]
}

export interface InstanceRequests {
  items: KeeperLogEntry[]
  total: number
  counts: { allow: number; deny: number; escalate: number; pending: number }
  by_workspace: { workspace_id: string; workspace_name: string; workspace_slug: string; count: number }[]
}

export interface InstanceHealthRow {
  workspace_id: string
  workspace_name: string
  workspace_slug: string
  samples: number
  min_samples: number
  progressed_rate: number
  judge_failure_rate: number
  p95_latency_ms: number
  alarm?: { kind: string; summary: string; at?: string } | null
}

/** Who a save reaches: every workspace (and the defaults), or a list. */
export type GovTargets = { all: true } | { workspaces: string[] }

async function errorOf(r: Response): Promise<string> {
  try {
    const e = (await r.json()) as { error?: string; detail?: string }
    return e.error ?? e.detail ?? `HTTP ${r.status}`
  } catch {
    return `HTTP ${r.status}`
  }
}

/** PUT /admin/instance/keeper/governance. Throws the server's message. */
export async function saveInstanceGovernance(targets: GovTargets, set: Partial<InstanceGovSettings>, dryRun = false): Promise<GovSaveResult> {
  const r = await apiFetch("/api/v1/admin/instance/keeper/governance", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...targets, dry_run: dryRun, set }),
  })
  if (!r.ok) throw new Error(await errorOf(r))
  return (await r.json()) as GovSaveResult
}

/**
 * The instance reads. `selected` narrows the decision log (null = every
 * workspace); the governance matrix and the health windows always cover them
 * all, since the panel and the matrix need every row.
 */
export function useInstanceKeeper(selected: string[] | null, liveTick = 0) {
  const [gov, setGov] = React.useState<InstanceGovList | null>(null)
  const [govError, setGovError] = React.useState<string | null>(null)
  const [requests, setRequests] = React.useState<InstanceRequests | null>(null)
  const [requestsError, setRequestsError] = React.useState<string | null>(null)
  const [health, setHealth] = React.useState<InstanceHealthRow[]>([])
  const [loading, setLoading] = React.useState(true)

  const filter = selected === null ? "" : selected.length === 0 ? null : selected.join(",")

  const loadGov = React.useCallback(async () => {
    try {
      const r = await apiFetch("/api/v1/admin/instance/keeper/governance")
      if (!r.ok) { setGovError(await errorOf(r)); return }
      setGov((await r.json()) as InstanceGovList)
      setGovError(null)
    } catch {
      setGovError("Workspace settings could not be read")
    }
  }, [])

  const loadRequests = React.useCallback(async () => {
    if (filter === null) {
      setRequests((prev) => ({ items: [], total: 0, counts: { allow: 0, deny: 0, escalate: 0, pending: 0 }, by_workspace: prev?.by_workspace ?? [] }))
      return
    }
    try {
      const q = new URLSearchParams({ limit: "500" })
      if (filter) q.set("workspace", filter)
      const r = await apiFetch(`/api/v1/admin/instance/keeper/requests?${q.toString()}`)
      if (!r.ok) { setRequestsError(await errorOf(r)); return }
      setRequests((await r.json()) as InstanceRequests)
      setRequestsError(null)
    } catch {
      setRequestsError("Activity could not be read")
    }
  }, [filter])

  const loadHealth = React.useCallback(async () => {
    try {
      const r = await apiFetch("/api/v1/admin/instance/keeper/health")
      if (r.ok) setHealth(((await r.json()) as { workspaces: InstanceHealthRow[] }).workspaces ?? [])
    } catch {
      /* the overview says nothing rather than something wrong */
    }
  }, [])

  const reload = React.useCallback(async () => {
    setLoading(true)
    await Promise.all([loadGov(), loadRequests(), loadHealth()])
    setLoading(false)
  }, [loadGov, loadRequests, loadHealth])

  React.useEffect(() => { void reload() }, [reload])
  React.useEffect(() => { if (liveTick) void loadRequests() }, [liveTick, loadRequests])

  return { gov, govError, requests, requestsError, health, loading, reload, reloadGov: loadGov }
}
