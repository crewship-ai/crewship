// waitpointDecide — POSTs the explicit boolean to the workspace-scoped
// approve endpoint. The handler treats an absent `approved` field as
// false, so callers MUST send the field even on Deny. Originally
// inlined inside inbox-list.tsx; lifted here so the trace canvas can
// share the same helper.
export async function waitpointDecide(
  workspaceID: string,
  token: string,
  approved: boolean,
): Promise<{ ok: true } | { ok: false; error: string }> {
  try {
    const res = await fetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceID)}/pipelines/waitpoints/${encodeURIComponent(token)}/approve`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ approved }),
      },
    )
    if (!res.ok) {
      const body = (await res.json().catch(() => null)) as { error?: string } | null
      return { ok: false, error: body?.error ?? `Decide failed (${res.status})` }
    }
    return { ok: true }
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e) }
  }
}

// Pending waitpoint shape returned by GET
// /api/v1/workspaces/{ws}/pipelines/waitpoints. The list is
// workspace-wide; callers filter by pipeline_run_id when they want
// "waitpoints for this run".
export interface PendingWaitpoint {
  token: string
  pipeline_run_id: string
  step_id: string
  kind: string
  prompt: string
  invoking_crew_id?: string
  timeout_at: string
  created_at: string
}

export async function listPendingWaitpoints(
  workspaceID: string,
): Promise<PendingWaitpoint[]> {
  const res = await fetch(
    `/api/v1/workspaces/${encodeURIComponent(workspaceID)}/pipelines/waitpoints`,
  )
  if (!res.ok) return []
  const data: unknown = await res.json()
  return Array.isArray(data) ? (data as PendingWaitpoint[]) : []
}
