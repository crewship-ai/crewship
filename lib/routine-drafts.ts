import { apiFetch } from "./api-fetch"

export interface RoutineDraft {
  id: string
  slug: string
  revision: number
  base_pipeline_id: string
  base_revision: number
  document: Record<string, unknown>
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
