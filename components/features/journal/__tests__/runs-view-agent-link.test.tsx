import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => undefined,
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { RunsView } from "@/components/features/journal/runs-view"

// =============================================================================
// Two links per row in the recent-runs table — the agent name and the
// external-link glyph at the end — both pointed at /crews/agents/<agent_id>.
// The redesign deleted that route, so the whole run table's only way out to
// the agent was a 404, twice per row.
//
// /api/v1/runs already returns agent_slug alongside agent_id (the Run
// interface has carried it since the model column landed), so the repaired
// link is /crews?agent=<slug> — which is what the canvas actually reads.
// agent_slug is optional on the wire though, and an id in ?agent= is worse
// than nothing: use-crews-selection matches on slug, so an id gets cleared by
// the stale-selection watcher and the canvas comes up blank. Rows without a
// slug therefore fall back to plain /crews.
// =============================================================================

const AGENT_ID = "ag_0f1e2d3c-4b5a"

function run(overrides: Record<string, unknown> = {}) {
  return {
    id: "run_aabbccdd-1122",
    agent_id: AGENT_ID,
    status: "COMPLETED",
    trigger_type: "manual",
    started_at: "2026-08-01T10:00:00Z",
    finished_at: "2026-08-01T10:00:30Z",
    error_message: null,
    exit_code: 0,
    created_at: "2026-08-01T10:00:00Z",
    agent_name: "Casey",
    agent_slug: "casey",
    crew_name: "Quality",
    triggerer: null,
    ...overrides,
  }
}

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function mockFeeds(rows: unknown[]) {
  vi.mocked(apiFetch).mockImplementation((input: RequestInfo | URL) => {
    const url = String(input)
    // A full insights payload, not {}: runs-view reads
    // `insights?.totals.running` (line 307) — the optional chain stops at
    // `insights`, so a partial object throws rather than degrading.
    if (url.includes("/runs/insights")) {
      return Promise.resolve(
        okJSON({
          totals: { total: rows.length, succeeded: rows.length, failed: 0, running: 0 },
          duration: { p50_ms: 1000, p95_ms: 2000 },
          window: "24h",
          by_trigger: [],
          by_crew: [],
          by_model: [],
          top_agents: [],
          truncated: false,
        }),
      )
    }
    if (url.includes("status=RUNNING")) return Promise.resolve(okJSON({ data: [] }))
    return Promise.resolve(
      okJSON({
        data: rows,
        stats: { running: 0, today: rows.length, failed: 0 },
        pagination: { page: 1, limit: 20, total: rows.length, total_pages: 1 },
      }),
    )
  })
}

async function agentLinks() {
  await waitFor(() => expect(screen.getByRole("link", { name: "Casey" })).toBeInTheDocument())
  return {
    name: screen.getByRole("link", { name: "Casey" }),
    glyph: screen.getByRole("link", { name: "Open agent" }),
  }
}

describe("RunsView recent-runs agent links", () => {
  beforeEach(() => vi.mocked(apiFetch).mockReset())

  it("links both the name and the glyph to the canvas by slug", async () => {
    mockFeeds([run()])
    render(<RunsView workspaceId="ws_1" workspaceLoading={false} />)
    const { name, glyph } = await agentLinks()
    expect(name).toHaveAttribute("href", "/crews?agent=casey")
    expect(glyph).toHaveAttribute("href", "/crews?agent=casey")
  })

  it("keeps the agent id out of the href", async () => {
    mockFeeds([run()])
    render(<RunsView workspaceId="ws_1" workspaceLoading={false} />)
    const { name } = await agentLinks()
    expect(name.getAttribute("href")).not.toContain(AGENT_ID)
  })

  it("percent-encodes a slug that needs it", async () => {
    mockFeeds([run({ agent_slug: "sběrač dokladů" })])
    render(<RunsView workspaceId="ws_1" workspaceLoading={false} />)
    const { name } = await agentLinks()
    expect(name).toHaveAttribute("href", `/crews?agent=${encodeURIComponent("sběrač dokladů")}`)
  })

  it("falls back to plain /crews when the row carries no slug", async () => {
    mockFeeds([run({ agent_slug: undefined })])
    render(<RunsView workspaceId="ws_1" workspaceLoading={false} />)
    const { name, glyph } = await agentLinks()
    expect(name).toHaveAttribute("href", "/crews")
    expect(glyph).toHaveAttribute("href", "/crews")
  })
})
