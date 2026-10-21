// The work dialogs replace the inline forms of the old Current work card.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
const mobile = vi.hoisted(() => ({ value: false }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile.value }))
vi.mock("@/hooks/use-issue-people", () => ({ useIssuePeople: () => ({ people: [{ id: "person", name: "Petra" }], error: false }) }))

import { IssueWorkDialogs } from "../issue-work-dialogs"
import { useIssueWork, type WorkDialog } from "@/hooks/use-issue-work"
import type { Mission } from "@/lib/types/mission"

const issue = {
  id: "issue", identifier: "ENG-1", workspace_id: "ws", crew_id: "crew", title: "Goal", status: "REVIEW",
  work_mode: "agent", work_revision: 3, delegate: { id: "agent", name: "Jordan" },
  execution: { id: "x", stage: "accepted", attempt: 1, reviewer: "Lead", note: "", workers: [] },
} as unknown as Mission
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

// The "Who" field is the kit Combobox (COM-13): a button named Who that opens
// a searchable list. Type to filter, arrows move, Enter picks, Esc closes the
// list and leaves the dialog open.
const whoTrigger = () => within(dialog()).getByRole("combobox", { name: "Who" })
const whoSearch = () => screen.findByRole("combobox", { name: "Search Who" })
async function pickWho(name: RegExp | string) {
  fireEvent.click(whoTrigger())
  await whoSearch()
  fireEvent.click(within(await screen.findByRole("listbox")).getByRole("option", { name }))
}

beforeEach(() => {
  fetchMock.mockReset()
  mobile.value = false
  Element.prototype.scrollIntoView ??= vi.fn()
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver
})

describe("Hand off dialog", () => {
  it("note required, sends revision + operation_id", async () => {
    fetchMock.mockResolvedValue({ ok: true })
    render(<Harness open={{ kind: "handoff", to: "person" }} />)
    fireEvent.click(screen.getByText("open"))
    expect(within(dialog()).getByText("Revision 3 · operation id is generated")).toBeInTheDocument()
    await pickWho(/Petra/)
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
    await pickWho(/Petra/)
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
    await pickWho(/Scout/)
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

describe("Hand off dialog: the Who combobox", () => {
  it("types to filter, arrows move, Enter picks, and Escape closes only the list", async () => {
    render(<Harness open={{ kind: "handoff", to: "agent" }} />)
    fireEvent.click(screen.getByText("open"))
    expect(whoTrigger()).toHaveTextContent("Choose")
    expect(within(dialog()).queryByRole("radiogroup", { name: "Who" })).toBeNull()
    fireEvent.click(whoTrigger())
    const search = await whoSearch()
    expect(within(screen.getByRole("listbox")).getAllByRole("option")).toHaveLength(2)
    fireEvent.change(search, { target: { value: "sco" } })
    expect(within(screen.getByRole("listbox")).getAllByRole("option").map((o) => o.textContent)).toEqual(["Scout"])
    fireEvent.change(search, { target: { value: "" } })
    fireEvent.keyDown(search, { key: "ArrowDown" })
    fireEvent.keyDown(search, { key: "Enter" })
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull())
    expect(whoTrigger()).toHaveTextContent("Scout")
    // Escape inside the list closes the list, not the dialog.
    fireEvent.click(whoTrigger())
    fireEvent.keyDown(await whoSearch(), { key: "Escape" })
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull())
    expect(screen.getByRole("dialog")).toBeInTheDocument()
  })

  it("lists people when handing off to a person, and says so when nobody matches", async () => {
    render(<Harness open={{ kind: "handoff", to: "person" }} />)
    fireEvent.click(screen.getByText("open"))
    fireEvent.click(whoTrigger())
    fireEvent.change(await whoSearch(), { target: { value: "zzz" } })
    expect(screen.getByText("Nobody matches.")).toBeInTheDocument()
  })

  it.each([
    ["phone (390 px, sheet)", true],
    ["tablet (1024 px, dialog)", false],
  ])("on a %s the list opens inside the dialog and a pick fills the field", async (_name, isMobile) => {
    mobile.value = isMobile
    render(<Harness open={{ kind: "handoff", to: "agent" }} />)
    fireEvent.click(screen.getByText("open"))
    await pickWho(/Scout/)
    expect(whoTrigger()).toHaveTextContent("Scout")
    // The list never outgrows a narrow screen (kit width: min(360px, 100vw - 24px)).
    fireEvent.click(whoTrigger())
    await whoSearch()
    expect(document.querySelector('[data-slot="popover-content"]')?.className ?? "").toContain("100vw-24px")
  })
})

describe("Delegate dialog", () => {
  it("hands the work to the agent and starts it when Start work now is on", async () => {
    fetchMock.mockResolvedValue({ ok: true })
    const runWorkflow = vi.fn(async () => true)
    render(<Harness open={{ kind: "delegate" }} runWorkflow={runWorkflow} />)
    fireEvent.click(screen.getByText("open"))
    expect(within(dialog()).queryByRole("radiogroup", { name: "Hand off to" })).toBeNull()
    await pickWho(/Scout/)
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
