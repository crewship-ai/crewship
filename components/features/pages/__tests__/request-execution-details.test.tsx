import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { RequestExecutionDetails } from "../request-execution-details"
import { ArtifactRequestStatus } from "../artifact-requests-panel"
import type { ArtifactAgentRequest } from "@/hooks/use-artifact-requests"

afterEach(cleanup)
const request: ArtifactAgentRequest = {
  id: "job", chat_id: "chat", agent_id: "agent", instruction: "Update report",
  base_rev: 1, status: "running", created_at: "2026-10-13T12:00:00Z",
  updated_at: "2026-10-13T12:01:00Z",
}

it("shows observer loss without inventing a failure or heartbeat from the last update", () => {
  const lost = { ...request, execution_state: "awaiting_confirmation" as const }
  render(<><ArtifactRequestStatus request={lost} /><RequestExecutionDetails request={lost} /></>)
  expect(screen.getByText("Awaiting run confirmation")).toBeTruthy()
  expect(screen.getByText(/agent may still be running/)).toBeTruthy()
  expect(screen.queryByText("Failed")).toBeNull()
  expect(screen.queryByText(/Last run heartbeat/)).toBeNull()
  expect(document.querySelector("time")).toBeNull()
})

it("distinguishes cancellation of a request from confirmed termination of a run", () => {
  const view = render(<RequestExecutionDetails request={{ ...request, status: "cancelled" }} />)
  expect(screen.getByText(/run has not been confirmed stopped/)).toBeTruthy()
  view.rerender(<RequestExecutionDetails request={{ ...request, status: "cancelled", run_status: "CANCELLED" }} />)
  expect(screen.queryByText(/has not been confirmed stopped/)).toBeNull()
  expect(screen.getByText("Confirmed run outcome: cancelled.")).toBeTruthy()
})

it("labels a legacy deadline as estimated and renders only supplied valid evidence", () => {
  render(<RequestExecutionDetails request={{ ...request, deadline_source: "legacy_derived", deadline_at: "2026-10-13T12:30:00Z", heartbeat_at: "invalid", last_activity_at: "2026-10-13T12:02:00Z" }} />)
  expect(screen.getByText(/Estimated request deadline/)).toBeTruthy()
  expect(screen.getByText(/Last recorded run activity/)).toBeTruthy()
  expect(screen.queryByText(/Last run heartbeat/)).toBeNull()
  expect(document.querySelectorAll("time")).toHaveLength(2)
})

it("retains a confirmed published result instead of replacing it with a runtime warning badge", () => {
  render(<ArtifactRequestStatus request={{ ...request, status: "succeeded", result_revision: 2, execution_state: "unknown" }} />)
  expect(screen.getByText("Revision published")).toBeTruthy()
})
