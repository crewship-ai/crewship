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
  detail: (ws: string, id: string) => ["provider-pools", ws, "detail", id] as const,
  accounts: (ws: string) => ["provider-pool-accounts", ws] as const,
}

/** A refused account-group request. `status` lets a page tell a conflict (412) from anything else. */
export class PoolRequestError extends Error {
  constructor(message: string, readonly status: number) {
    super(message)
    this.name = "PoolRequestError"
  }
}

/** The sentence for a stale If-Match — the group changed after it was read. */
export const POOL_CONFLICT_MESSAGE = "This group changed since you opened it. Reload it to see the latest version; your changes were not saved."

export async function poolRequest(ws: string, path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers)
  headers.set("X-Workspace-ID", ws)
  if (init.body) headers.set("Content-Type", "application/json")
  const response = await apiFetch(`/api/v1/${path}`, { ...init, headers })
  if (!response.ok) {
    const status = response.status
    if (status === 412) throw new PoolRequestError(POOL_CONFLICT_MESSAGE, status)
    if (status === 403) throw new PoolRequestError("Only workspace owners and admins can manage account groups.", status)
    if (status === 409) throw new PoolRequestError("This group name is already in use or reserved by a removed group. Choose another name.", status)
    if (status === 400) {
      const body = await response.json().catch(() => null)
      throw new PoolRequestError(typeof body?.error === "string" ? body.error.slice(0, 400) : "Check the group name, accounts and owner consent.", status)
    }
    throw new PoolRequestError(`Account group request failed (${status}). Please retry.`, status)
  }
  return response
}

export function useProviderPools(ws: string, enabled: boolean, after = "") {
  const client = useQueryClient()
  const available = enabled && !!ws
  const pools = useQuery({
    queryKey: poolKeys.list(ws, after), enabled: available,
    queryFn: async ({ signal }) => (await poolRequest(ws, `provider-logins/pools${after ? `?after=${encodeURIComponent(after)}` : ""}`, { signal })).json() as Promise<{ items: ProviderPool[]; next_cursor: string | null }>,
  })
  const accounts = useQuery({
    queryKey: poolKeys.accounts(ws), enabled: available,
    queryFn: async ({ signal }) => (await poolRequest(ws, "credentials?kind=provider_login", { signal })).json() as Promise<LoginCredential[]>,
  })
  const requireEnabled = () => { if (!available) throw new Error("Account group management is not available.") }
  return {
    pools: available && !pools.error ? pools.data : undefined, accounts: available && !accounts.error ? accounts.data : undefined,
    loading: available && (pools.isPending || accounts.isPending),
    error: available ? pools.error || accounts.error : null,
    reload: () => { requireEnabled(); void pools.refetch(); void accounts.refetch() },
    detail: async (id: string) => {
      requireEnabled()
      return (await poolRequest(ws, `provider-logins/pools/${encodeURIComponent(id)}`)).json() as Promise<ProviderPool>
    },
    /** Creates or updates a group; returns the server's view of it. */
    save: async (draft: PoolDraft, current?: ProviderPool) => {
      requireEnabled()
      const path = current ? `provider-logins/pools/${encodeURIComponent(current.id)}` : "provider-logins/pools"
      const body = current ? { name: draft.name, allow_cross_owner: draft.allow_cross_owner, members: draft.members } : draft
      const res = await poolRequest(ws, path, { method: current ? "PUT" : "POST", headers: current ? { "If-Match": `"${current.revision}"` } : {}, body: JSON.stringify(body) })
      const saved = (await res.json().catch(() => null)) as ProviderPool | null
      await client.invalidateQueries({ queryKey: poolKeys.all(ws) })
      return saved
    },
    remove: async (current: ProviderPool) => {
      requireEnabled()
      await poolRequest(ws, `provider-logins/pools/${encodeURIComponent(current.id)}`, { method: "DELETE", headers: { "If-Match": `"${current.revision}"` } })
      await client.invalidateQueries({ queryKey: poolKeys.all(ws) })
    },
  }
}

/** One account group with its members and the revision its next write must name. */
export function useProviderPool(ws: string, id: string | null, enabled: boolean) {
  const available = enabled && !!ws && !!id
  const q = useQuery({
    queryKey: poolKeys.detail(ws, id ?? ""),
    enabled: available,
    retry: false,
    queryFn: async ({ signal }) => (await poolRequest(ws, `provider-logins/pools/${encodeURIComponent(id ?? "")}`, { signal })).json() as Promise<ProviderPool>,
  })
  return {
    pool: available && !q.error ? q.data : undefined,
    loading: available && q.isPending,
    error: available ? (q.error as PoolRequestError | Error | null) : null,
    reload: () => { void q.refetch() },
  }
}
