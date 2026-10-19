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
    list: async () => { const value = await get(""); if (!Array.isArray(value?.proposals)) throw new Error("proposals unreadable"); return value.proposals as DocumentProposal[] },
    read: async id => await get(`/${encodeURIComponent(id)}`) as DocumentProposalDetail,
    accept: (id, expectedRev, requestId) => decide(`/${encodeURIComponent(id)}/accept`, { expected_rev: expectedRev }, requestId),
    reject: (id, requestId) => decide(`/${encodeURIComponent(id)}/reject`, {}, requestId),
  }
}
