import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"

/**
 * The two task actions the orchestration detail panel can take on someone
 * else's behalf. "edit" is not here — it only opens a panel that is already
 * on screen and writes nothing.
 */
export type TaskStatusAction = "retry" | "skip"

export interface TaskRef {
  crewId: string
  missionId: string
  taskId: string
  workspaceId: string
}

/**
 * What each action writes and what it is allowed to say afterwards. Keeping
 * the status and the sentence in one table is the point: the claim can only
 * be made where the outcome of the write is known.
 */
const ACTIONS: Record<TaskStatusAction, { status: string; claimed: string; refused: string }> = {
  retry: {
    status: "PENDING",
    claimed: "Task queued for retry",
    refused: "Could not queue the task for retry",
  },
  skip: { status: "SKIPPED", claimed: "Task skipped", refused: "Could not skip the task" },
}

/**
 * setTaskStatus PATCHes one mission task and reports whether the change
 * landed, so the caller only refreshes when there is something new to read —
 * and, more to the point, so "Task queued for retry" is only ever said about
 * a task the server actually requeued.
 *
 * apiFetch resolves on 4xx/5xx, so the status has to be read explicitly here;
 * a bare `await` was the bug (#1563).
 */
export async function setTaskStatus(action: TaskStatusAction, ref: TaskRef): Promise<boolean> {
  const spec = ACTIONS[action]
  const qs = `?workspace_id=${encodeURIComponent(ref.workspaceId)}`
  const url =
    `/api/v1/crews/${encodeURIComponent(ref.crewId)}` +
    `/missions/${encodeURIComponent(ref.missionId)}` +
    `/tasks/${encodeURIComponent(ref.taskId)}${qs}`

  try {
    const res = await apiFetch(url, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ status: spec.status }),
    })
    if (!res.ok) {
      const body = (await res.json().catch(() => null)) as
        | { error?: unknown; detail?: unknown }
        | null
      // The server's own words. It is the only party that knows whether this
      // was "that task is mid-run" or "you may not write to this crew", and
      // re-deriving that here would be a weaker second copy of its rule.
      const said = [body?.error, body?.detail].find((m) => typeof m === "string" && m.trim())
      toast.error(typeof said === "string" ? said : `${spec.refused} (HTTP ${res.status})`)
      return false
    }
  } catch (e) {
    toast.error(e instanceof Error ? e.message : spec.refused)
    return false
  }

  toast.success(spec.claimed)
  return true
}
