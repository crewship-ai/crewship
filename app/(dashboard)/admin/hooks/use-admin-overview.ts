"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"

import type {
  AdminHealth, AgentsStatus, AuxStatus, DaemonStatus, JournalIntegrity, KeeperHealth,
  LicenseInfo, SecurityPosture, TelemetryInfo, TimeseriesPoint, VersionInfo,
} from "../types"

/**
 * Everything the Overview reads, each piece on its own.
 *
 * The page used to wait for all of it in one Promise.all, so the slowest
 * probe — the journal chain verification, seconds on a busy instance — held
 * the whole screen on a skeleton. Now every card fills as its answer lands,
 * and the chain check runs last so it never competes with the cheap reads.
 *
 * Only facts the server measures itself are read here: nothing depends on the
 * host OS beyond disk capacity, which the server reports on Linux, macOS and
 * Windows and marks unavailable elsewhere.
 */
export interface AdminOverviewData {
  version: VersionInfo | null
  health: AdminHealth | null
  license: LicenseInfo | null
  telemetry: TelemetryInfo | null
  posture: SecurityPosture | null
  daemon: DaemonStatus | null
  aux: AuxStatus | null
  agents: AgentsStatus | null
  keeperHealth: KeeperHealth | null
  runs: TimeseriesPoint[] | null
  cost: TimeseriesPoint[] | null
  journal: JournalIntegrity | null
  /** Whether each read has answered (successfully or not). */
  settled: Partial<Record<keyof Omit<AdminOverviewData, "settled">, boolean>>
}

const EMPTY: AdminOverviewData = {
  version: null, health: null, license: null, telemetry: null, posture: null, daemon: null,
  aux: null, agents: null, keeperHealth: null, runs: null, cost: null, journal: null, settled: {},
}

type Key = keyof Omit<AdminOverviewData, "settled">

function series(raw: unknown): TimeseriesPoint[] | null {
  const buckets = (raw as { buckets?: { ts: string; series?: { total?: number } }[] })?.buckets
  if (!Array.isArray(buckets)) return null
  return buckets.map((b) => ({ ts: b.ts, value: Number(b.series?.total ?? 0) }))
}

export function useAdminOverview(workspaceId: string | null, enabled: boolean): AdminOverviewData & { reload: () => void } {
  const [data, setData] = useState<AdminOverviewData>(EMPTY)
  const generation = useRef(0)

  const load = useCallback(() => {
    if (!workspaceId || !enabled) return
    const gen = ++generation.current
    const ws = encodeURIComponent(workspaceId)
    const put = (key: Key, value: unknown) => {
      if (gen !== generation.current) return
      setData((d) => ({ ...d, [key]: value ?? d[key], settled: { ...d.settled, [key]: true } }))
    }
    const read = async (key: Key, url: string, map: (raw: unknown) => unknown = (x) => x) => {
      try {
        const res = await apiFetch(url)
        put(key, res.ok ? map(await res.json()) : null)
      } catch {
        put(key, null)
      }
    }
    const cheap = [
      read("version", "/api/v1/system/version"),
      read("health", `/api/v1/admin/health?workspace_id=${ws}`),
      read("license", `/api/v1/system/license?workspace_id=${ws}`),
      read("telemetry", "/api/v1/system/telemetry"),
      read("posture", `/api/v1/admin/security-posture?workspace_id=${ws}`),
      read("daemon", `/api/v1/crewshipd?workspace_id=${ws}`),
      read("aux", `/api/v1/system/aux-status?workspace_id=${ws}`),
      read("agents", `/api/v1/agents/crews-status?workspace_id=${ws}`),
      read("keeperHealth", `/api/v1/admin/keeper/health?workspace_id=${ws}`),
      read("runs", `/api/v1/metrics/timeseries?workspace_id=${ws}&metric=runs_count&window=7d&bucket=1d`, series),
      read("cost", `/api/v1/metrics/timeseries?workspace_id=${ws}&metric=cost_usd&window=7d&bucket=1d`, series),
    ]
    // The chain walk is the one expensive read; start it after the rest.
    void Promise.allSettled(cheap).then(() => read("journal", `/api/v1/admin/journal/verify?workspace_id=${ws}`))
  }, [workspaceId, enabled])

  useEffect(() => { load() }, [load])

  return { ...data, reload: load }
}
