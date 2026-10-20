import { apiFetch } from "@/lib/api-fetch"

/** Proposed contract FE-REQ-F3-4. Not implemented by the backend yet. */
export type ProposalStatus = "open" | "accepted" | "rejected" | "conflict" | "stale"
export interface DocumentProposal {
  id: string
  author: string
  /** Agent run that produced the proposal, when there was one. */
  runId?: string
  target: { kind: "document" } | { kind: "blocks"; blockIds: string[] }
  baseRev: number
  status: ProposalStatus
  createdAt: string
}
export interface DocumentProposalDetail extends DocumentProposal {
  baseContent: unknown
  proposedContent: unknown
}
export type ProposalDecision = { status: "done"; rev?: number } | { status: "conflict" } | { status: "denied" } | { status: "unavailable" } | { status: "unknown" } | { status: "error" }

export interface DocumentProposalClient {
  list(): Promise<DocumentProposal[]>
  read(id: string): Promise<DocumentProposalDetail>
  /** `expectedRev` is the head the reviewer was looking at; `requestId` is stable across retries of one decision. */
  accept(id: string, expectedRev: number, requestId: string): Promise<ProposalDecision>
  reject(id: string, requestId: string): Promise<ProposalDecision>
}

const STATUSES: readonly string[] = ["open", "accepted", "rejected", "conflict", "stale"]
const isObject = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)

/** The wire is snake_case (FE-REQ-F3-4); anything that does not match is unreadable, never guessed. */
function proposalFromWire(value: unknown): DocumentProposal {
  if (!isObject(value)) throw new Error("proposals unreadable")
  const { id, author, run_id: runId, target, base_rev: baseRev, status, created_at: createdAt } = value
  const ids = isObject(target) && Array.isArray(target.block_ids) ? target.block_ids : undefined
  const scope = target === "document" || (isObject(target) && target.kind === "document") ? { kind: "document" as const }
    : ids && ids.length > 0 && ids.every(item => typeof item === "string") ? { kind: "blocks" as const, blockIds: ids as string[] } : undefined
  if (typeof id !== "string" || typeof author !== "string" || !scope || !Number.isSafeInteger(baseRev) || typeof status !== "string" || !STATUSES.includes(status) || typeof createdAt !== "string"
    || (runId !== undefined && runId !== null && typeof runId !== "string")) throw new Error("proposals unreadable")
  return { id, author, ...(typeof runId === "string" ? { runId } : {}), target: scope, baseRev: baseRev as number, status: status as ProposalStatus, createdAt }
}

const statusOf = (code: number): ProposalDecision => code === 409 ? { status: "conflict" } : [401, 403, 404].includes(code) ? { status: "denied" } : code === 503 ? { status: "unavailable" } : code >= 500 ? { status: "unknown" } : { status: "error" }

export function createHttpProposalClient(workspaceId: string, pageId: string): DocumentProposalClient {
  const base = `/api/v1/pages/${encodeURIComponent(`@${pageId}`)}/document/proposals`
  const query = `?workspace_id=${encodeURIComponent(workspaceId)}`
  const get = async (path: string) => {
    const response = await apiFetch(`${base}${path}${query}`, { cache: "no-store" })
    if (!response.ok) throw new Error(`proposals ${response.status}`)
    return response.json()
  }
  const decide = async (path: string, body: Record<string, unknown>, requestId: string): Promise<ProposalDecision> => {
    try {
      const response = await apiFetch(`${base}${path}${query}`, { method: "POST", headers: { "Content-Type": "application/json", "Idempotency-Key": requestId }, body: JSON.stringify({ ...body, request_id: requestId }), cache: "no-store" })
      if (!response.ok) return statusOf(response.status)
      const value = await response.json().catch(() => null) as { rev?: unknown } | null
      return { status: "done", ...(typeof value?.rev === "number" ? { rev: value.rev } : {}) }
    } catch { return { status: "unknown" } }
  }
  return {
    list: async () => { const value = await get(""); if (!Array.isArray(value?.proposals)) throw new Error("proposals unreadable"); return value.proposals.map(proposalFromWire) },
    read: async id => {
      const value = await get(`/${encodeURIComponent(id)}`)
      if (!isObject(value) || !("base_content" in value) || !("proposed_content" in value)) throw new Error("proposals unreadable")
      return { ...proposalFromWire(value), baseContent: value.base_content, proposedContent: value.proposed_content }
    },
    accept: (id, expectedRev, requestId) => decide(`/${encodeURIComponent(id)}/accept`, { expected_rev: expectedRev }, requestId),
    reject: (id, requestId) => decide(`/${encodeURIComponent(id)}/reject`, {}, requestId),
  }
}
