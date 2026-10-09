/**
 * One client for POST /api/v1/agents/{id}/stop, shared by every Stop button.
 *
 * The route (internal/api/proxy.go, AgentStop) answers 200
 * {id, status: "STOPPED", outcome} only after the daemon confirms the agent
 * is not running: outcome "stopped" when the stop ended a run, or
 * "already_stopped" when nothing was running (#2879). Otherwise it refuses
 * with 403/404, or 502 with a stable `code`: "runtime_unavailable" (the
 * runtime could not be read) or "stop_not_confirmed" (a process may live and
 * its end was not confirmed). A 502 therefore means the agent may still be
 * running — the one thing a Stop button must never hide (#2864).
 *
 * Callers get a result, never a throw, so a failure cannot fall into an
 * empty catch again.
 */
import { apiFetch } from "@/lib/api-fetch"
import { readApiErrorDetail } from "@/lib/api-error"

export type StopRefusalCode = "stop_not_confirmed" | "runtime_unavailable"

export type StopAgentResult =
  | { ok: true; status: string; alreadyStopped: boolean }
  | { ok: false; message: string; code?: StopRefusalCode }

// Sentences replyError sent before the route carried codes; still matched
// so a server without `code` gets the same copy.
const RUNTIME_STOP_NOT_CONFIRMED = "runtime stop not confirmed"
const RUNTIME_STOP_UNAVAILABLE = "runtime stop unavailable"

const MAY_STILL_RUN = "The agent may still be running."

function refusalCode(body: unknown, message: string): StopRefusalCode | undefined {
  const code = (body as { code?: unknown } | null)?.code
  if (code === "stop_not_confirmed" || code === "runtime_unavailable") return code
  if (message === RUNTIME_STOP_NOT_CONFIRMED) return "stop_not_confirmed"
  if (message === RUNTIME_STOP_UNAVAILABLE) return "runtime_unavailable"
  return undefined
}

function stopFailureCopy(code: StopRefusalCode | undefined, message: string): string {
  switch (code) {
    case "stop_not_confirmed":
      return "The runtime didn't confirm the stop. The agent may still be running; check again in a moment."
    case "runtime_unavailable":
      return `The runtime can't be reached right now. ${MAY_STILL_RUN}`
    default:
      return `${message} ${MAY_STILL_RUN}`
  }
}

/** Toast text for a confirmed stop. */
export function stopSuccessMessage(result: Extract<StopAgentResult, { ok: true }>): string {
  return result.alreadyStopped ? "Agent was not running" : "Agent stopped"
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
    const { message, body } = await readApiErrorDetail(res, `Stop failed (HTTP ${res.status}).`)
    if (res.status !== 502) return { ok: false, message }
    const code = refusalCode(body, message)
    return code
      ? { ok: false, code, message: stopFailureCopy(code, message) }
      : { ok: false, message: stopFailureCopy(code, message) }
  }
  const body = (await res.json().catch(() => null)) as { status?: unknown; outcome?: unknown } | null
  return {
    ok: true,
    status: typeof body?.status === "string" ? body.status : "STOPPED",
    alreadyStopped: body?.outcome === "already_stopped",
  }
}
