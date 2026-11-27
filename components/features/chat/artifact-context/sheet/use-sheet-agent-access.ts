"use client"

import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"

/** Fallback sentences for the server's closed list, used only when it sent no sentence of its own. */
const REASON_TEXT: Record<string, string> = {
  feature_off: "Letting an agent change Sheets is not turned on in this workspace.",
  agent_policy: "This agent is not allowed to change artifacts.",
  not_attached: "The agent can change only a Sheet in your own private space, from your own private chat.",
  holder_human: "Someone is editing this Sheet now.",
  engine_failed: "Sheet changes by agents are not available on this server right now.",
  consent_stale: "The Sheet changed after you allowed the agent to change it. Allow it again.",
}
const UNCHECKED = "Whether the agent may change this Sheet could not be checked. Try again in a moment."

export interface AgentAccessAdvice {
  /** Why the choice is not offered, in words; null when it may be chosen. */
  refusal: string | null
  /** The person edits this Sheet here: it is handed over when the message is sent. */
  handOver: boolean
  /** The server starts the choice on (own private chat, a Sheet uploaded there); present only when true. Never a permission. */
  defaultAllowed?: true
}

/** Reads the server's advice. It is never a permission: the server checks every agent call again. */
export function agentAccessAdvice(value: unknown, editingHere: boolean): AgentAccessAdvice {
  const v = value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null
  if (!v || typeof v.allowed !== "boolean") return { refusal: UNCHECKED, handOver: false }
  if (v.allowed) return { refusal: null, handOver: false, ...(v.default_allowed === true ? { defaultAllowed: true as const } : {}) }
  const holder = v.holder && typeof v.holder === "object" ? v.holder as Record<string, unknown> : null
  if (v.reason === "holder_human" && holder?.mine === true) {
    return editingHere ? { refusal: null, handOver: true, ...(v.default_allowed === true ? { defaultAllowed: true as const } : {}) }
      : { refusal: "You are editing this Sheet in another tab or window. Hand it back there to let the agent change it.", handOver: false }
  }
  const message = typeof v.message === "string" && v.message.trim() ? v.message.trim() : REASON_TEXT[typeof v.reason === "string" ? v.reason : ""]
  return { refusal: message ?? UNCHECKED, handOver: false }
}

/** The version the server names as the Sheet's head, for leave given without an editor open; null when it named none it can stand behind. */
export interface SeenVersion { version: string; savedAt?: string; savedBy?: string }
export function seenVersion(value: unknown): SeenVersion | null {
  const v = value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null
  if (!v || typeof v.head_version !== "string" || !/^[1-9]\d{0,17}$/.test(v.head_version)) return null
  return { version: v.head_version, ...(typeof v.saved_at === "string" ? { savedAt: v.saved_at } : {}), ...(typeof v.saved_by_name === "string" && v.saved_by_name.trim() ? { savedBy: v.saved_by_name.trim() } : {}) }
}

/** The server's advice for one Sheet in one chat, read again on focus and every few seconds while shown. */
export function useSheetAgentAccess(workspaceId: string | null | undefined, pageId: string | null, chatId: string | null | undefined, editingHere: boolean) {
  const enabled = Boolean(workspaceId && pageId && chatId)
  const query = useQuery({
    queryKey: ["sheet-agent-access", workspaceId, pageId, chatId],
    enabled,
    retry: false,
    staleTime: 0,
    refetchInterval: 15_000,
    refetchOnWindowFocus: true,
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams({ workspace_id: workspaceId!, chat_id: chatId! })
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/agent-access?${params}`, { signal, cache: "no-store", redirect: "error" })
      if (!response.ok) throw Error("agent_access_unconfirmed")
      return response.json() as Promise<unknown>
    },
  })
  if (!enabled) return { checking: false, refusal: null, handOver: false, seen: null }
  if (query.isPending) return { checking: true, refusal: null, handOver: false, seen: null }
  if (query.isError) return { checking: false, refusal: UNCHECKED, handOver: false, seen: null }
  return { checking: false, ...agentAccessAdvice(query.data, editingHere), seen: seenVersion(query.data) }
}
