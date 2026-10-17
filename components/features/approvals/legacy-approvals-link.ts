/**
 * Where an old `/approvals` link lands now that approval requests are decided
 * in the Inbox.
 *
 * `/approvals` stays a route forever: notification emails and Slack "Review"
 * buttons (internal/notifyroute/links.go) and `crewship open approvals` built
 * that URL, and links that already left the building cannot be recalled.
 *
 * - default / `?status=pending` → the decisions waiting (`?attention=approvals`)
 * - `?status=decided|all`       → Inbox History
 * - `?agent_id=`                → carried along unchanged; escalation links set it
 *
 * Nothing else is forwarded: a parameter the old page never read must not
 * reach an Inbox that may read the same name differently (`?agent=` is the
 * Inbox's free-text search, not an id).
 */
export const LEGACY_APPROVALS_DEFAULT = "/inbox?attention=approvals"

export function legacyApprovalsTarget(params: Pick<URLSearchParams, "get"> | null): string {
  const status = params?.get("status") ?? ""
  const agentID = params?.get("agent_id")?.trim() ?? ""
  const out = new URLSearchParams()
  if (status === "decided" || status === "all") out.set("view", "history")
  else out.set("attention", "approvals")
  if (agentID) out.set("agent_id", agentID)
  return `/inbox?${out.toString()}`
}
