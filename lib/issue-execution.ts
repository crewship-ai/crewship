import type { Mission } from "@/lib/types/mission"

/** Ownership survives delegation. A human owner is not an executable agent. */
export function hasIssueAgentDelegate(issue: Pick<Mission, "delegate" | "assignee_type" | "assignee_id">): boolean {
  return Boolean(issue.delegate?.id || (issue.assignee_type === "agent" && issue.assignee_id))
}
