// The session block says who is working on the issue and offers the verbs
// that fit that state — and no others. One row per state.

import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a>,
}))

import { IssueSessionCard } from "../issue-session-card"
import { sessionState, SESSION_STATES, type SessionState } from "../issue-session"
import type { IssueWork } from "@/hooks/use-issue-work"
import type { Mission } from "@/lib/types/mission"

const base = {
  id: "i1", identifier: "ENG-1", workspace_id: "ws", crew_id: "c1", crew_name: "Research", title: "Goal",
  status: "TODO", work_mode: "agent", work_revision: 3,
} as Mission
const agent = { delegate: { id: "a1", name: "Scout" } }
const exec = (stage: string, attempt = 0) => ({ execution: { id: "x", stage, attempt, reviewer: "Lead", note: "", workers: [] } })

function work(over: Partial<IssueWork> = {}): IssueWork {
  return {
    busy: false, error: null, dialog: null,
    openDialog: vi.fn(), closeDialog: vi.fn(), takeOver: vi.fn(async () => true), handOff: vi.fn(async () => true),
    submit: vi.fn(async () => true), setClientAcceptance: vi.fn(async () => true), retryStop: vi.fn(async () => true),
    run: vi.fn(async () => true),
    ...over,
  }
}

const all = () => true

// [state, issue, latest run outcome, headline, buttons offered]
const TABLE: [SessionState, Partial<Mission>, string | undefined, RegExp, string[]][] = [
  ["none", {}, undefined, /Nobody is working on this/, ["Take it myself", "Delegate…"]],
  ["delegated", { ...agent }, undefined, /Scout is delegated/, ["Take it myself", "Start work"]],
  ["active", { ...agent, status: "IN_PROGRESS" }, undefined, /Working/, ["Take over", "Stop"]],
  ["awaiting_input", { ...agent, status: "IN_PROGRESS" }, "NEEDS_HUMAN", /Awaiting input/, ["Take over", "Open request in Inbox"]],
  ["complete", { ...agent, status: "REVIEW", client_review_required: true, ...exec("accepted") }, undefined, /Ready for review/, ["Request changes", "Approve"]],
  ["error", { ...agent, status: "FAILED" }, undefined, /Needs a person/, ["Reopen to Todo", "Take over", "Retry with a note…"]],
  ["closed", { ...agent, status: "DONE", work_note: "Shipped the FAQ." }, undefined, /Accepted/, ["Reopen"]],
  ["human", { work_mode: "human", worker_user_id: "me", worker_name: "Pavel" }, undefined, /You’re working on this/, ["Hand off…", "Submit result"]],
]

describe("sessionState", () => {
  it.each(TABLE)("%s", (state, over, outcome) => {
    expect(sessionState({ ...base, ...over } as Mission, { latestOutcome: outcome })).toBe(state)
  })

  it("covers every state the block can draw", () => {
    expect(new Set(TABLE.map((r) => r[0]))).toEqual(new Set(SESSION_STATES))
  })

  it("a Lead that gave up (needs_human, still In review) needs a person, not an approval", () => {
    expect(sessionState({ ...base, ...agent, status: "REVIEW", ...exec("needs_human", 2) } as Mission)).toBe("error")
  })
})

describe("IssueSessionCard", () => {
  it.each(TABLE)("renders one block per state: %s", (state, over, outcome, headline, buttons) => {
    const issue = { ...base, ...over } as Mission
    render(<IssueSessionCard issue={issue} state={sessionState(issue, { latestOutcome: outcome })} work={work()} can={all} meId="me" />)
    const block = screen.getByRole("region", { name: "Session" })
    expect(block).toHaveAttribute("data-state", state)
    expect(block).toHaveTextContent(headline)
    const offered = [...block.querySelectorAll("button, a")].map((b) => b.textContent?.trim()).filter((t) => buttons.includes(t ?? ""))
    expect(offered).toEqual(buttons)
  })

  it("names the person who holds it when it is not you", () => {
    const issue = { ...base, work_mode: "human", worker_user_id: "u2", worker_name: "Jana" } as Mission
    render(<IssueSessionCard issue={issue} state="human" work={work()} can={all} meId="me" />)
    expect(screen.getByText("Jana is working on this")).toBeInTheDocument()
  })

  it("shows the result and says the Lead closes it when nobody has to accept", () => {
    const issue = { ...base, ...agent, status: "REVIEW", client_review_required: false, work_note: "FAQ drafted", ...exec("reviewing") } as Mission
    render(<IssueSessionCard issue={issue} state="complete" work={work()} can={all} />)
    expect(screen.getByText("FAQ drafted")).toBeInTheDocument()
    expect(screen.getByText("No client acceptance: the Lead moves it to Done.")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull()
  })

  it("Approve and Start go through the shared work actions", () => {
    const w = work()
    const review = { ...base, ...agent, status: "REVIEW", client_review_required: true } as Mission
    const { unmount } = render(<IssueSessionCard issue={review} state="complete" work={w} can={all} />)
    fireEvent.click(screen.getByRole("button", { name: "Approve" }))
    expect(w.run).toHaveBeenCalledWith("approve")
    fireEvent.click(screen.getByRole("button", { name: "Request changes" }))
    expect(w.openDialog).toHaveBeenCalledWith({ kind: "request_changes" })
    unmount()
    render(<IssueSessionCard issue={{ ...base, ...agent } as Mission} state="delegated" work={w} can={all} />)
    fireEvent.click(screen.getByRole("button", { name: "Start work" }))
    expect(w.run).toHaveBeenCalledWith("start")
  })

  it("Stop asks how: after this step, or now", () => {
    const w = work()
    render(<IssueSessionCard issue={{ ...base, ...agent, status: "IN_PROGRESS" } as Mission} state="active" work={w} can={all} />)
    fireEvent.click(screen.getByRole("button", { name: "Stop" }))
    fireEvent.click(screen.getByRole("button", { name: /Stop after this step/ }))
    expect(w.run).toHaveBeenCalledWith("stop")
    fireEvent.click(screen.getByRole("button", { name: "Stop" }))
    fireEvent.click(screen.getByRole("button", { name: /Stop now/ }))
    expect(w.run).toHaveBeenCalledWith("stop_now")
  })

  it("offers nothing to someone who may not move the work", () => {
    render(<IssueSessionCard issue={{ ...base, ...agent, status: "IN_PROGRESS" } as Mission} state="active" work={work()} can={() => false} />)
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("does not offer agent execution on an issue a person holds", () => {
    render(<IssueSessionCard issue={{ ...base, work_mode: "human", worker_name: "Jana" } as Mission} state="human" work={work()} can={all} />)
    expect(screen.queryByRole("button", { name: "Start work" })).toBeNull()
  })
})
