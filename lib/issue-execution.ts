import type { Mission } from "@/lib/types/mission"

/** Ownership survives delegation. A human owner is not an executable agent. */
export function hasIssueAgentDelegate(issue: Pick<Mission, "delegate" | "assignee_type" | "assignee_id" | "work_mode">): boolean {
  return issue.work_mode !== "human" && Boolean(issue.delegate?.id || (issue.assignee_type === "agent" && issue.assignee_id))
}

/** One current-worker projection for the board, list, search and filter. */
export function getIssueWorker(issue: Pick<Mission, "work_mode" | "worker_user_id" | "worker_name" | "delegate" | "assignee_type" | "assignee_id" | "assignee_name">) {
  if (issue.work_mode === "human") return { id: issue.worker_user_id, name: issue.worker_name, isHuman: true }
  if (issue.delegate) return { id: issue.delegate.id, name: issue.delegate.name, isHuman: false }
  return { id: issue.assignee_id, name: issue.assignee_name, isHuman: issue.assignee_type === "user" }
}
