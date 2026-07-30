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
const ACTIONS: Record<TaskStatusAction, { status: string; claimed: string }> = {
  retry: { status: "PENDING", claimed: "Task queued for retry" },
  skip: { status: "SKIPPED", claimed: "Task skipped" },
}

/**
 * setTaskStatus PATCHes one mission task and reports whether the change
 * landed, so the caller only refreshes when there is something new to read.
 *
 * Lifted out of orchestration-layout's handleTaskAction, which inlined the
 * same URL and the same PATCH twice. Nothing else changes here.
 */
export async function setTaskStatus(action: TaskStatusAction, ref: TaskRef): Promise<boolean> {
  const spec = ACTIONS[action]
  const qs = `?workspace_id=${encodeURIComponent(ref.workspaceId)}`
  const url =
    `/api/v1/crews/${encodeURIComponent(ref.crewId)}` +
    `/missions/${encodeURIComponent(ref.missionId)}` +
    `/tasks/${encodeURIComponent(ref.taskId)}${qs}`

  await apiFetch(url, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ status: spec.status }),
  })
  toast.success(spec.claimed)
  return true
}
