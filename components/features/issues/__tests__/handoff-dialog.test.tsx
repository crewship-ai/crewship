// The work dialogs replace the inline forms of the old Current work card.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-issue-people", () => ({ useIssuePeople: () => ({ people: [{ id: "person", name: "Petra" }], error: false }) }))

import { IssueWorkDialogs } from "../issue-work-dialogs"
import { useIssueWork, type WorkDialog } from "@/hooks/use-issue-work"
import type { Mission } from "@/lib/types/mission"

const issue = {
  id: "issue", identifier: "ENG-1", workspace_id: "ws", crew_id: "crew", title: "Goal", status: "REVIEW",
  work_mode: "agent", work_revision: 3, delegate: { id: "agent", name: "Jordan" },
  execution: { id: "x", stage: "accepted", attempt: 1, reviewer: "Lead", note: "", workers: [] },
} as Mission
const agents = [{ id: "agent", name: "Jordan", slug: "jordan" }, { id: "agent2", name: "Scout", slug: "scout" }]

function Harness({ open, changed, runWorkflow }: { open: Exclude<WorkDialog, null>; changed?: () => Promise<void>; runWorkflow?: (a: string, c?: string) => Promise<boolean> }) {
  const work = useIssueWork({ issue, onChanged: changed ?? (async () => {}), runWorkflow: runWorkflow ?? (async () => true) })
  return (
    <>
      <button type="button" onClick={() => work.openDialog(open)}>open</button>
      <IssueWorkDialogs issue={issue} agents={agents} work={work} />
    </>
  )
}

const dialog = () => screen.getByRole("dialog")
const primary = (name: RegExp | string) => within(dialog()).getByRole("button", { name })

beforeEach(() => fetchMock.mockReset())

describe("Hand off dialog", () => {
  it("note required, sends revision + operation_id", async () => {
    fetchMock.mockResolvedValue({ ok: true })
    render(<Harness open={{ kind: "handoff", to: "person" }} />)
    fireEvent.click(screen.getByText("open"))
    expect(within(dialog()).getByText("Revision 3 · operation id is generated")).toBeInTheDocument()
    fireEvent.click(within(dialog()).getByRole("radio", { name: /Petra/ }))
    expect(primary("Hand off")).toBeDisabled()
    fireEvent.change(within(dialog()).getByLabelText(/Note \(required\)/), { target: { value: "Please verify the report" } })
    fireEvent.click(primary("Hand off"))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("/api/v1/crews/crew/issues/ENG-1/work?workspace_id=ws")
    const body = JSON.parse(init.body)
    expect(body).toMatchObject({ action: "handoff_human", target_id: "person", note: "Please verify the report", revision: 3 })
    expect(body.operation_id).toMatch(/[0-9a-f-]{36}/)
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("a refused handoff keeps the note, refreshes the page and says why", async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 409, json: async () => ({ detail: "Work changed. Refresh before transferring." }) })
    const changed = vi.fn().mockResolvedValue(undefined)
    render(<Harness open={{ kind: "handoff", to: "person" }} changed={changed} />)
    fireEvent.click(screen.getByText("open"))
    fireEvent.click(within(dialog()).getByRole("radio", { name: /Petra/ }))
    fireEvent.change(within(dialog()).getByLabelText(/Note \(required\)/), { target: { value: "Keep me" } })
    fireEvent.click(primary("Hand off"))
    await waitFor(() => expect(within(dialog()).getByRole("alert")).toHaveTextContent("Work changed. Refresh before transferring."))
    expect(changed).toHaveBeenCalledTimes(1)
    expect(within(dialog()).getByLabelText(/Note \(required\)/)).toHaveValue("Keep me")
  })

  it("a retry of the same request reuses its operation id", async () => {
    fetchMock.mockResolvedValueOnce({ ok: false, status: 503, json: async () => ({ detail: "Try again" }) }).mockResolvedValueOnce({ ok: true })
    render(<Harness open={{ kind: "handoff", to: "agent" }} />)
    fireEvent.click(screen.getByText("open"))
    fireEvent.click(within(dialog()).getByRole("radio", { name: /Scout/ }))
    fireEvent.click(within(dialog()).getByRole("switch", { name: "Start work now" }))
    fireEvent.change(within(dialog()).getByLabelText(/Note \(required\)/), { target: { value: "Same" } })
    fireEvent.click(primary("Hand off"))
    await waitFor(() => expect(within(dialog()).getByRole("alert")).toBeInTheDocument())
    fireEvent.click(primary("Hand off"))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    const ids = fetchMock.mock.calls.map((c) => JSON.parse(c[1].body).operation_id)
    expect(ids[0]).toBe(ids[1])
  })
})

describe("Delegate dialog", () => {
  it("hands the work to the agent and starts it when Start work now is on", async () => {
    fetchMock.mockResolvedValue({ ok: true })
    const runWorkflow = vi.fn(async () => true)
    render(<Harness open={{ kind: "delegate" }} runWorkflow={runWorkflow} />)
    fireEvent.click(screen.getByText("open"))
    expect(within(dialog()).queryByRole("radiogroup", { name: "Hand off to" })).toBeNull()
    fireEvent.click(within(dialog()).getByRole("radio", { name: /Scout/ }))
    fireEvent.change(within(dialog()).getByLabelText(/Note \(required\)/), { target: { value: "Draft the FAQ" } })
    fireEvent.click(primary("Delegate and start"))
    await waitFor(() => expect(runWorkflow).toHaveBeenCalledWith("start"))
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toMatchObject({ action: "handoff_agent", target_id: "agent2" })
  })
})

describe("Submit result and Request changes", () => {
  it("Submit result requires what you did and sends submit", async () => {
    fetchMock.mockResolvedValue({ ok: true })
    render(<Harness open={{ kind: "submit" }} />)
    fireEvent.click(screen.getByText("open"))
    expect(primary("Submit result")).toBeDisabled()
    fireEvent.change(within(dialog()).getByLabelText(/What you did/), { target: { value: "Token replaced" } })
    fireEvent.click(primary("Submit result"))
    await waitFor(() => expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toMatchObject({ action: "submit", note: "Token replaced", revision: 3 }))
  })

  it("Request changes says which round it is and sends the reason", async () => {
    const runWorkflow = vi.fn(async () => true)
    render(<Harness open={{ kind: "request_changes" }} runWorkflow={runWorkflow} />)
    fireEvent.click(screen.getByText("open"))
    expect(within(dialog()).getByText("Round 2 of 2")).toBeInTheDocument()
    fireEvent.change(within(dialog()).getByLabelText(/What should change/), { target: { value: "Shorter answers" } })
    fireEvent.click(primary("Send back"))
    await waitFor(() => expect(runWorkflow).toHaveBeenCalledWith("request_changes", "Shorter answers"))
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })
})
