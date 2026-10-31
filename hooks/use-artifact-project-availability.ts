"use client"
import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
/** An unconfirmed or disabled service never offers project creation. With an
 *  agent the server also answers whether that agent's crew may create projects
 *  (`can_create`); an explicit `false` hides the option. */
export function useArtifactProjectAvailability(workspaceId: string, agentId?: string) {
 const query = useQuery({ queryKey: ["artifact-project-availability", workspaceId, agentId ?? null], enabled: Boolean(workspaceId), retry: false, queryFn: async ({ signal }) => {
  const scope = agentId ? `&agent_id=${encodeURIComponent(agentId)}` : ""
  const response = await apiFetch(`/api/v1/pages/artifacts/availability?workspace_id=${encodeURIComponent(workspaceId)}${scope}`, { signal })
  if (!response.ok) return false
  const body = await response.json()
  return body?.enabled === true && body?.can_create !== false
 } })
 return !query.isError && query.data === true
}
