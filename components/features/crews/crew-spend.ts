"use client"

import { formatSpend } from "@/components/features/activity-stream/activity-spend"

import { useWorkspaceResource } from "./memory-workspace"

export { formatSpend }

/** A crew's cost over the last 30 days, and each agent's share of it. */
export interface CrewSpend {
  costUsd: number
  calls: number
  byAgent: Map<string, number>
}

interface Rows<T> { rows: T[] }
const isRows = (v: unknown): v is Rows<Record<string, unknown>> =>
  typeof v === "object" && v !== null && Array.isArray((v as { rows?: unknown }).rows)
const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : 0)

/**
 * Joins GET /paymaster/spend/by-crew and GET /paymaster/spend/by-agent/{crewId}
 * (range=30d) for one crew. The rows carry ids only. A crew with no row has
 * spent nothing; an answer that is not the documented shape is unknown (null),
 * never zero, so a failed call cannot read as "free".
 */
export function crewSpend30d(byCrew: unknown, byAgent: unknown, crewId: string): CrewSpend | null {
  if (!isRows(byCrew) || !isRows(byAgent)) return null
  const own = byCrew.rows.find((r) => r.crew_id === crewId)
  const agents = new Map<string, number>()
  for (const r of byAgent.rows) if (typeof r.agent_id === "string") agents.set(r.agent_id, num(r.cost_usd))
  return { costUsd: num(own?.cost_usd), calls: num(own?.call_count), byAgent: agents }
}

/**
 * The 30-day spend of one crew. `enabled` is false for readers who may not
 * see spend (Activity › Spend is OWNER / ADMIN; the server does not check, so
 * the page does, #3476): nothing is requested for them. Null while loading,
 * when disabled and when either call fails.
 */
export function useCrewSpend(workspaceId: string, crewId: string | undefined, enabled: boolean): CrewSpend | null {
  const ws = encodeURIComponent(workspaceId)
  const on = enabled && !!crewId
  const byCrew = useWorkspaceResource<unknown>(on ? `/api/v1/paymaster/spend/by-crew?workspace_id=${ws}&range=30d` : "")
  const byAgent = useWorkspaceResource<unknown>(on ? `/api/v1/paymaster/spend/by-agent/${encodeURIComponent(crewId)}?workspace_id=${ws}&range=30d` : "")
  if (!on || byCrew.error || byAgent.error || byCrew.data === undefined || byAgent.data === undefined) return null
  return crewSpend30d(byCrew.data, byAgent.data, crewId)
}
