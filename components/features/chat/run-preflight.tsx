"use client"

import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"

export interface ChatRunPreflight {
  scope: "configured_chat_agent"
  agent_id: string
  model: string
  payer: { state: string; category: string; label: string; provider?: string }
  budgets: { id: string; scope: string; window: string; mode: string; limit_usd: number; spent_usd: number; remaining_usd: number; state: string }[]
  gate: { configured_mode: string; predicted_mode: string; rule_matches: boolean; blocking: boolean; reason: string }
  estimate_usd: number | null
  estimate_state: "unknown"
  constraints: string[]
}

// Advisory configured-model snapshot. Launch and provider admission keep their
// own live authorization checks; this component never grants or reserves work.
export function RunPreflight({ chatId, workspaceId, userId, enabled }: {
  chatId: string; workspaceId: string; userId: string; enabled: boolean
}) {
  const query = useQuery({
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
  if (!enabled || (query.isError && query.error.message === "denied")) return null
  if (!query.data || query.isError) return <p role="status" className="text-xs text-muted-foreground">Current budget and gate are unavailable. Price is unknown.</p>
  const data = query.data
  const money = (value: number) => new Intl.NumberFormat(undefined, { style: "currency", currency: "USD" }).format(value)
  return <details className="shrink-0 rounded-md border px-3 py-2 text-xs" aria-label="Before work starts">
    <summary className="cursor-pointer coarse:min-h-12">Configured model · price unknown · {data.gate.blocking ? "approval predicted" : data.gate.predicted_mode === "async" && data.gate.rule_matches ? "nonblocking review predicted" : "no blocking gate predicted"}</summary>
    <div className="max-h-40 space-y-2 overflow-y-auto overscroll-contain pt-2 md:max-h-56" tabIndex={0} aria-label="Configured model budget details">
      <p>Payer: {data.payer.label || "Payer unknown"} ({data.payer.state}). Configured model: {data.model || "unknown"}.</p>
      {data.budgets.length ? <ul className="list-disc pl-4">{data.budgets.map(budget => <li key={budget.id}>{budget.scope} · {money(budget.remaining_usd)} remaining / {money(budget.limit_usd)} per {budget.window} · {budget.mode} · {budget.state}</li>)}</ul> : <p>No configured budget cap was found.</p>}
      <p>Price before the run is unknown. Gate prediction: {data.gate.rule_matches && data.gate.predicted_mode !== "none" ? `${data.gate.predicted_mode} review` : "no matching whole-run gate"}. Actual approval appears separately if the run waits.</p>
      <ul className="list-disc pl-4">{data.constraints.map(constraint => <li key={constraint}>{constraint}</li>)}</ul>
      <button type="button" className="underline coarse:min-h-12" disabled={query.isFetching} onClick={() => void query.refetch()}>Refresh budget and gate</button>
    </div>
  </details>
}
