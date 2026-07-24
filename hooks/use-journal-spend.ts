"use client"

import { useApiResource, type UseApiResourceState } from "@/hooks/use-api-resource"
import {
  spendResponseSchema,
  EMPTY_SPEND_RESPONSE,
  type SpendResponse,
  type SpendWindow,
} from "@/lib/types/journal-spend"

/**
 * Fetch GET /api/v1/journal/spend?window=&top= (#1404). Thin wrapper
 * over useApiResource, same convention as hooks/use-paymaster.ts's
 * spend hooks — 404→notConfigured, schema parse failure→empty rows.
 */
export function useJournalSpend(
  workspaceId: string | null | undefined,
  window: SpendWindow,
  top = 5,
  reloadKey = 0,
): UseApiResourceState<SpendResponse> {
  const enabled = Boolean(workspaceId)
  return useApiResource<SpendResponse>(
    enabled
      ? `/api/v1/journal/spend?workspace_id=${encodeURIComponent(workspaceId as string)}&window=${window}&top=${top}`
      : null,
    { schema: spendResponseSchema, fallback: EMPTY_SPEND_RESPONSE, resetOnDisable: true, reloadKey },
  )
}
