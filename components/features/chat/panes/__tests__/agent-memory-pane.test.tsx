import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// ---------------------------------------------------------------- stubs

let workspaceId: string | null = "ws-1"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId, loading: false }),
}))

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import { AgentMemoryPane } from "../agent-memory-pane"

// ---------------------------------------------------------------- helpers

function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}
function okText(body: string) {
  return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(body) })
}
function url(args: unknown[]): string {
  return String(args[0])
}

const AGENT = {
  id: "agent-1",
  slug: "casey",
  name: "Casey",
  crew_id: "crew-1",
  memory_enabled: true,
}

function version(sha: string, bytes = 42) {
  return {
    id: `mv_${sha}`,
    sha256: sha,
    bytes,
    written_at: "2026-08-01T10:00:00Z",
    written_by: "audit-watcher",
  }
}

/** Full happy-path router. Overrides let one route be swapped per test. */
function routes(over: Record<string, () => unknown> = {}) {
  return (...args: unknown[]) => {
    const u = url(args)
    for (const [needle, fn] of Object.entries(over)) {
      if (u.includes(needle)) return fn()
    }
    if (u.includes("/api/v1/agents/agent-1/persona")) {
      return ok({ layer: "agent", from_default: false, content: "Terse. British spelling.", bytes: 24, cap_bytes: 1500 })
    }
    if (u.includes("/api/v1/agents/agent-1/peers")) {
      return ok({ peers: [{ id: "p1", user_id: "u-1", user_slug: "pavel", bytes: 120, created_at: "", updated_at: "" }] })
    }
    if (u.includes("/api/v1/memory/versions/")) {
      if (u.includes("CREW.md")) return okText("# CREW.md\n\nShip on Thursdays.")
      return okText("# AGENT.md\n\nCasey remembers the deploy runbook.")
    }
    if (u.includes("/api/v1/memory/versions")) {
      if (u.includes("AGENT.md")) return ok({ entries: [version("aaa111")] })
      if (u.includes("CREW.md")) return ok({ entries: [version("bbb222")] })
      if (u.includes("pins.md")) return ok({ entries: [] })
      return ok({ entries: [] })
    }
    if (u.includes("/api/v1/agents/agent-1")) return ok(AGENT)
    throw new Error(`unrouted ${u}`)
  }
}

function pane() {
  return render(<AgentMemoryPane agentId="agent-1" agentSlug="casey" />)
}

beforeEach(() => {
  workspaceId = "ws-1"
  apiFetch.mockReset()
})

// ---------------------------------------------------------------- tests

describe("AgentMemoryPane", () => {
  it("shows a loading state while the agent record is in flight", async () => {
    apiFetch.mockImplementation(() => new Promise(() => {}))
    pane()
    expect(await screen.findByTestId("memory-pane-loading")).toBeInTheDocument()
  })

  it("reads AGENT.md through the memory-versions audit trail and renders its latest content", async () => {
    apiFetch.mockImplementation(routes())
    pane()

    await waitFor(() =>
      expect(screen.getByText(/Casey remembers the deploy runbook/)).toBeInTheDocument(),
    )

    const listCall = apiFetch.mock.calls.find(
      (c) => url(c).includes("/api/v1/memory/versions?") && url(c).includes("AGENT.md"),
    )
    expect(listCall).toBeTruthy()
    expect(url(listCall!)).toContain(encodeURIComponent("agent:casey/AGENT.md"))

    // The crew tier keys on crew ID, which only the agent record carries.
    const crewCall = apiFetch.mock.calls.find(
      (c) => url(c).includes("/api/v1/memory/versions?") && url(c).includes("CREW.md"),
    )
    expect(url(crewCall!)).toContain(encodeURIComponent("crew:crew-1/CREW.md"))

    // Persona and peer cards are read-only here — no editor, no delete.
    expect(screen.getByText(/Terse\. British spelling\./)).toBeInTheDocument()
    expect(screen.getByText("pavel")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^edit$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^delete$/i })).not.toBeInTheDocument()
  })

  it("says a tier is empty in the audit trail rather than implying the agent knows nothing", async () => {
    apiFetch.mockImplementation(
      routes({ "AGENT.md": () => ok({ entries: [] }) }),
    )
    pane()

    const empty = await screen.findByTestId("memory-tier-empty-agent")
    expect(empty).toHaveTextContent(/audit trail/i)
    expect(empty).toHaveTextContent(/no .*version/i)
  })

  it("says why the CREW.md tier is empty rather than letting it read as a silent crew", async () => {
    // crew:<id>/CREW.md is the path the existing memory panel queries, but
    // nothing in internal/ writes it: the audit watcher only walks
    // crews/{id}/agents/{slug}/.memory, and CREW.md lives in the crew's
    // shared tree. An absence here is a missing writer, not a missing file.
    apiFetch.mockImplementation(routes({ "CREW.md": () => ok({ entries: [] }) }))
    pane()

    const empty = await screen.findByTestId("memory-tier-empty-crew")
    expect(empty).toHaveTextContent(/nothing records this path/i)
    expect(empty).toHaveTextContent(/says nothing about what the crew has written/i)
  })

  it("names the tiers no dashboard endpoint can read instead of rendering a blank box", async () => {
    apiFetch.mockImplementation(routes())
    pane()

    // daily/YYYY-MM-DD.md — rows exist, but nothing a workspace member can
    // call enumerates the paths, so the pane must not pretend the list is
    // empty.
    const daily = await screen.findByTestId("memory-tier-unreachable-daily")
    expect(daily).toHaveTextContent(/daily/i)
    expect(daily).toHaveTextContent(/cannot|no .*endpoint|not reachable/i)

    // lessons.md is never recorded to memory_versions at all.
    const lessons = screen.getByTestId("memory-tier-unreachable-lessons")
    expect(lessons).toHaveTextContent(/lessons\.md/)
    expect(lessons).toHaveTextContent(/cannot|no .*endpoint|not reachable/i)

    // Neither may present as an empty list.
    expect(daily.querySelector("ul")).toBeNull()
    expect(lessons.querySelector("ul")).toBeNull()
  })

  it("shows an error state naming the failure, and retries on demand", async () => {
    let fail = true
    apiFetch.mockImplementation((...args: unknown[]) => {
      const u = url(args)
      if (fail && u.includes("/api/v1/agents/agent-1") && !u.includes("/peers") && !u.includes("/persona")) {
        return Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) })
      }
      return routes()(...args)
    })
    pane()

    const err = await screen.findByTestId("memory-error")
    expect(err).toHaveTextContent(/500/)

    fail = false
    fireEvent.click(screen.getByRole("button", { name: /retry/i }))

    await waitFor(() =>
      expect(screen.getByText(/Casey remembers the deploy runbook/)).toBeInTheDocument(),
    )
    expect(screen.queryByTestId("memory-error")).not.toBeInTheDocument()
  })
})
