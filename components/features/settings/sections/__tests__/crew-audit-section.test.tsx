import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { CrewAuditSection } from "../crew-audit-section"

// Characterisation tests for the settings-shell restyle (visual only —
// data fetching / behaviour must be byte-identical before and after).
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

const LOGS = [
  {
    id: "log-1",
    action: "agent.created",
    entity_type: "Agent",
    entity_id: "agent-123",
    metadata: null,
    ip_address: "10.0.0.1",
    user_agent: "crewship-cli/1.0",
    user: { id: "u1", email: "pavel@example.com", full_name: "Pavel Srba" },
    created_at: "2026-07-20T10:00:00Z",
  },
]

describe("CrewAuditSection", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })
  afterEach(() => cleanup())

  it("renders audit log rows returned from the server", async () => {
    apiFetch.mockResolvedValue(
      jsonResponse({ data: LOGS, pagination: { page: 1, limit: 50, total: 1, total_pages: 1 } }),
    )
    render(<CrewAuditSection workspaceId="ws-1" />)

    expect(await screen.findByText("Pavel Srba")).toBeTruthy()
    // The row states the action in words now; the raw verb moved into the
    // detail panel, where the machine-readable form belongs.
    expect(screen.getByText("created")).toBeTruthy()

    const [url] = apiFetch.mock.calls[0] as [string]
    expect(url).toContain("workspace_id=ws-1")
  })

  it("renders the empty state when there is no activity", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ data: [], pagination: { page: 1, limit: 50, total: 0, total_pages: 1 } }))
    render(<CrewAuditSection workspaceId="ws-1" />)

    expect(await screen.findByText(/No activity yet/i)).toBeTruthy()
  })

  it("renders an error state on a failed fetch", async () => {
    apiFetch.mockResolvedValue(jsonResponse({ error: "boom" }, 500))
    render(<CrewAuditSection workspaceId="ws-1" />)

    expect(await screen.findByText(/Failed to load audit logs \(500\)/i)).toBeTruthy()
    expect(await screen.findByRole("button", { name: /retry/i })).toBeTruthy()
  })

  it("surfaces a refresh failure after a prior successful load", async () => {
    apiFetch.mockResolvedValueOnce(
      jsonResponse({ data: LOGS, pagination: { page: 1, limit: 50, total: 1, total_pages: 1 } }),
    )
    render(<CrewAuditSection workspaceId="ws-1" />)
    await screen.findByText("Pavel Srba")

    apiFetch.mockResolvedValueOnce(jsonResponse({ error: "boom" }, 500))
    const refreshBtn = screen.getByRole("button", { name: /refresh audit log/i })
    fireEvent.click(refreshBtn)

    // A failed refresh keeps the rows you already had and says they're
    // stale. It used to replace them with a full-page error box while the
    // banner above promised the opposite, and printed the message twice.
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(/Failed to load audit logs \(500\)/i))
    expect(screen.getAllByText(/Failed to load audit logs \(500\)/i)).toHaveLength(1)
    expect(screen.getByText("Pavel Srba")).toBeTruthy()
    // The Retry button belongs to the full error box, which should be gone.
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull()
  })
})

// ── The rewrite ──────────────────────────────────────────────────────────
//
// The log read as undifferentiated churn: every row said who did what KIND of
// thing and gave eight characters of an id, never which thing; 88 rows from
// one reseed looked exactly like 88 distinct decisions; and four of the five
// category filters pointed at events nothing recorded.

function row(action: string, over: Record<string, unknown> = {}) {
  return {
    id: `log-${Math.random().toString(36).slice(2)}`,
    action,
    entity_type: "AGENT",
    entity_id: "agent-123456789",
    entity_name: null,
    metadata: null,
    ip_address: null,
    user_agent: null,
    user: { id: "u1", email: "pavel@example.com", full_name: "Pavel Srba" },
    created_at: "2026-07-20T10:00:00Z",
    ...over,
  }
}

function page(rows: unknown[]) {
  return jsonResponse({
    data: rows,
    pagination: { page: 1, limit: 50, total: rows.length, total_pages: 1 },
  })
}

describe("Audit log — a row says what it touched", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("names the entity instead of printing an id fragment", async () => {
    apiFetch.mockResolvedValue(page([row("create", { entity_name: "Riley" })]))
    render(<CrewAuditSection workspaceId="ws-1" />)

    expect(await screen.findByText("Riley")).toBeInTheDocument()
    expect(screen.queryByText(/agent-123/)).toBeNull()
  })

  it("falls back to the id when the entity has no name to give", async () => {
    apiFetch.mockResolvedValue(page([
      row("backup.create", { entity_type: "backup", entity_id: "/var/backups/x.tar", entity_name: null }),
    ]))
    render(<CrewAuditSection workspaceId="ws-1" />)

    expect(await screen.findByText(/var\/backups|x\.tar|\/var/)).toBeInTheDocument()
  })
})

describe("Audit log — repetition collapses", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("folds a run of identical events into one line", async () => {
    const burst = Array.from({ length: 12 }, (_, i) =>
      row("delete", { entity_name: `Agent ${i}`, created_at: "2026-07-20T10:00:00Z" }))
    apiFetch.mockResolvedValue(page(burst))
    render(<CrewAuditSection workspaceId="ws-1" />)

    // One summary line stands in for the burst…
    const summary = await screen.findByRole("button", { name: /12 × delete/i })
    expect(summary).toBeInTheDocument()
    // …and the individual events are not all on screen until asked for.
    expect(screen.queryByText("Agent 7")).toBeNull()

    fireEvent.click(summary)
    expect(await screen.findByText("Agent 7")).toBeInTheDocument()
  })

  it("leaves a single event alone — folding one row helps nobody", async () => {
    apiFetch.mockResolvedValue(page([row("delete", { entity_name: "Riley" })]))
    render(<CrewAuditSection workspaceId="ws-1" />)

    expect(await screen.findByText("Riley")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /1 × delete/i })).toBeNull()
  })

  it("does not fold across different actors or actions", async () => {
    apiFetch.mockResolvedValue(page([
      row("delete", { entity_name: "A" }),
      row("delete", { entity_name: "B", user: { id: "u2", email: "other@example.com", full_name: "Other" } }),
      row("create", { entity_name: "C" }),
    ]))
    render(<CrewAuditSection workspaceId="ws-1" />)

    await screen.findByText("A")
    expect(screen.queryByRole("button", { name: /× delete/i })).toBeNull()
  })
})

describe("Audit log — grouped by day", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("puts a heading between days", async () => {
    apiFetch.mockResolvedValue(page([
      row("create", { entity_name: "New", created_at: "2026-07-20T10:00:00Z" }),
      row("create", { entity_name: "Old", created_at: "2026-07-18T10:00:00Z" }),
    ]))
    render(<CrewAuditSection workspaceId="ws-1" />)

    await screen.findByText("New")
    const headings = screen.getAllByRole("heading", { level: 3 })
    expect(headings.length).toBeGreaterThanOrEqual(2)
  })
})

describe("Audit log — four trails, one page", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("asks the server for the trail you picked", async () => {
    apiFetch.mockResolvedValue(page([row("create", { entity_name: "Riley" })]))
    render(<CrewAuditSection workspaceId="ws-1" />)
    await screen.findByText("Riley")

    apiFetch.mockClear()
    fireEvent.click(screen.getByRole("button", { name: /keeper/i }))

    await waitFor(() => {
      expect(apiFetch.mock.calls.some(([url]) => String(url).includes("source=keeper"))).toBe(true)
    })
  })

  it("defaults to the workspace trail, so an existing link lands where it did", async () => {
    apiFetch.mockResolvedValue(page([row("create", { entity_name: "Riley" })]))
    render(<CrewAuditSection workspaceId="ws-1" />)
    await screen.findByText("Riley")

    const first = String(apiFetch.mock.calls[0][0])
    expect(first.includes("source=") ? first : "source=workspace").toContain("source=workspace")
  })
})

describe("Audit log — the security-relevant events stand out", () => {
  beforeEach(() => { cleanup(); apiFetch.mockReset() })

  it("marks the events that change who can reach what", async () => {
    apiFetch.mockResolvedValue(page([
      row("workspace.update", {
        entity_type: "WORKSPACE", entity_name: "Acme",
        metadata: { allow_privileged_credentials: true },
      }),
      row("member.role_change", { entity_type: "WorkspaceMember", entity_name: "a@b.c" }),
      row("create", { entity_name: "Riley" }),
    ]))
    const { container } = render(<CrewAuditSection workspaceId="ws-1" />)
    await screen.findByText("Riley")

    // Two of the three rows are security-relevant; the agent create is not.
    expect(container.querySelectorAll('[data-audit-weight="security"]')).toHaveLength(2)
  })
})
