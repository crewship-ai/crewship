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
      return {
        enabled: value?.enabled === true,
        wordcraftNative: value?.wordcraft_native === true,
        gridcraftNative: value?.gridcraft_native === true,
        wordcraftCommands: value?.wordcraft_commands === true,
        wordcraftSDKPrefix: typeof value?.wordcraft_sdk_prefix === "string" && /^\/mooring-sdk\/generated\/[0-9a-f]{64}$/.test(value.wordcraft_sdk_prefix)
          ? value.wordcraft_sdk_prefix as string : undefined,
      }
    },
  })
  // Failed refreshes must withdraw cached admission, while preserving the
  // query error for callers and the editor's separately retained local draft.
  return {...query, data: query.isError ? false : query.data?.enabled, wordcraftNative: !query.isError && query.data?.wordcraftNative === true, gridcraftNative: !query.isError && query.data?.gridcraftNative === true, wordcraftCommands: !query.isError && query.data?.wordcraftCommands === true, wordcraftSDKPrefix: query.isError ? undefined : query.data?.wordcraftSDKPrefix}
}
