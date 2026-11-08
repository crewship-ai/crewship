"use client"

import { useQuery } from "@tanstack/react-query"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import type { FrameAccess } from "./frame"

export type ArtifactAccessProbe = FrameAccess | "loading" | "unknown"

/**
 * Why an address the library does not list cannot open: signed out, no
 * access (403) or nothing there (404). One read of the Page's metadata, only
 * for an address the catalog did not have; it never fetches content. A
 * readable answer means the catalog is behind (a just-made artifact), not a gate.
 */
export function useArtifactAccess(workspaceId: string, reference: string | null, enabled: boolean): ArtifactAccessProbe {
  const session = useSessionSafe()
  const signedOut = session?.status === "unauthenticated"
  const probe = useQuery({
    queryKey: ["artifact-access", workspaceId, reference],
    enabled: enabled && !signedOut && Boolean(reference),
    retry: false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(reference!)}?${new URLSearchParams({ workspace_id: workspaceId })}`, { signal, cache: "no-store" })
      return response.status
    },
  })
  if (!enabled) return "ok"
  if (signedOut) return "signedout"
  if (probe.isLoading) return "loading"
  const status = probe.data
  if (status === 401) return "signedout"
  if (status === 403) return "denied"
  if (status === 404) return "missing"
  if (status !== undefined && status >= 200 && status < 300) return "loading"
  return "unknown"
}
