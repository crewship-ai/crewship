import { apiFetch } from "@/lib/api-fetch"

// inboxBulk — POSTs a batch state transition to /api/v1/inbox/bulk, the
// engine behind the tree-grouped inbox's "resolve all under this group"
// action. The server re-checks visibility per id and SKIPS source-managed
// kinds (waitpoint/escalation) that can't take a non-`read` state, so the
// returned counts tell the UI how much actually moved.
export interface InboxBulkResult {
  updated: number
  skipped: number
  skipped_ids: string[]
  not_found: number
  state: string
}

export async function inboxBulk(
  workspaceID: string,
  ids: string[],
  state: "unread" | "read" | "resolved",
  resolvedAction?: string,
): Promise<{ ok: true; result: InboxBulkResult } | { ok: false; error: string }> {
  try {
    const res = await apiFetch(
      `/api/v1/inbox/bulk?workspace_id=${encodeURIComponent(workspaceID)}`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids, state, resolved_action: resolvedAction }),
      },
    )
    if (!res.ok) {
      const body = (await res.json().catch(() => null)) as { error?: string } | null
      return { ok: false, error: body?.error ?? `Bulk action failed (${res.status})` }
    }
    return { ok: true, result: (await res.json()) as InboxBulkResult }
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e) }
  }
}
