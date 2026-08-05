// The sequencing guard.
//
// `useIssueDetail` used to own the issue's comments and carried two explicit
// request-id guards so a slow response for the issue you just navigated away
// from could not smear over the one you are looking at. That work moved into
// this component — so the guard has to move with it, or "the guards went with
// the work they were guarding" is a comfortable sentence covering a
// regression.
//
// Switching issues is cheap and fast (a click in the explorer, an arrow key on
// the board), and the responses are five parallel fetches deep, so the
// out-of-order case is the ordinary case rather than an exotic one.

import * as React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("@/hooks/use-pipelines", () => ({
  usePipelines: () => ({ pipelines: [], loading: false }),
}))
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: () => <div data-testid="tiptap" />,
}))
// Recorded rather than rendered: the timeline owns a journal fetch and a
// stream, and what this file needs from it is the props the surface asks for.
const runActivity = vi.hoisted(() => ({ props: [] as Record<string, unknown>[] }))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({
  RunActivityTimeline: (props: Record<string, unknown>) => {
    runActivity.props.push(props)
    return null
  },
  RUN_WORK_ENTRY_TYPES: ["exec"],
}))
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))

import { IssueDetailSurface } from "../issue-detail-surface"

function issueBody(identifier: string, title: string) {
  return {
    id: `id-${identifier}`,
    identifier,
    title,
    description: "",
    status: "BACKLOG",
    crew_id: "crew-1",
    created_at: "2026-08-01T12:00:00Z",
    updated_at: "2026-08-01T12:00:00Z",
    labels: [],
  }
}

/**
 * Holds each issue fetch at BOTH of its awaits.
 *
 * The headers and the body are two separate gaps a newer request can slip
 * into, and only the second one reaches the post-parse guard — so a mock that
 * resolves the body with the response can only ever exercise half the code.
 * `releaseHeaders` resolves `fetch()`, `releaseBody` resolves `.json()`.
 */
function gatedFetch() {
  const headerGates = new Map<string, () => void>()
  const bodyGates = new Map<string, () => void>()
  const commentGates = new Map<string, () => void>()

  global.fetch = vi.fn((url: string) => {
    const u = String(url)

    // Comments come from the sub-resource group, which has its own guard.
    const c = /\/issues\/([A-Z]+-\d+)\/comments\?/.exec(u)
    if (c) {
      const ident = c[1]
      return new Promise<Response>((resolve) => {
        commentGates.set(ident, () =>
          resolve({
            ok: true,
            status: 200,
            json: () =>
              Promise.resolve([
                {
                  id: `c-${ident}`,
                  mission_id: `id-${ident}`,
                  author_type: "user",
                  author_id: "u1",
                  author_name: "Reader",
                  body: `comment on ${ident}`,
                  created_at: "2026-08-01T12:00:00Z",
                  updated_at: "2026-08-01T12:00:00Z",
                },
              ]),
          } as unknown as Response),
        )
      })
    }

    const m = /\/api\/v1\/issues\/([A-Z]+-\d+)\?/.exec(u)
    if (m) {
      const ident = m[1]
      return new Promise<Response>((resolveResponse) => {
        headerGates.set(ident, () =>
          resolveResponse({
            ok: true,
            status: 200,
            json: () =>
              new Promise((resolveBody) => {
                bodyGates.set(ident, () => resolveBody(issueBody(ident, `Title of ${ident}`)))
              }),
          } as unknown as Response),
        )
      })
    }
    // sub-resources and the roster
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve([]),
    } as unknown as Response)
  }) as unknown as typeof fetch

  async function open(gates: Map<string, () => void>, ident: string) {
    await waitFor(() => expect(gates.has(ident)).toBe(true))
    gates.get(ident)!()
    // Let the awaiting continuation run.
    await new Promise((r) => setTimeout(r, 0))
  }

  /**
   * Release the body only if it was ever asked for. A request the guard has
   * already abandoned never calls `.json()`, so its body gate never appears —
   * and waiting for one that will not come is the test timing out on the
   * behaviour it is trying to assert.
   */
  async function openIfAwaited(gates: Map<string, () => void>, ident: string) {
    for (let i = 0; i < 5 && !gates.has(ident); i++) {
      await new Promise((r) => setTimeout(r, 5))
    }
    if (!gates.has(ident)) return
    gates.get(ident)!()
    await new Promise((r) => setTimeout(r, 0))
  }

  return {
    releaseHeaders: (ident: string) => open(headerGates, ident),
    releaseBody: (ident: string) => open(bodyGates, ident),
    releaseComments: (ident: string) => open(commentGates, ident),
    /** Both gaps at once, for the cases that do not care which one closed. */
    async release(ident: string) {
      await open(headerGates, ident)
      await openIfAwaited(bodyGates, ident)
    },
  }
}

describe("IssueDetailSurface — a stale response cannot smear over a newer one", () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => vi.restoreAllMocks())

  it("ignores the previous issue's response when it lands last", async () => {
    const gate = gatedFetch()
    const { rerender } = render(
      <IssueDetailSurface workspaceId="ws1" identifier="ENG-1" />,
    )

    // Navigate before ENG-1 has answered — the ordinary case on a board.
    rerender(<IssueDetailSurface workspaceId="ws1" identifier="ENG-2" />)

    // ENG-2 answers first, then the abandoned ENG-1 request finally lands.
    await gate.release("ENG-2")
    await waitFor(() => expect(screen.getByText("Title of ENG-2")).toBeInTheDocument())
    await gate.release("ENG-1")

    // A moment for the stale promise to settle and (wrongly) set state.
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.getByText("Title of ENG-2")).toBeInTheDocument()
    expect(screen.queryByText("Title of ENG-1")).toBeNull()
  })

  it("does not let a stale response end the new issue's loading state", async () => {
    const gate = gatedFetch()
    const { rerender } = render(
      <IssueDetailSurface workspaceId="ws1" identifier="ENG-1" />,
    )
    rerender(<IssueDetailSurface workspaceId="ws1" identifier="ENG-2" />)

    // Only the abandoned request answers. The skeleton has to stay: the
    // issue on screen has not loaded, and saying otherwise renders "Issue
    // not found" over an issue that exists.
    await gate.release("ENG-1")
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByText(/not found/i)).toBeNull()
    expect(screen.queryByText("Title of ENG-1")).toBeNull()

    await gate.release("ENG-2")
    await waitFor(() => expect(screen.getByText("Title of ENG-2")).toBeInTheDocument())
  })

  it("ignores a response whose BODY arrives after the reader moved on", async () => {
    // The other half of the guard. The headers can land while you are still
    // on ENG-1 and the body only after you have opened ENG-2 — a check at
    // the first await alone lets that through, and it is the longer of the
    // two gaps, so it is the likelier one to lose.
    const gate = gatedFetch()
    const { rerender } = render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-1" />)

    // ENG-1's headers arrive first, while it is still the open issue.
    await gate.releaseHeaders("ENG-1")

    // Only now does the reader move on.
    rerender(<IssueDetailSurface workspaceId="ws1" identifier="ENG-2" />)
    await gate.release("ENG-2")
    await waitFor(() => expect(screen.getByText("Title of ENG-2")).toBeInTheDocument())

    // ENG-1's body finally parses. It must not land on screen.
    await gate.releaseBody("ENG-1")
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.getByText("Title of ENG-2")).toBeInTheDocument()
    expect(screen.queryByText("Title of ENG-1")).toBeNull()
  })

  it("does not hang the previous issue's comments under this one", async () => {
    // The sub-resources are a second group with a second guard: five parallel
    // requests, fired once the crew_id resolves, and the whole group is stale
    // the moment the identifier changes. Reading ENG-1's conversation under
    // ENG-2's title is the visible shape of losing this one.
    const gate = gatedFetch()
    const { rerender } = render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-1" />)
    await gate.release("ENG-1")

    rerender(<IssueDetailSurface workspaceId="ws1" identifier="ENG-2" />)
    await gate.release("ENG-2")
    await waitFor(() => expect(screen.getByText("Title of ENG-2")).toBeInTheDocument())

    await gate.releaseComments("ENG-2")
    await waitFor(() => expect(screen.getByText("comment on ENG-2")).toBeInTheDocument())

    // ENG-1's comment group finally answers.
    await gate.releaseComments("ENG-1")
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.getByText("comment on ENG-2")).toBeInTheDocument()
    expect(screen.queryByText("comment on ENG-1")).toBeNull()
  })
})

describe("IssueDetailSurface — run activity wears the page's chrome", () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    runActivity.props.length = 0
  })
  afterEach(() => vi.restoreAllMocks())

  it("asks the timeline for the card variant", async () => {
    // Every other section of this page is a DetailCard. The timeline renders
    // bare by default because the routine panel and the activity bar want it
    // that way, so the issue detail has to ask.
    const gate = gatedFetch()
    render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-1" />)
    await gate.release("ENG-1")
    await waitFor(() => expect(screen.getByText("Title of ENG-1")).toBeInTheDocument())

    expect(runActivity.props.length).toBeGreaterThan(0)
    expect(runActivity.props.at(-1)).toMatchObject({ card: true })
  })
})
