"use client"

/**
 * Who gets a credential at run time: rows of `credential_bindings` — (scope,
 * slot) → credential, scope AGENT, CREW or WORKSPACE. The same rows
 * `crewship credential bind` writes. Creating and removing them is
 * OWNER/ADMIN (the server repeats the check).
 *
 * Shared by an AI provider account's "Pays for" tab and a secret's "Give to
 * agents" picker. Neither reads or sends a credential's value.
 */

import * as React from "react"

import { apiFetch } from "@/lib/api-fetch"

export interface BindingRow {
  id: string
  credential_id: string
  credential_name?: string
  scope: string
  crew_id: string | null
  agent_id: string | null
  slot: string
  created_at?: string
}

export interface BindingAgent {
  id: string
  name: string
  slug?: string
  cli_adapter?: string | null
  crew_id?: string | null
  crew?: { name?: string } | null
}

export type BindingTarget =
  | { scope: "AGENT"; agentId: string }
  | { scope: "CREW"; crewId: string }
  | { scope: "WORKSPACE" }

export function targetKey(t: BindingTarget): string {
  return t.scope === "AGENT" ? `AGENT:${t.agentId}` : t.scope === "CREW" ? `CREW:${t.crewId}` : "WORKSPACE"
}

export function bindingKey(b: BindingRow): string {
  const scope = b.scope.toUpperCase()
  return scope === "AGENT" ? `AGENT:${b.agent_id}` : scope === "CREW" ? `CREW:${b.crew_id}` : "WORKSPACE"
}

/** POST /credentials/bindings. Resolves to the error sentence, or null when it landed. */
export async function createBinding(workspaceId: string, credentialId: string, target: BindingTarget, slot: string): Promise<string | null> {
  try {
    const res = await apiFetch(`/api/v1/credentials/bindings?workspace_id=${encodeURIComponent(workspaceId)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        credential_id: credentialId,
        scope: target.scope,
        crew_id: target.scope === "CREW" ? target.crewId : "",
        agent_id: target.scope === "AGENT" ? target.agentId : "",
        slot,
      }),
    })
    if (res.ok) return null
    const data = await res.json().catch(() => ({}))
    if (res.status === 409) return `${slot} is already taken there by another credential. Remove that assignment first.`
    if (res.status === 403) return "Only workspace owners and admins can change assignments."
    return typeof data?.error === "string" ? data.error.slice(0, 300) : `The server refused (HTTP ${res.status}).`
  } catch {
    return "Network error."
  }
}

/** DELETE /credentials/bindings/{id}. Resolves to the error sentence, or null. */
export async function deleteBinding(workspaceId: string, bindingId: string): Promise<string | null> {
  try {
    const res = await apiFetch(`/api/v1/credentials/bindings/${encodeURIComponent(bindingId)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "DELETE" })
    if (res.ok || res.status === 404) return null
    if (res.status === 403) return "Only workspace owners and admins can change assignments."
    return `The server refused (HTTP ${res.status}).`
  } catch {
    return "Network error."
  }
}

/** The agents of the workspace and this credential's bindings, loaded together. */
export function useBindingTargets(workspaceId: string, credentialId: string, enabled = true) {
  const [agents, setAgents] = React.useState<BindingAgent[] | null>(null)
  const [bindings, setBindings] = React.useState<BindingRow[] | null>(null)
  const [error, setError] = React.useState(false)
  const [tick, setTick] = React.useState(0)

  React.useEffect(() => {
    if (!enabled) return
    let cancelled = false
    setError(false)
    const ws = encodeURIComponent(workspaceId)
    Promise.all([
      apiFetch(`/api/v1/agents?workspace_id=${ws}`).then((r) => (r.ok ? r.json() : Promise.reject(new Error()))),
      apiFetch(`/api/v1/credentials/bindings?workspace_id=${ws}&credential_id=${encodeURIComponent(credentialId)}`).then((r) => (r.ok ? r.json() : Promise.reject(new Error()))),
    ])
      .then(([a, b]: [unknown, { bindings?: BindingRow[] }]) => {
        if (cancelled) return
        setAgents(Array.isArray(a) ? (a as BindingAgent[]).filter((x) => typeof x?.id === "string") : [])
        setBindings(Array.isArray(b?.bindings) ? b.bindings : [])
      })
      .catch(() => { if (!cancelled) setError(true) })
    return () => { cancelled = true }
  }, [workspaceId, credentialId, enabled, tick])

  const reload = React.useCallback(() => setTick((t) => t + 1), [])
  return { agents, bindings, setBindings, error, reload, loading: enabled && !error && (agents === null || bindings === null) }
}

/** Agents grouped by crew, crews in name order, the crewless last. */
export function agentsByCrew(
  agents: BindingAgent[],
  crewName: (id: string) => string | undefined,
): { crewId: string | null; name: string; agents: BindingAgent[] }[] {
  const by = new Map<string | null, BindingAgent[]>()
  for (const a of agents) {
    const k = a.crew_id ?? null
    by.set(k, [...(by.get(k) ?? []), a])
  }
  return [...by.entries()]
    .map(([crewId, list]) => ({
      crewId,
      name: crewId ? crewName(crewId) ?? list[0]?.crew?.name ?? "Crew" : "No crew",
      agents: [...list].sort((x, y) => x.name.localeCompare(y.name)),
    }))
    .sort((a, b) => (a.crewId === null ? 1 : b.crewId === null ? -1 : a.name.localeCompare(b.name)))
}
