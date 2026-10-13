"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type { RequestExecution } from "@/lib/pages/request-execution"

export interface ArtifactRequestTarget {
  chat_id: string
  title: string
  agent_id: string
  agent_name: string
  agent_slug: string
}
export interface ArtifactAgentRequest extends RequestExecution {
  id: string
  chat_id: string
  agent_id: string
  agent_name?: string
  agent_slug?: string
  thread_id?: string
  instruction: string
  base_rev: number
  status: "queued" | "running" | "succeeded" | "failed" | "cancelled"
  created_at: string
  updated_at: string
  run_id?: string
  result_revision?: number
  error?: string
}
export interface ArtifactRequestsResult {
  can_dispatch: boolean
  eligible_chats: ArtifactRequestTarget[]
  requests: ArtifactAgentRequest[]
}
export class ArtifactRequestError extends Error {
  constructor(message: string, public status: number) { super(message) }
}
export interface ArtifactRequestDraft {
  intent_id: string // Local draft identity, never sent as platform authority.
  chat_id: string
  thread_id?: string
  instruction: string
  base_rev: number
}

export function useArtifactRequests(workspace: string, slug: string) {
  const client = useQueryClient()
  const key = ["artifact-requests", workspace, slug] as const
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/artifact/requests`
  const params = new URLSearchParams({ workspace_id: workspace })
  const [accessError, setAccessError] = useState<ArtifactRequestError | null>(null)
  const identities = useRef(new Map<string, string>())
  const fingerprint = (input: ArtifactRequestDraft) => JSON.stringify({ workspace, slug, input })
  const invalidate = useCallback(() => {
    void client.invalidateQueries({ queryKey: ["artifact-requests", workspace, slug] })
  }, [client, workspace, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("assignment.updated", invalidate)
  useRealtimeEventSafe("run.completed", invalidate)
  useRealtimeEventSafe("run.failed", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const query = useQuery<ArtifactRequestsResult>({
    queryKey: key, enabled: Boolean(workspace && slug) && !accessError,
    retry: false, gcTime: 0,
    refetchInterval: state => state.state.data?.requests.some(item => ["queued", "running"].includes(item.status)) ? 4000 : 60000,
    refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`${endpoint}?${params}`, { signal })
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new ArtifactRequestError(body?.error ?? "Could not load agent requests.", response.status)
      }
      const body = await response.json()
      if (!Array.isArray(body.requests) || !Array.isArray(body.eligible_chats)) throw new Error("The server returned an invalid agent request list.")
      setAccessError(null)
      return { ...body, can_dispatch: body.can_dispatch === true } as ArtifactRequestsResult
    },
  })
  useEffect(() => {
    identities.current.clear()
    setAccessError(null)
  }, [workspace, slug])
  useEffect(() => {
    if (!accessError && query.error instanceof ArtifactRequestError && [401, 403, 404].includes(query.error.status)) {
      setAccessError(query.error)
      identities.current.clear()
      client.removeQueries({ queryKey: ["artifact-requests", workspace, slug], exact: true })
    }
    if (query.data?.can_dispatch === false) identities.current.clear()
  }, [query.error, query.data?.can_dispatch, accessError, client, workspace, slug])
  const send = useMutation({
    retry: false,
    mutationFn: async (input: ArtifactRequestDraft): Promise<{ id: string }> => {
      if (!input.chat_id || !input.instruction.trim() || new TextEncoder().encode(input.instruction).length > 4096 || input.base_rev < 1) throw new ArtifactRequestError("Choose a private chat and enter instructions within 4 KiB.", 400)
      const stamp = fingerprint(input)
      if (!identities.current.has(stamp)) {
        if (identities.current.size >= 64) throw new ArtifactRequestError("Too many unconfirmed requests. Retry an existing submission before sending another.", 400)
        identities.current.set(stamp, crypto.randomUUID())
      }
      const payload = { chat_id: input.chat_id, instruction: input.instruction, base_rev: input.base_rev, ...(input.thread_id ? { thread_id: input.thread_id } : {}) }
      let response: Response
      try {
        response = await apiFetch(`${endpoint}?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ...payload, submission_id: identities.current.get(stamp) }), signal: AbortSignal.timeout(20000) })
      } catch { throw new Error("The send could not be confirmed. Retry this submission to check its original outcome.") }
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new ArtifactRequestError(body?.error ?? "Could not send this request.", response.status)
      }
      return response.json()
    },
    onSuccess: async (_receipt, input) => {
      identities.current.delete(fingerprint(input))
      await client.invalidateQueries({ queryKey: key })
    },
    onError: async error => {
      // A target-chat refusal need not revoke Page access. Recheck the list;
      // only an authorized read can expose fresh targets or preserve the draft.
      if (error instanceof ArtifactRequestError && [401, 403, 404, 409].includes(error.status)) {
        await client.invalidateQueries({ queryKey: key })
        if (error.status === 409) await client.invalidateQueries({ queryKey: ["page-artifact", workspace, slug] })
      }
    },
  })
  const cancel = useMutation({
    retry: false,
    mutationFn: async (id: string) => {
      const response = await apiFetch(`${endpoint}/${encodeURIComponent(id)}/cancel?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}", signal: AbortSignal.timeout(20000) })
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new ArtifactRequestError(body?.error ?? "Could not cancel this request. Refresh to check its current state.", response.status)
      }
      return response.json() as Promise<{ id: string; status: string }>
    },
    onSuccess: async () => { await client.invalidateQueries({ queryKey: key }) },
    onError: async () => { await client.invalidateQueries({ queryKey: key }) },
  })
  return { query: { ...query, data: accessError ? undefined : query.data, error: accessError ?? query.error }, send, cancel }
}
