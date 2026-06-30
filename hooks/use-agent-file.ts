"use client"

import { useAgentFetch } from "@/hooks/use-agent-fetch"
import { apiFetch } from "@/lib/api-fetch"

// useAgentFile — best-effort fetch of a single file's text content from
// an agent's output dir. Drives the inline viewer in the trace side
// panel's Files tab (artifact_path → download → OutputView).
//
// Returns the raw text (the download endpoint streams file bytes; the
// caller renders it through OutputView, which detects language by
// content). `enabled` gates the fetch until all three of agentId /
// workspaceId / path are known — flipping to a fresh path aborts the
// previous request (useAgentFetch owns the AbortController).
export function useAgentFile(
  agentId: string | null | undefined,
  workspaceId: string | null | undefined,
  path: string | null | undefined,
): { content: string | null; loading: boolean; error: unknown } {
  const enabled = Boolean(agentId && workspaceId && path)
  const { data, loading, error } = useAgentFetch<string>(
    async (signal) => {
      const url =
        `/api/v1/agents/${agentId}/files/download` +
        `?workspace_id=${encodeURIComponent(workspaceId as string)}` +
        `&path=${encodeURIComponent(path as string)}`
      const res = await apiFetch(url, { signal })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      return res.text()
    },
    [agentId, workspaceId, path],
    { enabled, logLabel: "useAgentFile" },
  )
  return { content: data, loading, error }
}
