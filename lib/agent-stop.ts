/**
 * One client for POST /api/v1/agents/{id}/stop, shared by every Stop button.
 *
 * The route (internal/api/proxy.go, AgentStop) answers 200
 * {id, status: "STOPPED"} only after the daemon confirms the runtime ended.
 * Otherwise it refuses with 403/404, or 502 when the daemon could not be
 * reached ("runtime stop unavailable") or did not confirm termination
 * ("runtime stop not confirmed"). A 502 therefore means the agent may still
 * be running — the one thing a Stop button must never hide (#2864).
 *
 * Callers get a result, never a throw, so a failure cannot fall into an
 * empty catch again.
 */
import { apiFetch } from "@/lib/api-fetch"
import { readApiError } from "@/lib/api-error"

export type StopAgentResult = { ok: true; status: string } | { ok: false; message: string }

// Exact sentences replyError sends from AgentStop in internal/api/proxy.go.
// Matching them only picks friendlier copy: if the server wording changes,
// an unknown 502 still shows the server's sentence plus the running caveat.
const RUNTIME_STOP_NOT_CONFIRMED = "runtime stop not confirmed"
const RUNTIME_STOP_UNAVAILABLE = "runtime stop unavailable"

const MAY_STILL_RUN = "The agent may still be running."

function stopFailureCopy(status: number, message: string): string {
  if (status !== 502) return message
  switch (message) {
    case RUNTIME_STOP_NOT_CONFIRMED:
      return "The runtime didn't confirm the stop. The agent may still be running; check again in a moment."
    case RUNTIME_STOP_UNAVAILABLE:
      return `The runtime can't be reached right now. ${MAY_STILL_RUN}`
    default:
      return `${message} ${MAY_STILL_RUN}`
  }
}

export async function stopAgent(agentId: string, workspaceId: string): Promise<StopAgentResult> {
  let res: Response
  try {
    res = await apiFetch(
      `/api/v1/agents/${encodeURIComponent(agentId)}/stop?workspace_id=${encodeURIComponent(workspaceId)}`,
      { method: "POST" },
    )
  } catch {
    return { ok: false, message: `Could not reach the server to stop the agent. ${MAY_STILL_RUN}` }
  }
  if (!res.ok) {
    const message = await readApiError(res, `Stop failed (HTTP ${res.status}).`)
    return { ok: false, message: stopFailureCopy(res.status, message) }
  }
  const body = (await res.json().catch(() => null)) as { status?: unknown } | null
  return { ok: true, status: typeof body?.status === "string" ? body.status : "STOPPED" }
}
