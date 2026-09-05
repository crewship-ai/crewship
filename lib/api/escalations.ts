import { apiFetch } from "@/lib/api-fetch"

// escalationResolve — PATCHes the real escalation lifecycle endpoint
// (the source of truth), NOT the inbox row. Used by the inbox detail so
// an agent escalation gets a genuine approve/reject decision instead of
// silently flipping the inbox projection (which 409s for source-managed
// kinds). Mirrors the escalation-response-card's resolve call.
export async function escalationResolve(
  escalationID: string,
  action: "approve" | "reject",
  resolution: string,
  workspaceID: string,
): Promise<{ ok: true } | { ok: false; error: string; status: number }> {
  try {
    // workspace_id MUST be on the query string: the RequireWorkspace middleware
    // reads it from the URL (query/path), not the request body, and rejects with
    // 400 "workspace_id is required" without it.
    const res = await apiFetch(
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

// escalationSupplyCredential — POSTs the value an agent asked for (#2376).
// This is the ONLY route a human-typed secret takes: it lands in the vault,
// the agent is granted the credential and told its name, never the value.
// /resolve refuses resolution text on a CREDENTIAL escalation, so the two
// cannot be confused. `name`/`type` are only needed for a free-text ask that
// staged no credential.
export async function escalationSupplyCredential(
  escalationID: string,
  value: string,
  workspaceID: string,
  opts: { name?: string; type?: string; securityLevel?: number } = {},
): Promise<
  | { ok: true; credential: { id: string; name: string; handle_only: boolean; granted: boolean } | null }
  | { ok: false; error: string; status: number }
> {
  try {
    const res = await apiFetch(
      `/api/v1/escalations/${encodeURIComponent(escalationID)}/supply?workspace_id=${encodeURIComponent(workspaceID)}`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          value,
          ...(opts.name ? { name: opts.name } : {}),
          ...(opts.type ? { type: opts.type } : {}),
          ...(opts.securityLevel ? { security_level: opts.securityLevel } : {}),
        }),
      },
    )
    if (!res.ok) {
      const body = (await res.json().catch(() => null)) as { error?: string } | null
      return { ok: false, error: body?.error ?? `Supply failed (${res.status})`, status: res.status }
    }
    const body = (await res.json().catch(() => null)) as {
      credential?: { id: string; name: string; handle_only: boolean; granted: boolean }
    } | null
    return { ok: true, credential: body?.credential ?? null }
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e), status: 0 }
  }
}
