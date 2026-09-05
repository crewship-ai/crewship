// An agent's write has to reach a tab that is already open.
//
// `crewship issue link`, a comment, a status move, a relation, a PATCH — a
// dozen production handlers broadcast `issue.updated` on the workspace
// channel, every one of them keying the payload on the mission id. The
// issue detail subscribed to `mission.updated` and nothing else, so none of
// those arrived: our own writes refetch directly (clicking in the UI works),
// and an agent's write simply did not show up. Reload and it is there.
//
// Both subscriptions are asserted here, because they are not redundant and
// dropping either re-opens a hole:
//
//   issue.updated    every issue handler and the internal agent-facing routes
//   mission.updated  the mission ENGINE. `POST /issues/{id}/start` hands the
//                    row to `missionEngine.StartMission`, and from then on it
//                    is `broadcastMissionStatus` — not any issue handler —
//                    that says the agent finished, failed or timed out.
//
// So `mission.updated` is not stale. It is the only channel that reports the
// run, and issues are rows in `missions` like everything else the engine
// drives.

import * as React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { act, render, screen, waitFor } from "@testing-library/react"

/**
 * Records the live subscription for each event type. Last one wins, which is
 * what a re-render produces and what a dispatch would reach.
 */
const realtime = vi.hoisted(() => ({
  subs: new Map<string, (event: unknown) => void>(),
}))
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (type: string, cb: (event: unknown) => void) => {
    realtime.subs.set(type, cb)
  },
}))
vi.mock("@/hooks/use-pipelines", () => ({
  usePipelines: () => ({ pipelines: [], loading: false }),
}))
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: () => <div data-testid="tiptap" />,
}))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({
  RunActivityTimeline: () => null,
  RUN_WORK_ENTRY_TYPES: ["exec"],
}))
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}))

import { IssueDetailSurface } from "../issue-detail-surface"

function ok(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: () => Promise.resolve(body),
  } as unknown as Response
}

/**
 * A live issue whose title the server can change under the open tab — which
 * is exactly what an agent's write looks like from here.
 */
function liveFetch() {
  const server = { title: "As the reader left it" }
  const calls = { issue: 0 }

  global.fetch = vi.fn((url: string) => {
    const u = String(url)
    if (/\/api\/v1\/issues\/[A-Z]+-\d+\?/.test(u)) {
      calls.issue++
      return Promise.resolve(
        ok({
          id: "id-ENG-1",
          identifier: "ENG-1",
          title: server.title,
          description: "",
          status: "BACKLOG",
          crew_id: "crew-1",
          created_at: "2026-08-01T12:00:00Z",
          updated_at: "2026-08-01T12:00:00Z",
          labels: [],
        }),
      )
    }
    return Promise.resolve(ok([]))
  }) as unknown as typeof fetch

  return { server, calls }
}

/** Dispatches an event the way RealtimeProvider does: the full envelope. */
function emit(type: string, payload: Record<string, unknown>) {
  const cb = realtime.subs.get(type)
  if (!cb) throw new Error(`nothing is subscribed to "${type}"`)
  act(() => {
    cb({ type, payload, timestamp: new Date() })
  })
}

async function openIssue() {
  render(<IssueDetailSurface workspaceId="ws1" identifier="ENG-1" />)
  await waitFor(() =>
    expect(screen.getByText("As the reader left it")).toBeInTheDocument(),
  )
}

describe("IssueDetailSurface — an agent's write reaches the open tab", () => {
  beforeEach(() => {
    realtime.subs.clear()
    vi.restoreAllMocks()
  })
  afterEach(() => vi.restoreAllMocks())

  it("subscribes to issue.updated", async () => {
    liveFetch()
    await openIssue()
    expect(realtime.subs.has("issue.updated")).toBe(true)
  })

  it("still subscribes to mission.updated — the mission engine's channel", async () => {
    liveFetch()
    await openIssue()
    expect(realtime.subs.has("mission.updated")).toBe(true)
  })

  it("repaints when issue.updated names this issue", async () => {
    const { server } = liveFetch()
    await openIssue()

    // The agent attaches a link / renames / moves the status. Only the
    // broadcast tells this tab.
    server.title = "What the agent did"
    emit("issue.updated", { id: "id-ENG-1", identifier: "ENG-1" })

    await waitFor(() =>
      expect(screen.getByText("What the agent did")).toBeInTheDocument(),
    )
  })

  it("repaints when mission.updated names this issue", async () => {
    const { server } = liveFetch()
    await openIssue()

    server.title = "What the engine did"
    emit("mission.updated", { id: "id-ENG-1", status: "COMPLETED" })

    await waitFor(() =>
      expect(screen.getByText("What the engine did")).toBeInTheDocument(),
    )
  })

  it("ignores an event for a different issue", async () => {
    // The workspace channel carries every issue's traffic, and a busy board
    // is a lot of it. The id filter is the whole reason the payload carries
    // one — and it only works if it is read off the payload rather than off
    // the event envelope, where `id` is always undefined and the filter
    // therefore never matches anything.
    const { calls } = liveFetch()
    await openIssue()
    const before = calls.issue

    emit("issue.updated", { id: "id-ENG-9", identifier: "ENG-9" })
    await new Promise((r) => setTimeout(r, 20))
    expect(calls.issue).toBe(before)

    // ...and the one that does name it still gets through.
    emit("issue.updated", { id: "id-ENG-1", identifier: "ENG-1" })
    await waitFor(() => expect(calls.issue).toBeGreaterThan(before))
  })
})

// B11 (§14.2/§17, #2368): `issue.session.state` and `run.outcome` are new
// board/detail signals — neither existed before this PR, so nothing
// subscribed and nothing repainted on them. Per §24.1's own warning ("a
// test asserting a component SUBSCRIBES is not proof anything repaints"),
// this drives the SAME real-frame-then-DOM-assertion shape as the
// `issue.updated`/`mission.updated` tests above: the Runs card's STATUS
// pill is existing, already-rendered UI (issue-runs-card.tsx) that reads
// off `fetchSubResources`' own `runs` state — exactly what `run.outcome`
// is supposed to keep fresh without a reload.
function liveFetchWithRun(initialStatus: string) {
  const server = { title: "As the reader left it", runStatus: initialStatus }
  const calls = { issue: 0, runs: 0 }

  global.fetch = vi.fn((url: string) => {
    const u = String(url)
    if (/\/api\/v1\/issues\/[A-Z]+-\d+\?/.test(u)) {
      calls.issue++
      return Promise.resolve(
        ok({
          id: "id-ENG-1",
          identifier: "ENG-1",
          title: server.title,
          description: "",
          status: "IN_PROGRESS",
          crew_id: "crew-1",
          created_at: "2026-08-01T12:00:00Z",
          updated_at: "2026-08-01T12:00:00Z",
          labels: [],
        }),
      )
    }
    if (/\/issues\/ENG-1\/runs\?/.test(u)) {
      calls.runs++
      return Promise.resolve(
        ok([
          {
            id: "run-1",
            status: server.runStatus,
            agent_name: "Backend Dev",
            task: "Fix the thing",
            duration_ms: 0,
          },
        ]),
      )
    }
    return Promise.resolve(ok([]))
  }) as unknown as typeof fetch

  return { server, calls }
}

describe("IssueDetailSurface — run.outcome and issue.session.state repaint the Runs card", () => {
  beforeEach(() => {
    realtime.subs.clear()
    vi.restoreAllMocks()
  })
  afterEach(() => vi.restoreAllMocks())

  it("subscribes to run.outcome and issue.session.state", async () => {
    liveFetchWithRun("RUNNING")
    await openIssue()
    expect(realtime.subs.has("run.outcome")).toBe(true)
    expect(realtime.subs.has("issue.session.state")).toBe(true)
  })

  it("repaints the run's STATUS pill from Running to Done on run.outcome — a REAL frame, not a subscription check", async () => {
    const { server } = liveFetchWithRun("RUNNING")
    await openIssue()
    await waitFor(() => expect(screen.getByText("Running")).toBeInTheDocument())

    // The run finishes server-side. Only the broadcast tells this open tab.
    server.runStatus = "COMPLETED"
    emit("run.outcome", { mission_id: "id-ENG-1", assignment_id: "run-1", status: "COMPLETED", outcome: "SUCCEEDED" })

    // formatStatus (lib/format-status.ts) renders COMPLETED as "Done".
    await waitFor(() => expect(screen.getByText("Done")).toBeInTheDocument())
    expect(screen.queryByText("Running")).not.toBeInTheDocument()
  })

  it("issue.session.state also triggers a sub-resources refetch", async () => {
    const { calls } = liveFetchWithRun("RUNNING")
    await openIssue()
    const before = calls.runs

    emit("issue.session.state", { mission_id: "id-ENG-1", session_id: "s1", agent_id: "a1", state: "active" })

    await waitFor(() => expect(calls.runs).toBeGreaterThan(before))
  })

  it("ignores run.outcome for a different issue", async () => {
    const { calls } = liveFetchWithRun("RUNNING")
    await openIssue()
    const before = calls.runs

    emit("run.outcome", { mission_id: "id-ENG-9", assignment_id: "run-9", status: "COMPLETED", outcome: "SUCCEEDED" })
    await new Promise((r) => setTimeout(r, 20))
    expect(calls.runs).toBe(before)
  })
})
