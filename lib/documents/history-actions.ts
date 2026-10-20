import { apiFetch } from "@/lib/api-fetch"

/** Outcome of reverting one change. Restore is a different operation with its own path (see use-document-history). */
export type HistoryActionResult =
  | { status: "done"; rev: number }
  | { status: "conflict"; blocks: string[] }
  | { status: "stale" }
  | { status: "retention" }
  | { status: "denied" }
  | { status: "unavailable" }
  /** Network failure, 5xx other than 503, or an unreadable answer: the server may or may not have acted. */
  | { status: "unknown" }
  | { status: "error" }

export interface DocumentHistoryActions {
  /** Undo only the change recorded at one checkpoint; later work stays. */
  revertChange(input: { checkpointId: string; expectedRev: number; requestId: string }): Promise<HistoryActionResult>
}

async function post(path: string, body: Record<string, unknown>, requestId: string): Promise<HistoryActionResult> {
  let response: Response
  // The same request ID is sent on every retry of one decision, so the server can recognise a repeat.
  try { response = await apiFetch(path, { method: "POST", headers: { "Content-Type": "application/json", "Idempotency-Key": requestId }, body: JSON.stringify({ ...body, request_id: requestId }), cache: "no-store" }) } catch { return { status: "unknown" } }
  const value = await response.json().catch(() => null) as Record<string, unknown> | null
  if (response.ok) return typeof value?.rev === "number" && Number.isSafeInteger(value.rev) ? { status: "done", rev: value.rev } : { status: "unknown" }
  if ([401, 403, 404].includes(response.status)) return { status: "denied" }
  if (response.status === 409) return Array.isArray(value?.conflicts) ? { status: "conflict", blocks: value.conflicts.filter((id): id is string => typeof id === "string") } : { status: "stale" }
  if (response.status === 410) return { status: "retention" }
  if (response.status === 503) return { status: "unavailable" }
  if (response.status >= 500) return { status: "unknown" }
  return { status: "error" }
}

/** Client for the proposed revert endpoint (FE-REQ-F3-2). */
export function createHttpHistoryActions(workspaceId: string, pageId: string): DocumentHistoryActions {
  const base = `/api/v1/pages/${encodeURIComponent(`@${pageId}`)}/document`
  return {
    revertChange: ({ checkpointId, expectedRev, requestId }) => post(`${base}/revert`, { workspace_id: workspaceId, change_id: checkpointId, expected_rev: expectedRev }, requestId),
  }
}
