import { apiFetch } from "@/lib/api-fetch"
import { artifactRequest } from "../projects/client"
import type { CraftLeaseHolder, CraftLeaseRequest, CraftLeaseStatus, CraftProposal } from "../projects/types"

/**
 * Client for the native Craft lease routes (pages/{slug}/craft/..., #4374).
 * Every lease route needs a private admission token in X-Craft-Session; it
 * lives only in memory and never goes into a URL. The proposals routes are
 * still only proposed. The availability call answers false for a missing
 * route, and nothing else fires until it says enabled.
 */

const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const str = (value: unknown): value is string => typeof value === "string"
const int = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0
const GENERATION = /^(0|[1-9][0-9]{0,18})$/
const kindOf = (value: unknown) => value === "human" || value === "agent" ? value : null

const craftPath = (workspaceId: string, slug: string, rest: string) =>
  `/api/v1/pages/${encodeURIComponent(slug)}/craft${rest}?${new URLSearchParams({ workspace_id: workspaceId })}`
const admitted = (token: string) => ({ "X-Craft-Session": token })

function parseHolder(raw: unknown): CraftLeaseHolder | null {
  const kind = object(raw) ? kindOf(raw.kind) : null
  return object(raw) && kind && str(raw.actor_id) && raw.actor_id && str(raw.display_name) ? { actor_id: raw.actor_id, display_name: raw.display_name, kind } : null
}

function parseRequest(raw: unknown): CraftLeaseRequest | null {
  const holder = parseHolder(raw)
  return holder && object(raw) && str(raw.request_id) && raw.request_id ? { request_id: raw.request_id, ...holder } : null
}

export function parseCraftLease(raw: unknown): CraftLeaseStatus | null {
  if (!object(raw) || typeof raw.mine !== "boolean" || typeof raw.can_write !== "boolean") return null
  if (raw.generation !== null && !(str(raw.generation) && GENERATION.test(raw.generation))) return null
  const holder = raw.holder === null || raw.holder === undefined ? null : parseHolder(raw.holder)
  // A holder we cannot read is not "free": refusing the snapshot avoids offering an edit over someone else's lease.
  if (raw.holder && !holder) return null
  const rawRequests = Array.isArray(raw.handoff_requested_by) ? raw.handoff_requested_by : []
  const requests = rawRequests.map(parseRequest).filter((r): r is CraftLeaseRequest => r !== null)
  return {
    holder, mine: raw.mine, generation: raw.generation, can_write: raw.can_write, requests,
    ...(str(raw.expires_at) ? { expires_at: raw.expires_at } : {}),
  }
}

const STATES: CraftProposal["state"][] = ["open", "accepted", "rejected", "stale"]

export function parseCraftProposal(raw: unknown): CraftProposal | null {
  if (!object(raw) || !str(raw.id) || !raw.id || !int(raw.base_version) || !str(raw.summary) || !str(raw.created_at) || !STATES.includes(raw.state as CraftProposal["state"])) return null
  const author = parseHolder(raw.author)
  return author ? { id: raw.id, author, base_version: raw.base_version, summary: raw.summary, created_at: raw.created_at, state: raw.state as CraftProposal["state"] } : null
}

/** Any non-200 (the route is not served yet) or malformed answer means off; it is never an error the UI shows. */
export async function fetchCraftAvailability(workspaceId: string, slug: string, signal?: AbortSignal): Promise<boolean> {
  try {
    const response = await apiFetch(craftPath(workspaceId, slug, "/availability"), { method: "GET", cache: "no-store", signal })
    if (!response.ok) return false
    const body: unknown = await response.json().catch(() => null)
    return object(body) && body.enabled === true
  } catch (error) {
    if ((error as { name?: string })?.name === "AbortError") throw error
    return false
  }
}

/** Opens one admission; the token is the only proof the lease routes accept. */
export const createCraftSession = (workspaceId: string, slug: string) =>
  artifactRequest(craftPath(workspaceId, slug, "/sessions"), raw => object(raw) && str(raw.session_token) && raw.session_token ? raw.session_token : null, { method: "POST", body: {} })

export const fetchCraftLease = (workspaceId: string, slug: string, token: string, signal?: AbortSignal) =>
  artifactRequest(craftPath(workspaceId, slug, "/lease"), parseCraftLease, { signal, headers: admitted(token) })

export interface CraftHandoffRequest { request_id: string; generation: string }
export const requestCraftHandoff = (workspaceId: string, slug: string, token: string) =>
  artifactRequest<CraftHandoffRequest>(craftPath(workspaceId, slug, "/lease/request-handoff"),
    raw => object(raw) && str(raw.request_id) && str(raw.generation) && GENERATION.test(raw.generation) ? { request_id: raw.request_id, generation: raw.generation } : null,
    { method: "POST", body: {}, headers: admitted(token) })

export const fetchCraftProposals = (workspaceId: string, slug: string, signal?: AbortSignal) =>
  artifactRequest(craftPath(workspaceId, slug, "/proposals"), raw => {
    const list = Array.isArray(raw) ? raw : object(raw) && Array.isArray(raw.proposals) ? raw.proposals : null
    return list ? list.map(parseCraftProposal).filter((p): p is CraftProposal => p !== null) : null
  }, { signal })

export type CraftProposalDecision = "accept" | "reject"
export const decideCraftProposal = (workspaceId: string, slug: string, id: string, decision: CraftProposalDecision) =>
  artifactRequest(craftPath(workspaceId, slug, `/proposals/${encodeURIComponent(id)}/${decision}`), raw => parseCraftProposal(raw) ?? true, { method: "POST", body: {} })
