/** Runtime evidence is distinct from the durable request/result state. */
export interface RequestExecution {
  execution_state?: "observed" | "awaiting_confirmation" | "unknown" | "deadline_exceeded"
  heartbeat_at?: string | null
  last_activity_at?: string | null
  deadline_at?: string | null
  deadline_source?: "recorded" | "legacy_derived"
  run_status?: "COMPLETED" | "FAILED" | "CANCELLED" | "TIMEOUT"
}

export function requestRunningLabel(request: RequestExecution): string {
  if (request.execution_state === "awaiting_confirmation") return "Awaiting run confirmation"
  if (request.execution_state === "unknown") return "Run state unknown"
  if (request.execution_state === "deadline_exceeded") return "Request deadline elapsed"
  return "Running"
}
