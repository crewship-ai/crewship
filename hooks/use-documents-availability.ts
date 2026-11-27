"use client"

import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"

/** The instance administrator's mode for each Craft application ("open", "draining" or "closed"). */
export type CraftApplicationMode = "open" | "draining" | "closed"
const MODES: readonly string[] = ["open", "draining", "closed"]
function craftModes(value: unknown): Partial<Record<"wordcraft" | "gridcraft", CraftApplicationMode>> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {}
  const out: Partial<Record<"wordcraft" | "gridcraft", CraftApplicationMode>> = {}
  for (const app of ["wordcraft", "gridcraft"] as const) {
    const mode = (value as Record<string, unknown>)[app]
    if (typeof mode === "string" && MODES.includes(mode)) out[app] = mode as CraftApplicationMode
  }
  return out
}

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
        // The server keeps Sheets as their workbook files (package document_format workbook): an Excel file opens as a Sheet at once.
        gridcraftWorkbook: value?.gridcraft_workbook === true,
        wordcraftCommands: value?.wordcraft_commands === true,
        wordcraftSDKPrefix: typeof value?.wordcraft_sdk_prefix === "string" && /^\/mooring-sdk\/generated\/[0-9a-f]{64}$/.test(value.wordcraft_sdk_prefix)
          ? value.wordcraft_sdk_prefix as string : undefined,
        applications: craftModes(value?.applications),
      }
    },
  })
  // Failed refreshes must withdraw cached admission, while preserving the
  // query error for callers and the editor's separately retained local draft.
  return {...query, data: query.isError ? false : query.data?.enabled, wordcraftNative: !query.isError && query.data?.wordcraftNative === true, gridcraftNative: !query.isError && query.data?.gridcraftNative === true, gridcraftWorkbook: !query.isError && query.data?.gridcraftNative === true && query.data?.gridcraftWorkbook === true, wordcraftCommands: !query.isError && query.data?.wordcraftCommands === true, wordcraftSDKPrefix: query.isError ? undefined : query.data?.wordcraftSDKPrefix, applications: query.isError ? {} : query.data?.applications ?? {}}
}
