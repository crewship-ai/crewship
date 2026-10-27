import { apiFetch } from "@/lib/api-fetch"
import { artifactRequest } from "../projects/client"
import type { CraftLeaseHolder, CraftLeaseStatus, CraftProposal } from "../projects/types"

/**
 * Client for the PROPOSED Craft lease routes over EditLease
 * (pages/{slug}/craft/...). None is served yet; the availability call
 * answers false for a missing route, and the hooks fire nothing else until it
 * says enabled.
 */

const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const str = (value: unknown): value is string => typeof value === "string"
const int = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0

const craftPath = (workspaceId: string, slug: string, rest: string) =>
  `/api/v1/pages/${encodeURIComponent(slug)}/craft${rest}?${new URLSearchParams({ workspace_id: workspaceId })}`

function parseHolder(raw: unknown): CraftLeaseHolder | null {
  return object(raw) && str(raw.actor_id) && raw.actor_id && str(raw.display_name) && (raw.kind === "human" || raw.kind === "agent")
    ? { actor_id: raw.actor_id, display_name: raw.display_name, kind: raw.kind } : null
}

export function parseCraftLease(raw: unknown): CraftLeaseStatus | null {
  if (!object(raw) || typeof raw.mine !== "boolean" || !int(raw.generation) || typeof raw.can_write !== "boolean") return null
  const holder = raw.holder === null || raw.holder === undefined ? null : parseHolder(raw.holder)
  // A holder we cannot read is not "free": refusing the snapshot avoids offering an edit over someone else's lease.
  if (raw.holder && !holder) return null
  return {
    holder, mine: raw.mine, generation: raw.generation, can_write: raw.can_write,
    ...(str(raw.expires_at) ? { expires_at: raw.expires_at } : {}),
    ...(str(raw.handoff_requested_by) || raw.handoff_requested_by === null ? { handoff_requested_by: raw.handoff_requested_by as string | null } : {}),
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

export const fetchCraftLease = (workspaceId: string, slug: string, signal?: AbortSignal) =>
  artifactRequest(craftPath(workspaceId, slug, "/lease"), parseCraftLease, { signal })

export const requestCraftHandoff = (workspaceId: string, slug: string) =>
  artifactRequest(craftPath(workspaceId, slug, "/lease/request-handoff"), raw => parseCraftLease(raw) ?? true, { method: "POST", body: {} })

export const acquireCraftLease = (workspaceId: string, slug: string) =>
  artifactRequest(craftPath(workspaceId, slug, "/lease"), parseCraftLease, { method: "POST", body: {} })

export const fetchCraftProposals = (workspaceId: string, slug: string, signal?: AbortSignal) =>
  artifactRequest(craftPath(workspaceId, slug, "/proposals"), raw => {
    const list = Array.isArray(raw) ? raw : object(raw) && Array.isArray(raw.proposals) ? raw.proposals : null
    return list ? list.map(parseCraftProposal).filter((p): p is CraftProposal => p !== null) : null
  }, { signal })

export type CraftProposalDecision = "accept" | "reject"
export const decideCraftProposal = (workspaceId: string, slug: string, id: string, decision: CraftProposalDecision) =>
  artifactRequest(craftPath(workspaceId, slug, `/proposals/${encodeURIComponent(id)}/${decision}`), raw => parseCraftProposal(raw) ?? true, { method: "POST", body: {} })
