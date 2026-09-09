"use client"

import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import type { LoginCredential } from "@/lib/credentials/provider-logins"

export interface PoolMember { credential_id: string; priority: number }
export interface ProviderPool {
  id: string; name: string; provider: string; mode: "api_key" | "subscription"
  revision: number; allow_cross_owner: boolean; member_count: number; members?: PoolMember[]
}
export interface PoolDraft {
  name: string; provider: string; mode: "api_key" | "subscription"
  allow_cross_owner: boolean; members: PoolMember[]
}
export const poolKeys = {
  all: (ws: string) => ["provider-pools", ws] as const,
  list: (ws: string, after: string) => ["provider-pools", ws, { after }] as const,
  accounts: (ws: string) => ["provider-pool-accounts", ws] as const,
}

async function request(ws: string, path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers)
  headers.set("X-Workspace-ID", ws)
  if (init.body) headers.set("Content-Type", "application/json")
  const response = await apiFetch(`/api/v1/${path}`, { ...init, headers })
  if (!response.ok) {
    if (response.status === 412) throw new Error("This group changed. Close this editor and reopen it to review the latest version. Your changes were not saved.")
    if (response.status === 403) throw new Error("Only workspace owners and admins can manage account groups.")
    if (response.status === 409) throw new Error("This group name is already in use or reserved by a removed group. Choose another name.")
    if (response.status === 400) {
      const body = await response.json().catch(() => null)
      throw new Error(typeof body?.error === "string" ? body.error.slice(0, 400) : "Check the group name, accounts and owner consent.")
    }
    throw new Error(`Account group request failed (${response.status}). Please retry.`)
  }
  return response
}

export function useProviderPools(ws: string, enabled: boolean, after = "") {
  const client = useQueryClient()
  const pools = useQuery({
    queryKey: poolKeys.list(ws, after), enabled: enabled && !!ws,
    queryFn: async ({ signal }) => (await request(ws, `provider-logins/pools${after ? `?after=${encodeURIComponent(after)}` : ""}`, { signal })).json() as Promise<{ items: ProviderPool[]; next_cursor: string | null }>,
  })
  const accounts = useQuery({
    queryKey: poolKeys.accounts(ws), enabled: enabled && !!ws,
    queryFn: async ({ signal }) => (await request(ws, "credentials?kind=provider_login", { signal })).json() as Promise<LoginCredential[]>,
  })
  const requireEnabled = () => { if (!enabled || !ws) throw new Error("Account group management is not available.") }
  return {
    pools: enabled && !pools.error ? pools.data : undefined, accounts: enabled && !accounts.error ? accounts.data : undefined,
    loading: enabled && (pools.isPending || accounts.isPending),
    error: enabled ? pools.error || accounts.error : null,
    reload: () => { requireEnabled(); void pools.refetch(); void accounts.refetch() },
    detail: async (id: string) => {
      requireEnabled()
      return (await request(ws, `provider-logins/pools/${encodeURIComponent(id)}`)).json() as Promise<ProviderPool>
    },
    save: async (draft: PoolDraft, current?: ProviderPool) => {
      requireEnabled()
      const path = current ? `provider-logins/pools/${encodeURIComponent(current.id)}` : "provider-logins/pools"
      const body = current ? { name: draft.name, allow_cross_owner: draft.allow_cross_owner, members: draft.members } : draft
      await request(ws, path, { method: current ? "PUT" : "POST", headers: current ? { "If-Match": `"${current.revision}"` } : {}, body: JSON.stringify(body) })
      await client.invalidateQueries({ queryKey: poolKeys.all(ws) })
    },
    remove: async (current: ProviderPool) => {
      requireEnabled()
      await request(ws, `provider-logins/pools/${encodeURIComponent(current.id)}`, { method: "DELETE", headers: { "If-Match": `"${current.revision}"` } })
      await client.invalidateQueries({ queryKey: poolKeys.all(ws) })
    },
  }
}
