"use client"

import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"

export interface ChatRunPreflight {
  scope: "configured_chat_agent"
  agent_id: string
  model: string
  payer: { state: string; category: string; label: string; provider?: string }
  // Amounts are absent below ADMIN (#3590); scope, window, mode and state stay.
  budgets: { id: string; scope: string; window: string; mode: string; limit_usd?: number; spent_usd?: number; remaining_usd?: number; state: string }[]
  gate: { configured_mode: string; predicted_mode: string; rule_matches: boolean; blocking: boolean; reason: string }
  estimate_usd: number | null
  estimate_state: "unknown"
  constraints: string[]
}

// Advisory configured-model snapshot. Launch and provider admission keep their
// own live authorization checks; this component never grants or reserves work.
export function useChatRunPreflight({ chatId, workspaceId, userId, enabled }: {
  chatId: string; workspaceId: string; userId: string; enabled: boolean
}) {
  return useQuery({
    queryKey: ["chat-run-preflight", workspaceId, chatId, userId],
    enabled: enabled && !!chatId && !!workspaceId && !!userId,
    retry: false, gcTime: 0, staleTime: 0,
    refetchOnWindowFocus: "always", refetchOnReconnect: "always", refetchInterval: 30_000,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/run-preflight?workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
      if (!response.ok) throw new Error([401, 403, 404].includes(response.status) ? "denied" : "unavailable")
      const body = await response.json() as ChatRunPreflight
      if (body.scope !== "configured_chat_agent" || !Array.isArray(body.budgets) || !body.gate || !body.payer) throw new Error("unavailable")
      return body
    },
  })
}

/**
 * What, if anything, will hold the next run in this chat. Only two facts are
 * worth the reader's attention: the run will wait for an approval, or a hard
 * budget is used up. Everything else the snapshot carries (payer, configured
 * model, unknown price, a nonblocking review) is advisory and stays out of
 * Chat — models and budgets are managed in Crews.
 */
export function runHold(data: ChatRunPreflight): string | null {
  // A run without a payer cannot start at all; say so before the send, not after.
  if (data.payer.state === "missing") return "This agent has no model credential, so it cannot run. Add one in the agent's settings in Crews."
  if (data.gate.blocking) return "Runs in this chat wait for approval before they start."
  if (data.budgets.some((budget) => budget.mode === "hard" && budget.state === "exceeded")) {
    return "A budget this agent runs under is used up. New runs are blocked until it resets."
  }
  return null
}

export function RunPreflight({ chatId, workspaceId, userId, enabled }: {
  chatId: string; workspaceId: string; userId: string; enabled: boolean
}) {
  const query = useChatRunPreflight({ chatId, workspaceId, userId, enabled })
  // Denied, unavailable or not yet read: say nothing. The launch keeps its own
  // live checks, so silence here never lets a run through that should wait.
  if (!enabled || query.isError || !query.data) return null
  const hold = runHold(query.data)
  if (!hold) return null
  return <p role="status" data-testid="run-preflight" className="mx-2 shrink-0 rounded-md border border-warn/30 bg-warn/5 px-3 py-2 text-xs text-foreground">{hold}</p>
}
