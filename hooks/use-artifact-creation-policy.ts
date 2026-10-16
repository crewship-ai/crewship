"use client"

import { useEffect, useRef, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier } from "@/lib/permissions/tiers"

export interface ArtifactCreationPolicy {
  enabled: boolean
  crew_ids: string[]
  version: number
}
export interface ArtifactPolicyCrew { id: string; name: string }
export class ArtifactPolicyError extends Error {
  constructor(message: string, public status?: number) { super(message) }
}
export function policyAccessDenied(error: unknown) {
  return error instanceof ArtifactPolicyError && [401, 403, 404].includes(error.status ?? 0)
}
export function validArtifactPolicy(value: unknown): value is ArtifactCreationPolicy {
  if (!value || typeof value !== "object") return false
  const policy = value as Partial<ArtifactCreationPolicy>
  return typeof policy.enabled === "boolean" && Number.isSafeInteger(policy.version) &&
    (policy.version ?? -1) >= 0 && Array.isArray(policy.crew_ids) &&
    policy.crew_ids.every(id => typeof id === "string")
}
async function readJSON(url: string, signal: AbortSignal) {
  const response = await apiFetch(url, { signal })
  const body = await response.json().catch(() => null)
  if (!response.ok) throw new ArtifactPolicyError(body?.error ?? "Could not load artifact creation policy.", response.status)
  return { response, body }
}
async function readCrews(workspace: string, signal: AbortSignal): Promise<ArtifactPolicyCrew[]> {
  const crews: ArtifactPolicyCrew[] = []
  for (let offset = 0; offset < 5000; offset += 500) {
    const { body, response } = await readJSON(`/api/v1/crews?workspace_id=${encodeURIComponent(workspace)}&limit=500&offset=${offset}`, signal)
    const rows = Array.isArray(body) ? body : body?.data
    if (!Array.isArray(rows) || rows.some(row => typeof row?.id !== "string" || typeof row?.name !== "string")) {
      throw new ArtifactPolicyError("Could not load the crew allowlist.")
    }
    crews.push(...rows.map(row => ({ id: row.id, name: row.name })))
    const total = Number(response.headers.get("X-Total-Count"))
    if (rows.length < 500 || (total > 0 && crews.length >= total)) return crews
  }
  throw new ArtifactPolicyError("This workspace has too many crews to display here. Manage the allowlist with the CLI.")
}
interface PolicySnapshot { policy: ArtifactCreationPolicy; crews: ArtifactPolicyCrew[] }
export function useArtifactCreationPolicy(workspace: string, role: string | null) {
  const client = useQueryClient()
  const [review, setReview] = useState(0)
  const [denial, setDenial] = useState<{ identity: string; error: ArtifactPolicyError } | null>(null)
  const canManage = isAdminTier(role)
  const key = ["artifact-creation-policy", workspace, role, review] as const
  const identity = JSON.stringify(key)
  const stopped = denial?.identity === identity ? denial.error : null
  const query = useQuery<PolicySnapshot>({
    queryKey: key,
    enabled: Boolean(workspace && canManage && !stopped),
    retry: false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const controller = new AbortController()
      const abort = () => controller.abort()
      signal.addEventListener("abort", abort, { once: true })
      if (signal.aborted) controller.abort()
      const timer = setTimeout(abort, 20000)
      try {
        const results = await Promise.allSettled([
          readJSON(`/api/v1/workspaces/${encodeURIComponent(workspace)}/artifact-creation-policy?workspace_id=${encodeURIComponent(workspace)}`, controller.signal),
          readCrews(workspace, controller.signal),
        ])
        const errors = results.flatMap(result => result.status === "rejected" ? [result.reason] : [])
        if (errors.length) {
          const authError = errors.find(policyAccessDenied)
          if (authError) throw authError
          if (controller.signal.aborted && !signal.aborted) throw new ArtifactPolicyError("Could not load policy before the request timed out.")
          throw errors[0]
        }
        const policy = results[0], crews = results[1]
        if (policy.status !== "fulfilled" || crews.status !== "fulfilled" || !validArtifactPolicy(policy.value.body)) {
          throw new ArtifactPolicyError("Could not read artifact creation policy.")
        }
        return { policy: policy.value.body, crews: crews.value }
      } finally {
        clearTimeout(timer)
        signal.removeEventListener("abort", abort)
      }
    },
  })
  const readDenied = policyAccessDenied(query.error)
  const visible = canManage && !readDenied && !stopped
  const allowed = useRef(visible)
  allowed.current = visible
  useEffect(() => {
    if (readDenied) setDenial({ identity, error: query.error as ArtifactPolicyError })
    if (!canManage || readDenied) client.removeQueries({ queryKey: ["artifact-creation-policy", workspace] })
  }, [client, workspace, canManage, readDenied, identity, query.error])
  return {
    query,
    canManage,
    data: visible ? query.data : undefined,
    error: stopped ?? query.error,
    identity,
    // A new key guarantees a fresh snapshot after denial or explicit discard.
    reviewLatest: () => setReview(value => value + 1),
    saved: (policy: ArtifactCreationPolicy) => {
      if (!allowed.current) return
      client.setQueryData<PolicySnapshot>(key, previous => previous ? { ...previous, policy } : undefined)
    },
    deny: (error: ArtifactPolicyError) => {
      allowed.current = false
      setDenial({ identity, error })
      client.removeQueries({ queryKey: ["artifact-creation-policy", workspace] })
    },
  }
}
