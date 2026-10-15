"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"
import { apiFetch, type ApiFetchInit } from "@/lib/api-fetch"
import { useFeedbackStore } from "@/stores/feedback-store"

export const feedbackPreferenceChoices = [
  "Next time, keep answers concise.",
  "Next time, start with the conclusion.",
] as const

export interface FeedbackPreference {
  id: string
  revision: number
  feedback_id: string
  source_revision: number
  chat_id: string
  message_id: string
  reason: string
  value: string
  status: string
  source_type: string
}
interface FeedbackRow {
  id: string
  workspace_id: string
  user_id: string
  message_id: string
  signal: string
  reason?: string
  revision: number
  memory?: { status: string; entry_id?: string }
}
export const preferenceKeys = {
  agent: (workspaceId: string, userId: string, agentId: string) => ["agent-preferences", workspaceId, { userId, agentId }] as const,
  feedback: (workspaceId: string, userId: string, messageId: string, signal: string) => ["message-feedback", workspaceId, { userId, messageId, signal }] as const,
  consent: (workspaceId: string, userId: string) => ["peer-consent", workspaceId, { userId }] as const,
}

async function request<T>(url: string, init?: ApiFetchInit): Promise<T> {
  const res = await apiFetch(url, init)
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.error || `Request failed (${res.status})`)
  }
  return res.status === 204 ? undefined as T : res.json()
}
const json = (method: string, body: unknown): ApiFetchInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })

export function useTurnPreference({ workspaceId, userId, messageId, agentId, signal, chatId, traceId }: {
  workspaceId: string; userId: string; messageId: string; agentId: string
  signal: "helpful" | "not_helpful"; chatId?: string; traceId?: string
}) {
  const client = useQueryClient()
  const [pollUntil, setPollUntil] = useState(() => Date.now() + 15_000)
  const key = preferenceKeys.feedback(workspaceId, userId, messageId, signal)
  const fetchRow = async (abort?: AbortSignal) => {
    const query = new URLSearchParams({ message_id: messageId, workspace_id: workspaceId })
    const data = await request<{ feedback: FeedbackRow[] }>(`/api/v1/feedback?${query}`, { signal: abort })
    return data.feedback.find(row => row.workspace_id === workspaceId && row.user_id === userId && row.signal === signal) ?? null
  }
  const state = useQuery({ queryKey: key, queryFn: ({ signal }) => fetchRow(signal), retry: false,
    refetchInterval: query => query.state.data?.memory?.status === "processing" && Date.now() < pollUntil ? 1000 : false,
    refetchIntervalInBackground: false,
  })
  const remember = useMutation({
    mutationFn: async (reason: string) => {
      // Share the existing per-vote serialization with thumbs and undo.
      await useFeedbackStore.getState().submit(messageId, signal, { workspaceId, chatId, traceId, reason })
      const row = await fetchRow()
      if (!row || row.reason !== reason) throw new Error("Unable to verify that your preference was saved. Please retry.")
      return row
    },
    onMutate: () => { setPollUntil(Date.now() + 15_000) },
    onSuccess: row => {
      client.setQueryData(key, row)
      void client.invalidateQueries({ queryKey: preferenceKeys.agent(workspaceId, userId, agentId) })
    },
  })
  const status = state.data?.memory?.status
  useEffect(() => {
    if (status && status !== "processing") void client.invalidateQueries({ queryKey: preferenceKeys.agent(workspaceId, userId, agentId) })
  }, [client, workspaceId, userId, agentId, status])
  return { state, remember }
}

export function useAgentPreferences(workspaceId: string, userId: string, agentId: string) {
  const client = useQueryClient()
  const [pollUntil, setPollUntil] = useState(() => Date.now() + 15_000)
  const query = new URLSearchParams({ workspace_id: workspaceId })
  const base = `/api/v1/agents/${encodeURIComponent(agentId)}/peers`
  const key = preferenceKeys.agent(workspaceId, userId, agentId)
  const consentKey = preferenceKeys.consent(workspaceId, userId)
  const enabled = !!workspaceId && !!userId && !!agentId
  const entries = useQuery({ queryKey: key, enabled, queryFn: async ({ signal }) => {
    const data = await request<{ preferences?: FeedbackPreference[] }>(`${base}?${query}`, { signal })
    return data.preferences ?? []
  }, refetchInterval: query => query.state.data?.some(entry => entry.status === "processing") && Date.now() < pollUntil ? 1000 : false, refetchIntervalInBackground: false })
  const consent = useQuery({ queryKey: consentKey, enabled, queryFn: ({ signal }) => request<{ opted_out: boolean }>(`/api/v1/users/me/peer-consent?${query}`, { signal }) })
  const refresh = async () => {
    await Promise.all([
      client.invalidateQueries({ queryKey: key }),
      client.invalidateQueries({ queryKey: consentKey }),
      client.invalidateQueries({ queryKey: ["message-feedback", workspaceId] }),
    ])
  }
  const change = useMutation({ mutationFn: async ({ entry, reason }: { entry: FeedbackPreference; reason?: string }) => {
    const path = `${base}/${encodeURIComponent(userId)}`
    if (reason) return request<void>(`${path}?${query}`, json("PATCH", { entry_id: entry.id, revision: entry.revision, reason }))
    const params = new URLSearchParams(query); params.set("entry_id", entry.id); params.set("revision", String(entry.revision))
    return request<void>(`${path}?${params}`, { method: "DELETE" })
  }, onMutate: () => { setPollUntil(Date.now() + 15_000) }, onSuccess: refresh, onError: () => { void client.invalidateQueries({ queryKey: key }) } })
  const setConsent = useMutation({ mutationFn: (optedOut: boolean) => request(`/api/v1/users/me/peer-consent?${query}`, json("PUT", { opted_out: optedOut })), onSuccess: refresh })
  return { entries, consent, change, setConsent, refresh }
}
