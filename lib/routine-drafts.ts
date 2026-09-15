import { apiFetch } from "./api-fetch"

export interface RoutineDraft {
  id: string
  slug: string
  revision: number
  base_pipeline_id: string
  base_revision: number
  document: Record<string, unknown>
  updated_by?: string
  updated_at?: string
}

/** The rows GET …/pipelines/drafts returns: one per saved draft. */
export interface RoutineDraftListEntry {
  slug: string
  revision: number
  updated_at: string
}

export async function listRoutineDrafts(workspaceId: string, signal?: AbortSignal): Promise<RoutineDraftListEntry[]> {
  const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/drafts`, { signal })
  if (!res.ok) throw new Error("Could not list the saved drafts.")
  const data = await res.json().catch(() => [])
  return Array.isArray(data) ? data : []
}

export async function discardRoutineDraft(workspaceId: string, draft: Pick<RoutineDraft, "slug" | "id" | "revision">) {
  const res = await apiFetch(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(draft.slug)}/draft`,
    {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: draft.id, revision: draft.revision }),
    },
  )
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.error || "Could not discard the draft.")
  }
}

async function readDraftResponse(response: Response, fallback: string): Promise<RoutineDraft> {
  const body = await response.json().catch(() => null)
  if (!response.ok) throw new Error(body?.error || fallback)
  if (!body || typeof body.revision !== "number" || !body.document)
    throw new Error("The server did not return a draft revision.")
  return body
}

export async function loadRoutineDraft(workspaceId: string, slug: string, signal?: AbortSignal) {
  return readDraftResponse(
    await apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/draft`,
      { signal },
    ),
    "Could not load the routine draft.",
  )
}

export async function saveRoutineDraft(
  workspaceId: string,
  draft: RoutineDraft,
  document: Record<string, unknown>,
) {
  return readDraftResponse(
    await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/drafts`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...draft, document }),
    }),
    "Could not save the routine draft.",
  )
}
