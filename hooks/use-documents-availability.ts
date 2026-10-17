"use client"

import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"

/** An unavailable or unconfirmed service never offers document creation. */
export function useDocumentsAvailability(workspaceId: string) {
  const query = useQuery({
    queryKey: ["documents-status", workspaceId],
    enabled: Boolean(workspaceId),
    retry: false,
    queryFn: async ({signal}) => {
      const response = await apiFetch(`/api/v1/documents/status?workspace_id=${encodeURIComponent(workspaceId)}`,{signal})
      if (!response.ok) throw new Error("Document availability could not be confirmed.")
      const value = await response.json()
      return value?.enabled === true
    },
  })
  // Failed refreshes must withdraw cached admission, while preserving the
  // query error for callers and the editor's separately retained local draft.
  return {...query, data: query.isError ? false : query.data}
}
