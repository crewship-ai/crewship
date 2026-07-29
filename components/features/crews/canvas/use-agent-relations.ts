"use client"

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { readThrough } from "@/lib/stale-cache"

import type { AgentRecord, AgentCredRow, AgentSkillRow } from "../agent-canvas-tabs/types"

// =============================================================================
// The relations the agent overview shows as cells.
//
// Everything here is read THROUGH the stale cache, and that is not an
// optimisation — it is the fix for a real outage. The overview mounts five
// fetches, the canvas above it mounts three more, and the workspace pipeline
// list used to be re-fetched on every agent selection. Clicking down a roster
// of a dozen agents therefore issued ~100 requests inside a minute and tripped
// the server's 120/min bucket; the 429s took the realtime stream with them and
// the whole app fell into "reconnecting" until the window rolled over.
//
// Cached reads make a second visit free and a roster walk cheap, and each
// fetch still degrades to empty rather than throwing: an agent with no issues
// is the normal state, and a backend that predates one of these endpoints must
// not blank the overview.
// =============================================================================

const TTL_MS = 30_000

export interface AgentIssueRow {
  id: string
  identifier?: string | null
  title: string
  status?: string | null
  priority?: string | null
  updated_at?: string | null
}

export interface AgentPipelineRow {
  id: string
  slug: string
  name?: string | null
  author_agent_id?: string | null
}

export interface AgentRelations {
  issues: AgentIssueRow[]
  credentials: AgentCredRow[]
  skills: AgentSkillRow[]
  pipelines: AgentPipelineRow[]
  loading: boolean
  refresh: () => void
}

async function fetchList<T>(url: string, signal?: AbortSignal): Promise<T[]> {
  const r = await apiFetch(url, { signal })
  if (!r.ok) return []
  const data = await r.json()
  return Array.isArray(data) ? (data as T[]) : []
}

export function useAgentRelations(workspaceId: string, agentId: string | undefined): AgentRelations {
  const [issues, setIssues] = useState<AgentIssueRow[]>([])
  const [credentials, setCredentials] = useState<AgentCredRow[]>([])
  const [skills, setSkills] = useState<AgentSkillRow[]>([])
  const [pipelines, setPipelines] = useState<AgentPipelineRow[]>([])
  const [loading, setLoading] = useState(false)
  const [nonce, setNonce] = useState(0)

  const refresh = useCallback(() => setNonce((n) => n + 1), [])

  useEffect(() => {
    if (!agentId) {
      setIssues([]); setCredentials([]); setSkills([]); setPipelines([])
      return
    }
    const controller = new AbortController()
    const ws = encodeURIComponent(workspaceId)
    const id = encodeURIComponent(agentId)

    const cached = {
      issues: readThrough(`agent:${agentId}:issues`,
        () => fetchList<AgentIssueRow>(`/api/v1/issues?workspace_id=${ws}&assignee_id=${id}`, controller.signal), TTL_MS),
      credentials: readThrough(`agent:${agentId}:credentials`,
        () => fetchList<AgentCredRow>(`/api/v1/agents/${id}/credentials?workspace_id=${ws}`, controller.signal), TTL_MS),
      skills: readThrough(`agent:${agentId}:skills`,
        () => fetchList<AgentSkillRow>(`/api/v1/agents/${id}/skills?workspace_id=${ws}`, controller.signal), TTL_MS),
      // Workspace-scoped, so the key deliberately omits the agent — walking a
      // roster must not re-fetch the same list once per agent.
      pipelines: readThrough(`workspace:${workspaceId}:pipelines`,
        () => fetchList<AgentPipelineRow>(`/api/v1/workspaces/${ws}/pipelines`, controller.signal), TTL_MS),
    }

    // Paint whatever the cache already holds before any request resolves.
    if (cached.issues.value) setIssues(cached.issues.value)
    if (cached.credentials.value) setCredentials(cached.credentials.value)
    if (cached.skills.value) setSkills(cached.skills.value)
    if (cached.pipelines.value) setPipelines(cached.pipelines.value)
    setLoading(true)

    void Promise.allSettled([
      cached.issues.fresh, cached.credentials.fresh, cached.skills.fresh, cached.pipelines.fresh,
    ]).then(([i, c, s, p]) => {
      if (controller.signal.aborted) return
      setIssues(i.status === "fulfilled" ? i.value : [])
      setCredentials(c.status === "fulfilled" ? c.value : [])
      setSkills(s.status === "fulfilled" ? s.value : [])
      setPipelines(p.status === "fulfilled" ? p.value : [])
      setLoading(false)
    })

    return () => controller.abort()
  }, [workspaceId, agentId, nonce])

  return { issues, credentials, skills, pipelines, loading, refresh }
}

// =============================================================================
// "Čím se spouští" is derived, not fetched. Every trigger the backend supports
// is already on the agent record or in its inbox, and deriving it here keeps
// the four ways an agent can start work in one list instead of scattered
// across three tabs.
// =============================================================================

export type TriggerKind = "schedule" | "webhook" | "delegation" | "manual"

export interface AgentTrigger {
  kind: TriggerKind
  title: string
  subtitle: string
  meta: string
  /** Automatic triggers fire without a person; manual ones need one. */
  automatic: boolean
}

export function deriveTriggers(agent: AgentRecord, peerMessageCount: number): AgentTrigger[] {
  const triggers: AgentTrigger[] = []

  if (agent.schedule_cron) {
    triggers.push({
      kind: "schedule",
      title: `Plán · ${agent.schedule_cron}`,
      subtitle: agent.schedule_next_run
        ? `příští běh ${new Date(agent.schedule_next_run).toLocaleString()}`
        : "příští běh se spočítá po uložení",
      meta: agent.schedule_enabled === false ? "vypnuto" : "aktivní",
      automatic: true,
    })
  }

  // The secret itself is show-once and never readable back; the record only
  // reports whether one is configured, which is exactly what this row needs.
  const webhookSet = (agent as AgentRecord & { webhook_secret_set?: boolean }).webhook_secret_set
  if (webhookSet) {
    triggers.push({
      kind: "webhook",
      title: "Webhook",
      subtitle: `POST /api/v1/agents/${agent.slug}/trigger`,
      meta: "aktivní",
      automatic: true,
    })
  }

  if (agent.agent_role === "LEAD" || peerMessageCount > 0) {
    triggers.push({
      kind: "delegation",
      title: "Delegace od kolegy",
      subtitle: peerMessageCount > 0 ? `${peerMessageCount} čekajících zpráv` : "žádná čekající zpráva",
      meta: "povoleno",
      automatic: true,
    })
  }

  triggers.push({
    kind: "manual",
    title: "Ručně z chatu nebo CLI",
    subtitle: `crewship agent run ${agent.slug}`,
    meta: "—",
    automatic: false,
  })

  return triggers
}
