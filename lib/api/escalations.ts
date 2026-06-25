// escalationResolve — PATCHes the real escalation lifecycle endpoint
// (the source of truth), NOT the inbox row. Used by the inbox detail so
// an agent escalation gets a genuine approve/reject decision instead of
// silently flipping the inbox projection (which 409s for source-managed
// kinds). Mirrors the escalation-response-card's resolve call.
export async function escalationResolve(
  workspaceID: string,
  escalationID: string,
  action: "approve" | "reject",
  resolution: string,
): Promise<{ ok: true } | { ok: false; error: string; status: number }> {
  try {
    // workspace_id is required by the wsCtx (RequireWorkspace) middleware on
    // this route — it reads it from the query string (the route carries no
    // {workspaceId} path param). Omit it and the call 400s with
    // "workspace_id is required" before the handler runs.
    const res = await fetch(
      `/api/v1/escalations/${encodeURIComponent(escalationID)}/resolve?workspace_id=${encodeURIComponent(workspaceID)}`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action, resolution }),
      },
    )
    if (!res.ok) {
      const body = (await res.json().catch(() => null)) as { error?: string } | null
      return {
        ok: false,
        error: body?.error ?? `Resolve failed (${res.status})`,
        status: res.status,
      }
    }
    return { ok: true }
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e), status: 0 }
  }
}
