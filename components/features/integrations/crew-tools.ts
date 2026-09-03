/**
 * Crew tools — the crew-scoped MCP servers — and how a credential gets bound
 * to one (audit-fleet.md §6 P1 8).
 *
 * These servers lived on neither tab of /integrations; their only UI was the
 * flag-off legacy page. The crew's Settings tab listed them by name with a
 * blank where "No credential" belonged. This module is the data half of the
 * new Crew tools section: the rows, and the one operation Connect performs.
 */
import { apiFetch } from "@/lib/api-fetch"

export interface CrewTool {
  id: string
  crew_id: string
  crew_name: string
  crew_slug: string
  name: string
  display_name: string
  transport: string
  endpoint: string | null
  command: string | null
  enabled: boolean
  agent_binding_count: number
  /** "connected", "missing", "expired", "none" */
  auth_status: string
}

export function needsCredential(tool: Pick<CrewTool, "auth_status">): boolean {
  return tool.auth_status === "missing" || tool.auth_status === "expired"
}

/** Ranks the list: gaps first, then by crew and name — the same rule as
 *  every other list on the product (README §4). */
export function sortCrewTools(tools: CrewTool[]): CrewTool[] {
  const rank = (t: CrewTool) => (t.auth_status === "expired" ? 0 : t.auth_status === "missing" ? 1 : 2)
  return [...tools].sort((a, b) => rank(a) - rank(b) || a.crew_name.localeCompare(b.crew_name) || a.name.localeCompare(b.name))
}

export async function fetchCrewTools(workspaceId: string): Promise<CrewTool[]> {
  const res = await apiFetch(`/api/v1/integrations/crews?workspace_id=${encodeURIComponent(workspaceId)}`)
  if (!res.ok) throw new Error(`Could not load crew tools (HTTP ${res.status})`)
  const body: unknown = await res.json()
  return Array.isArray(body) ? (body as CrewTool[]) : []
}

interface AgentRow { id: string }
interface BindingRow { id: string; mcp_server_id: string; mcp_server_scope: string }

/**
 * Bind a credential to a crew tool for every agent in the crew: agents that
 * already have a binding for the server get it patched, the rest get one
 * created. Mirrors what the legacy OAuth flow did, minus the silence — the
 * caller gets the failures back to say so.
 */
export async function bindCredentialToCrewTool({
  workspaceId,
  tool,
  credentialId,
}: {
  workspaceId: string
  tool: Pick<CrewTool, "id" | "crew_id">
  credentialId: string
}): Promise<{ bound: number; failures: string[] }> {
  const ws = encodeURIComponent(workspaceId)
  const agentsRes = await apiFetch(`/api/v1/agents?workspace_id=${ws}&crew_id=${encodeURIComponent(tool.crew_id)}&limit=500`)
  if (!agentsRes.ok) throw new Error(`Could not list the crew's agents (HTTP ${agentsRes.status})`)
  const agents = ((await agentsRes.json()) as AgentRow[]).filter((a) => a && typeof a.id === "string")

  const failures: string[] = []
  let bound = 0
  for (const agent of agents) {
    try {
      const listRes = await apiFetch(`/api/v1/agents/${agent.id}/integrations?workspace_id=${ws}`)
      const bindings: BindingRow[] = listRes.ok ? await listRes.json() : []
      const existing = Array.isArray(bindings)
        ? bindings.find((b) => b.mcp_server_id === tool.id && b.mcp_server_scope === "crew")
        : undefined
      const res = existing
        ? await apiFetch(`/api/v1/agents/${agent.id}/integrations/${existing.id}?workspace_id=${ws}`, {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ credential_id: credentialId, cred_type: "bearer" }),
          })
        : await apiFetch(`/api/v1/agents/${agent.id}/integrations?workspace_id=${ws}`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              mcp_server_id: tool.id,
              mcp_server_scope: "crew",
              credential_id: credentialId,
              cred_type: "bearer",
              enabled: true,
            }),
          })
      if (res.ok) bound += 1
      else failures.push(`agent ${agent.id}: HTTP ${res.status}`)
    } catch (e) {
      failures.push(`agent ${agent.id}: ${e instanceof Error ? e.message : String(e)}`)
    }
  }
  return { bound, failures }
}
