"use client"
import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
/** An unconfirmed or disabled service never offers project creation. */
export function useArtifactProjectAvailability(workspaceId: string) {
 const query = useQuery({ queryKey: ["artifact-project-availability", workspaceId], enabled: Boolean(workspaceId), retry: false, queryFn: async ({ signal }) => {
  const response = await apiFetch(`/api/v1/pages/artifacts/availability?workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
  if (!response.ok) return false
  return (await response.json())?.enabled === true
 } })
 return !query.isError && query.data === true
}
