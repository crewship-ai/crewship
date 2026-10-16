"use client"
import { useCallback, useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"

export interface BoardPermissionTarget { element_id: string; expected_rev: number }
export interface BoardPermissionIntent {
  mutation_id: string; run_id: string; elements: BoardPermissionTarget[]
  methods: ("update" | "delete")[]; purpose: string; expires_in_seconds: number
}
export interface BoardElementPermission extends Omit<BoardPermissionIntent, "mutation_id" | "expires_in_seconds"> {
  id: string; agent_id: string; agent_name: string; expires_at: string; created_at: string; used: boolean; revoked: boolean
}
export interface BoardPermissionRun { run_id: string; agent_id: string; agent_name: string; expires_at: string; needs_board_delegation?: boolean }
export class BoardPermissionError extends Error { constructor(message: string, public status: number) { super(message) } }

export function useBoardElementPermissions(workspace: string, slug: string, enabled: boolean) {
  const client = useQueryClient()
  const [accessError, setAccessError] = useState<BoardPermissionError | null>(null)
  const [partialDelegation, setPartialDelegation] = useState<string | null>(null)
  const key = ["board-element-permissions", workspace, slug]
  const base = `/api/v1/pages/${encodeURIComponent(slug)}/board`
  const params = new URLSearchParams({ workspace_id: workspace })
  async function request(path: string, method?: string, body?: unknown, signal?: AbortSignal) {
    const response = await apiFetch(`${base}${path}?${params}`, { method, ...(body !== undefined ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : {}), signal: signal ?? AbortSignal.timeout(20000) })
    const result = response.status === 204 ? null : await response.json().catch(() => null)
    if (!response.ok) throw new BoardPermissionError(result?.error ?? "Could not update agent editing permissions.", response.status)
    return result
  }
  const query = useQuery<{ permissions: BoardElementPermission[]; eligible_runs: BoardPermissionRun[] }>({
    queryKey: key, enabled: enabled && !accessError, gcTime: 0, retry: false, refetchInterval: 15000, refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
      const result = await request("/element-permissions", undefined, undefined, signal)
      if (!Array.isArray(result?.permissions) || !Array.isArray(result?.eligible_runs)) throw new Error("Invalid editing permissions response.")
      setAccessError(null)
      return result
    },
  })
  const grant = useMutation({ retry: false, mutationFn: async (input: { intent: BoardPermissionIntent; needsBoardDelegation: boolean }) => {
    // Both operations are explicitly disclosed in the confirmation dialog.
    // Neither creates or widens the agent's existing Page grant.
    if (input.needsBoardDelegation) { await request("/delegations", "POST", { run_id: input.intent.run_id }); setPartialDelegation(input.intent.run_id) }
    const result = await request("/element-permissions", "POST", input.intent) as BoardElementPermission
    setPartialDelegation(null)
    return result
  }, onSuccess: async () => { await client.invalidateQueries({ queryKey: key }) } })
  const revoke = useMutation({ retry: false, mutationFn: (id: string) => request(`/element-permissions/${encodeURIComponent(id)}`, "DELETE"), onSuccess: async () => { await client.invalidateQueries({ queryKey: key }) } })
  const revokeDelegation = useMutation({ retry: false, mutationFn: (run: string) => request(`/delegations/${encodeURIComponent(run)}`, "DELETE"), onSuccess: async () => { setPartialDelegation(null); grant.reset(); await client.invalidateQueries({ queryKey: key }) } })
  const denial = [query.error, grant.error, revoke.error, revokeDelegation.error].find(error => error instanceof BoardPermissionError && [401, 403, 404].includes(error.status)) as BoardPermissionError | undefined
  useEffect(() => { if (denial && !accessError) { setAccessError(denial); client.removeQueries({ queryKey: ["board-element-permissions", workspace, slug], exact: true }) } }, [denial, accessError, client, workspace, slug])
  useEffect(() => { setAccessError(null); setPartialDelegation(null) }, [workspace, slug])
  const refresh = useCallback(async () => { grant.reset(); revoke.reset(); revokeDelegation.reset(); setAccessError(null); return query.refetch() }, [grant, revoke, revokeDelegation, query])
  return { query: { ...query, data: accessError || denial ? undefined : query.data, error: accessError ?? query.error }, grant, revoke, revokeDelegation, partialDelegation, refresh, denied: Boolean(accessError || denial) }
}
