"use client"

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

import type { AgentRecord, AgentCredRow, AgentSkillRow } from "../agent-canvas-tabs/types"

// =============================================================================
// The relations the agent overview shows as cells. Each one is a slice — the
// screen that owns the concept (issues board, routines, vault) stays the only
// full implementation, and every cell links out to it.
//
// All three tolerate failure by staying empty. An agent with no issues is the
// normal state, and a 404 from a backend that predates one of these endpoints
// must not blank the whole overview.
// =============================================================================

export interface AgentIssueRow {
  id: string
  identifier?: string | null
  title: string
  status?: string | null
  priority?: string | null
  updated_at?: string | null
}

export interface AgentRelations {
  issues: AgentIssueRow[]
  credentials: AgentCredRow[]
  skills: AgentSkillRow[]
  loading: boolean
  refresh: () => void
}

export function useAgentRelations(workspaceId: string, agentId: string | undefined): AgentRelations {
  const [issues, setIssues] = useState<AgentIssueRow[]>([])
  const [credentials, setCredentials] = useState<AgentCredRow[]>([])
  const [skills, setSkills] = useState<AgentSkillRow[]>([])
  const [loading, setLoading] = useState(false)
  const [nonce, setNonce] = useState(0)

  const refresh = useCallback(() => setNonce((n) => n + 1), [])

  useEffect(() => {
    if (!agentId) {
      setIssues([]); setCredentials([]); setSkills([])
      return
    }
    const controller = new AbortController()
    const ws = encodeURIComponent(workspaceId)
    const id = encodeURIComponent(agentId)
    setLoading(true)
    // Clear first: without this the previous agent's rows stay on screen for
    // the length of the request, which reads as "this agent owns them".
    setIssues([]); setCredentials([]); setSkills([])

    const json = async <T,>(url: string): Promise<T[]> => {
      try {
        const r = await apiFetch(url, { signal: controller.signal })
        if (!r.ok) return []
        const data = await r.json()
        return Array.isArray(data) ? (data as T[]) : []
      } catch {
        return []
      }
    }

    void Promise.all([
      json<AgentIssueRow>(`/api/v1/issues?workspace_id=${ws}&assignee_id=${id}`),
      json<AgentCredRow>(`/api/v1/agents/${id}/credentials?workspace_id=${ws}`),
      json<AgentSkillRow>(`/api/v1/agents/${id}/skills?workspace_id=${ws}`),
    ]).then(([i, c, s]) => {
      if (controller.signal.aborted) return
      setIssues(i); setCredentials(c); setSkills(s)
      setLoading(false)
    })

    return () => controller.abort()
  }, [workspaceId, agentId, nonce])

  return { issues, credentials, skills, loading, refresh }
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
