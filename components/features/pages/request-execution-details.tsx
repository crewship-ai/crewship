"use client"

import type { RequestExecution } from "@/lib/pages/request-execution"

function EvidenceTime({ label, value }: { label: string; value?: string | null }) {
  if (!value || !Number.isFinite(Date.parse(value))) return null
  return <p>{label} <time dateTime={value}>{new Date(value).toLocaleString()}</time></p>
}

export function RequestExecutionDetails({ request }: { request: RequestExecution & { status: string } }) {
  const unconfirmed = !request.run_status && ["awaiting_confirmation", "unknown", "deadline_exceeded"].includes(request.execution_state ?? "")
  return <div className="space-y-1 text-xs text-muted-foreground">
    {request.agent_reported_phase && <div><p>Agent-reported phase: {request.agent_reported_phase.phase.replaceAll("_", " ")}</p>{request.agent_reported_phase.message && <p className="whitespace-pre-wrap break-words">{request.agent_reported_phase.message}</p>}<EvidenceTime label="Phase reported" value={request.agent_reported_phase.reported_at} /></div>}
    {request.retry_of_request_id && <p>New submission linked to an earlier request.</p>}
    {request.retry_warning && <p className="text-warn">{request.retry_warning}</p>}
    {unconfirmed && <p>Run status could not be confirmed. The agent may still be running; starting another request can duplicate work.</p>}
    {request.status === "cancelled" && !request.run_status && <p>This request no longer accepts a result. The agent run has not been confirmed stopped.</p>}
    {request.execution_state === "deadline_exceeded" && <p>The request deadline elapsed. Late results are no longer accepted for this request.</p>}
    {request.run_status && <p>Confirmed run outcome: {request.run_status.toLowerCase()}.</p>}
    <EvidenceTime label="Last run heartbeat" value={request.heartbeat_at} />
    <EvidenceTime label="Last recorded run activity" value={request.last_activity_at} />
    {["queued", "running"].includes(request.status) && <EvidenceTime label={request.deadline_source === "legacy_derived" ? "Estimated request deadline" : "Request deadline"} value={request.deadline_at} />}
  </div>
}
