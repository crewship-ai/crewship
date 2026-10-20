// Work & control, with the real work actions (useIssueWork) over a stubbed
// API. Ported from the Current work panel's tests: the revision travels with
// every transfer, the owner is never rewritten, a handback waits for agents
// to stop, and a read-only viewer gets no controls.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))

import { IssueWorkCard, POLICY_LOCK_REASON } from "../issue-work-card"
import { useIssueWork } from "@/hooks/use-issue-work"
import type { Mission } from "@/lib/types/mission"

const issue = {
  id: "issue", identifier: "ENG-1", workspace_id: "ws", crew_id: "crew", crew_name: "Research", title: "Goal", status: "TODO",
  work_mode: "agent", work_revision: 3, delegate: { id: "agent", name: "Jordan" }, owner: { id: "owner", name: "Eva" },
  client_review_required: true,
} as Mission

function Harness({ issue: i, changed, can = () => true }: { issue: Mission; changed?: () => Promise<void>; can?: (a: string) => boolean }) {
  const work = useIssueWork({ issue: i, onChanged: changed ?? (async () => {}), runWorkflow: async () => true })
  return <IssueWorkCard issue={i} work={work} can={can} />
}

beforeEach(() => fetchMock.mockReset())

describe("IssueWorkCard", () => {
  it("says who holds the work and at which revision", () => {
    render(<Harness issue={issue} />)
    expect(screen.getByText("Jordan holds the work")).toBeInTheDocument()
    expect(screen.getByText(/Research crew · revision 3/)).toBeInTheDocument()
    expect(screen.getByText("Accountable owner: Eva")).toBeInTheDocument()
  })

  it("takes over with the displayed revision and preserves the accountable owner", async () => {
    fetchMock.mockResolvedValue({ ok: true })
    const changed = vi.fn().mockResolvedValue(undefined)
    render(<Harness issue={issue} changed={changed} />)
    fireEvent.click(screen.getByRole("button", { name: "Take over" }))
    await waitFor(() => expect(changed).toHaveBeenCalledTimes(1))
    const body = JSON.parse(fetchMock.mock.calls[0][1].body)
    expect(body).toMatchObject({ action: "take_over", revision: 3 })
    expect(body.operation_id).toBeTruthy()
    expect(body).not.toHaveProperty("owner_user_id")
  })

  it("client acceptance switch locked outside Todo/Backlog with reason", () => {
    const { unmount } = render(<Harness issue={issue} />)
    expect(screen.getByRole("switch", { name: "Require client acceptance" })).toBeEnabled()
    expect(screen.queryByText(POLICY_LOCK_REASON)).toBeNull()
    unmount()

    render(<Harness issue={{ ...issue, status: "IN_PROGRESS" }} />)
    const locked = screen.getByRole("switch", { name: "Require client acceptance" })
    expect(locked).toBeDisabled()
    expect(screen.getByText(POLICY_LOCK_REASON)).toBeInTheDocument()
    expect(locked).toHaveAttribute("aria-describedby", screen.getByText(POLICY_LOCK_REASON).id)
  })

  it("flips client acceptance through the review policy with the revision", async () => {
    fetchMock.mockResolvedValue({ ok: true })
    render(<Harness issue={issue} />)
    fireEvent.click(screen.getByRole("switch", { name: "Require client acceptance" }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("/api/v1/crews/crew/issues/ENG-1/review-policy?workspace_id=ws")
    expect(JSON.parse(init.body)).toEqual({ revision: 3, client_review_required: false })
  })

  it("blocks handback and submission until the previous agents have stopped", () => {
    render(<Harness issue={{ ...issue, work_mode: "human", worker_name: "Petra", work_stopping: true }} />)
    expect(screen.getByRole("button", { name: "Hand off…" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Submit result" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Retry stopping agents" })).toBeEnabled()
    expect(screen.queryByRole("button", { name: "Take over" })).not.toBeInTheDocument()
  })

  it("keeps a read-only viewer away from mutation controls", () => {
    render(<Harness issue={issue} can={() => false} />)
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
    // The policy reads as a fact, not as a control that cannot be pressed.
    expect(screen.queryByRole("switch")).toBeNull()
    expect(screen.getByText("Required")).toBeInTheDocument()
  })

  it("an issue nobody holds offers Take it myself and Delegate", () => {
    render(<Harness issue={{ ...issue, delegate: null }} />)
    expect(screen.getByText("Nobody holds the work")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Take it myself" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Delegate…" })).toBeInTheDocument()
  })
})
