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
  defaults: InstanceGovSettings & { configured: boolean; effective_second_approver?: InstanceGovRow["effective_second_approver"] }
  workspaces: InstanceGovRow[]
}

export interface GovFieldChange { field: string; before: unknown; after: unknown }

export interface GovSaveResult {
  /** Fingerprint of what the save does; send it back to confirm a preview. */
  preview_id?: string
  applied: boolean
  changed: number
  workspaces: { workspace_id: string; workspace_name: string; workspace_slug: string; changes: GovFieldChange[]; warnings?: string[] }[]
}

export interface InstanceRequests {
  items: KeeperLogEntry[]
  total: number
  counts: { allow: number; deny: number; escalate: number; pending: number }
  by_workspace: { workspace_id: string; workspace_name: string; workspace_slug: string; count: number }[]
  /** Every kind under the workspace filter alone. */
  by_type: Record<string, number>
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
export async function saveInstanceGovernance(targets: GovTargets, set: Partial<InstanceGovSettings>, dryRun = false, expectPreview?: string): Promise<GovSaveResult> {
  const r = await apiFetch("/api/v1/admin/instance/keeper/governance", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...targets, dry_run: dryRun, ...(expectPreview ? { expect_preview: expectPreview } : {}), set }),
  })
  if (!r.ok) throw new Error(await errorOf(r))
  return (await r.json()) as GovSaveResult
}

export interface DefaultsResult {
  applied: boolean
  configured: boolean
  defaults: InstanceGovSettings
  changes: GovFieldChange[]
  preview_id: string
}

/**
 * PUT /admin/instance/keeper/governance/defaults: the template a new
 * workspace copies when it is created. Changes no existing workspace.
 */
export async function saveDefaults(set: Partial<InstanceGovSettings>, dryRun: boolean, expectPreview?: string): Promise<DefaultsResult> {
  const r = await apiFetch("/api/v1/admin/instance/keeper/governance/defaults", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ dry_run: dryRun, ...(expectPreview ? { expect_preview: expectPreview } : {}), set }),
  })
  if (!r.ok) throw new Error(await errorOf(r))
  return (await r.json()) as DefaultsResult
}

/** What the decision log is narrowed to on the server (review R6). */
export interface RequestFilter {
  /** request_type values; a credential request is access or execute. */
  types?: string[]
  /** ALLOW, DENY, ESCALATE or PENDING. */
  decision?: string
}

const PAGE = 100

/**
 * The instance reads. `selected` narrows the decision log (null = every
 * workspace) and `filter` narrows it by kind and decision, both on the server,
 * which pages through the whole history. The governance matrix and the health
 * windows always cover every workspace, since the panel and the matrix need
 * every row.
 *
 * Every read carries a generation: an answer for a query that is no longer
 * the current one (the selection or the filter moved on while it was in
 * flight) is dropped, never shown under the new scope (review R4).
 */
export function useInstanceKeeper(selected: string[] | null, liveTick = 0, filter: RequestFilter = {}) {
  const [gov, setGov] = React.useState<InstanceGovList | null>(null)
  const [govError, setGovError] = React.useState<string | null>(null)
  const [requests, setRequests] = React.useState<InstanceRequests | null>(null)
  const [requestsError, setRequestsError] = React.useState<string | null>(null)
  const [requestsLoading, setRequestsLoading] = React.useState(true)
  const [health, setHealth] = React.useState<InstanceHealthRow[]>([])
  const [healthError, setHealthError] = React.useState<string | null>(null)
  const [loading, setLoading] = React.useState(true)

  const scope = selected === null ? "" : selected.length === 0 ? null : selected.join(",")
  const types = (filter.types ?? []).join(",")
  const decision = filter.decision ?? ""
  const queryKey = `${scope}|${types}|${decision}`
  const generation = React.useRef({ gov: 0, requests: 0, health: 0 })

  const loadGov = React.useCallback(async () => {
    const g = ++generation.current.gov
    try {
      const r = await apiFetch("/api/v1/admin/instance/keeper/governance")
      if (g !== generation.current.gov) return
      if (!r.ok) { setGovError(await errorOf(r)); return }
      setGov((await r.json()) as InstanceGovList)
      setGovError(null)
    } catch {
      if (g === generation.current.gov) setGovError("Workspace settings could not be read")
    }
  }, [])

  const url = React.useCallback((offset: number) => {
    const q = new URLSearchParams({ limit: String(PAGE), offset: String(offset) })
    if (scope) q.set("workspace", scope)
    if (types) q.set("request_type", types)
    if (decision) q.set("decision", decision)
    return `/api/v1/admin/instance/keeper/requests?${q.toString()}`
  }, [scope, types, decision])

  const loadRequests = React.useCallback(async () => {
    const g = ++generation.current.requests
    if (scope === null) {
      setRequests((prev) => ({ items: [], total: 0, counts: { allow: 0, deny: 0, escalate: 0, pending: 0 }, by_workspace: prev?.by_workspace ?? [], by_type: {} }))
      setRequestsError(null)
      setRequestsLoading(false)
      return
    }
    setRequestsLoading(true)
    try {
      const r = await apiFetch(url(0))
      if (g !== generation.current.requests) return
      if (!r.ok) { setRequestsError(await errorOf(r)); return }
      const body = (await r.json()) as InstanceRequests
      if (g !== generation.current.requests) return
      setRequests(body)
      setRequestsError(null)
    } catch {
      if (g === generation.current.requests) setRequestsError("Activity could not be read")
    } finally {
      if (g === generation.current.requests) setRequestsLoading(false)
    }
  }, [scope, url])

  // One next-page read at a time: a second click while one is in flight
  // would ask for the same offset and append the page twice.
  const loadingMore = React.useRef(false)

  /** The next page of the same query, appended. */
  const loadMore = React.useCallback(async () => {
    if (loadingMore.current) return
    loadingMore.current = true
    const g = generation.current.requests
    const have = requests?.items.length ?? 0
    try {
      const r = await apiFetch(url(have))
      if (g !== generation.current.requests || !r.ok) return
      const body = (await r.json()) as InstanceRequests
      if (g !== generation.current.requests) return
      setRequests((prev) => prev ? { ...body, items: [...prev.items, ...body.items] } : body)
    } catch {
      /* the button stays; a retry is one click */
    } finally {
      loadingMore.current = false
    }
  }, [requests, url])

  const loadHealth = React.useCallback(async () => {
    const g = ++generation.current.health
    try {
      const r = await apiFetch("/api/v1/admin/instance/keeper/health")
      if (g !== generation.current.health) return
      if (!r.ok) { setHealthError(await errorOf(r)); return }
      setHealth(((await r.json()) as { workspaces: InstanceHealthRow[] }).workspaces ?? [])
      setHealthError(null)
    } catch {
      if (g === generation.current.health) setHealthError("Judge health could not be read")
    }
  }, [])

  const reload = React.useCallback(async () => {
    setLoading(true)
    await Promise.all([loadGov(), loadRequests(), loadHealth()])
    setLoading(false)
  }, [loadGov, loadRequests, loadHealth])

  React.useEffect(() => { void loadRequests() }, [queryKey]) // eslint-disable-line react-hooks/exhaustive-deps -- the query key is the dependency
  React.useEffect(() => { void Promise.all([loadGov(), loadHealth()]).then(() => setLoading(false)) }, [loadGov, loadHealth])
  React.useEffect(() => { if (liveTick) void loadRequests() }, [liveTick]) // eslint-disable-line react-hooks/exhaustive-deps -- a live event reloads the current query

  return { gov, govError, requests, requestsError, requestsLoading, loadMore, health, healthError, loading, reload, reloadGov: loadGov }
}
