"use client"

import { useAccessMe, type AccessDecision, type AccessMe } from "@/hooks/use-access-me"

const explanations: Record<string, string> = {
  workspace_visible: "Visible in this workspace",
  visible_metadata: "Metadata visible",
  role: "Allowed by your workspace role",
  missing_role: "Your workspace role does not allow this",
  missing_role_or_capability: "Your role and grants do not allow this",
  runtime_preflight_required: "Eligible; checked again when the run starts",
  non_interactive_auth: "Requires an interactive session",
  workspace_switch_off: "Secret reveal is off for this workspace",
  below_role_floor: "Requires at least a manager role",
  missing_capability: "Requires the credentials:reveal grant",
  outside_crew_scope: "Outside your crew scope",
  sealed: "SEALED secrets cannot be revealed",
  fresh_login_reason_and_audit_required: "Eligible; fresh login, reason and audit still required",
}

function decisionText(decision: AccessDecision) {
  const reason = explanations[decision.reason] ?? (decision.reason.startsWith("routine_")
    ? `Routine is ${decision.reason.slice(8).replaceAll("_", " ")}`
    : "Access is checked again when you act")
  return `${decision.state === "allowed" ? "Allowed" : decision.state === "denied" ? "Unavailable" : "Conditional"} — ${reason}`
}

export function YourAccess({ url, actions, provided }: { url?: string; actions: { key: string; label: string }[]; provided?: { access: AccessMe | null; loading: boolean; error: boolean } }) {
  const fetched = useAccessMe(provided ? undefined : url)
  const { access, loading, error } = provided ?? fetched
  return (
    <div className="space-y-1 text-xs" data-testid="your-access">
      <p className="font-medium">Your access</p>
      {loading || (!access && !error) ? <p role="status">Checking your access…</p> : null}
      {error ? <p role="alert">Your access could not be determined. Try again later.</p> : null}
      {access && actions.map(({ key, label }) => (
        <p key={key}><span className="font-medium">{label}:</span> {access.actions[key] ? decisionText(access.actions[key]) : "Could not be determined"}</p>
      ))}
    </div>
  )
}
