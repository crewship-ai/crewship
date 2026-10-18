import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock("@/lib/toast", () => ({ toast }))

import { IntakeSurface, describeCron, describeRule } from "../intake-surface"

// ODCH-ISS-05: Intake (Triage rules, Recurring issues) on the endpoints the
// backend already has: /triage-rules, /triage/process, /recurring-issues.

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

const RULE = { id: "r1", name: "Bugs to Ops", pattern: "bug", match_type: "contains", crew_id: "c-ops", assignee_id: null, priority: "high", project_id: null, labels_json: null, position: 0, enabled: true, match_count: 3, created_at: "2026-10-01T00:00:00Z" }
const RECUR = { id: "q1", crew_id: "c-ops", crew_name: "Ops", title: "Weekly cost review", description: null, priority: "none", project_id: null, milestone_id: null, assignee_type: null, assignee_id: null, labels_json: null, cron_expression: "0 9 * * 1", enabled: true, next_run: "2026-10-19T09:00:00Z", last_run: null, run_count: 2, created_at: "2026-10-01T00:00:00Z" }
const CREWS = [{ id: "c-ops", name: "Ops" }, { id: "c-res", name: "Research" }]

let rules: unknown[]
let recurring: unknown[]
const calls = () => apiFetch.mock.calls.map((c) => [String(c[0]), (c[1] as RequestInit | undefined)?.method ?? "GET", (c[1] as RequestInit | undefined)?.body ? JSON.parse(String((c[1] as RequestInit).body)) : undefined] as const)

beforeEach(() => {
  apiFetch.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  rules = [RULE]
  recurring = [RECUR]
  apiFetch.mockImplementation(async (url = "", init?: RequestInit) => {
    const u = String(url)
    const m = init?.method ?? "GET"
    if (u.startsWith("/api/v1/triage-rules") && m === "GET") return json(rules)
    if (u.startsWith("/api/v1/recurring-issues") && m === "GET") return json(recurring)
    if (u.startsWith("/api/v1/crews")) return json(CREWS)
    if (u.startsWith("/api/v1/agents")) return json([])
    if (u.startsWith("/api/v1/triage/process")) return json({ processed: 4, matched: 2 })
    if (m === "POST") return json({ id: "new" }, 201)
    return json({})
  })
})

describe("describeRule / describeCron", () => {
  it("says the match in words", () => {
    expect(describeRule({ match_type: "contains", pattern: "bug" })).toBe("Title contains “bug”")
    expect(describeRule({ match_type: "exact", pattern: "Deploy" })).toBe("Title is “Deploy”")
    expect(describeRule({ match_type: "regex", pattern: "^fix" })).toBe("Title matches /^fix/")
  })
  it("names the common schedules and shows the rest as cron (UTC)", () => {
    expect(describeCron("0 9 * * *")).toBe("Every day at 09:00 UTC")
    expect(describeCron("0 9 * * 1")).toBe("Every Monday at 09:00 UTC")
    expect(describeCron("*/30 * * * *")).toBe("*/30 * * * * (UTC)")
  })
})

describe("IntakeSurface — Triage", () => {
  it("lists rules with what they match, what they do and how often they hit", async () => {
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    expect(await screen.findByText("Bugs to Ops")).toBeInTheDocument()
    expect(screen.getByText("Title contains “bug”")).toBeInTheDocument()
    expect(screen.getByText(/Crew Ops/)).toBeInTheDocument()
    expect(screen.getByText(/High priority/)).toBeInTheDocument()
    expect(screen.getByText("3 matches")).toBeInTheDocument()
    expect(calls()).toContainEqual(["/api/v1/triage-rules?workspace_id=ws", "GET", undefined])
  })

  it("says what a rule is when there are none", async () => {
    rules = []
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    expect(await screen.findByText(/No triage rules yet/)).toBeInTheDocument()
  })

  it("turns a rule off with one PATCH", async () => {
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("switch", { name: "Enable rule Bugs to Ops" }))
    await waitFor(() => expect(calls()).toContainEqual(["/api/v1/triage-rules/r1?workspace_id=ws", "PATCH", { enabled: false }]))
  })

  it("deletes a rule only after confirming", async () => {
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("button", { name: "Delete rule Bugs to Ops" }))
    expect(calls().some(([, m]) => m === "DELETE")).toBe(false)
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete rule" }))
    await waitFor(() => expect(calls()).toContainEqual(["/api/v1/triage-rules/r1?workspace_id=ws", "DELETE", undefined]))
  })

  it("creates a rule with only what was filled in", async () => {
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("button", { name: "New rule" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Docs" } })
    fireEvent.change(within(dialog).getByLabelText("Title pattern"), { target: { value: "docs" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Create rule" }))
    await waitFor(() => expect(calls()).toContainEqual(["/api/v1/triage-rules?workspace_id=ws", "POST", { name: "Docs", pattern: "docs", match_type: "contains" }]))
  })

  it("will not create a rule without a name and pattern", async () => {
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("button", { name: "New rule" }))
    expect(within(await screen.findByRole("dialog")).getByRole("button", { name: "Create rule" })).toBeDisabled()
  })

  it("runs the triage processor on request and reports the result", async () => {
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("button", { name: "Run triage now" }))
    await waitFor(() => expect(calls()).toContainEqual(["/api/v1/triage/process?workspace_id=ws", "POST", undefined]))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(expect.stringContaining("2 of 4")))
  })

  it("says so when the server refuses a change", async () => {
    apiFetch.mockImplementation(async (url = "", init?: RequestInit) => {
      if ((init?.method ?? "GET") === "GET") return String(url).includes("triage-rules") ? json(rules) : json([])
      return json({ detail: "forbidden" }, 403)
    })
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("switch", { name: "Enable rule Bugs to Ops" }))
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    // The switch is put back to what the server has.
    await waitFor(() => expect(screen.getByRole("switch", { name: "Enable rule Bugs to Ops" })).toBeChecked())
  })

  it("says the list could not load instead of showing an empty one", async () => {
    apiFetch.mockImplementation(async () => json({}, 500))
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    expect(await screen.findByRole("alert")).toHaveTextContent(/could not load/i)
  })
})

describe("IntakeSurface — Recurring issues", () => {
  it("lists schedules in words with the next run and how many times they ran", async () => {
    render(<IntakeSurface workspaceId="ws" tab="recurring" onTabChange={() => {}} />)
    expect(await screen.findByText("Weekly cost review")).toBeInTheDocument()
    expect(screen.getByText("Every Monday at 09:00 UTC")).toBeInTheDocument()
    expect(screen.getByText(/Ops/)).toBeInTheDocument()
    expect(screen.getByText("ran 2 times")).toBeInTheDocument()
    expect(calls()).toContainEqual(["/api/v1/recurring-issues?workspace_id=ws", "GET", undefined])
  })

  it("pauses a schedule with one PATCH", async () => {
    render(<IntakeSurface workspaceId="ws" tab="recurring" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("switch", { name: "Enable schedule Weekly cost review" }))
    await waitFor(() => expect(calls()).toContainEqual(["/api/v1/recurring-issues/q1?workspace_id=ws", "PATCH", { enabled: false }]))
  })

  it("deletes a schedule only after confirming", async () => {
    render(<IntakeSurface workspaceId="ws" tab="recurring" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("button", { name: "Delete schedule Weekly cost review" }))
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete schedule" }))
    await waitFor(() => expect(calls()).toContainEqual(["/api/v1/recurring-issues/q1?workspace_id=ws", "DELETE", undefined]))
  })

  it("creates a schedule: crew, title and cron are required", async () => {
    render(<IntakeSurface workspaceId="ws" tab="recurring" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("button", { name: "New schedule" }))
    const dialog = await screen.findByRole("dialog")
    const create = within(dialog).getByRole("button", { name: "Create schedule" })
    expect(create).toBeDisabled()
    await waitFor(() => expect(within(dialog).getByRole("option", { name: "Research" })).toBeInTheDocument())
    fireEvent.change(within(dialog).getByLabelText("Crew"), { target: { value: "c-res" } })
    fireEvent.change(within(dialog).getByLabelText("Title"), { target: { value: "Daily digest" } })
    expect(within(dialog).getByLabelText("Cron (UTC)")).toHaveValue("0 9 * * *")
    expect(create).toBeEnabled()
    fireEvent.click(create)
    await waitFor(() =>
      expect(calls()).toContainEqual(["/api/v1/recurring-issues?workspace_id=ws", "POST", { crew_id: "c-res", title: "Daily digest", cron_expression: "0 9 * * *" }]),
    )
  })

  it("shows the server's reason when the cron is refused", async () => {
    apiFetch.mockImplementation(async (url = "", init?: RequestInit) => {
      if ((init?.method ?? "GET") === "POST") return json({ detail: "invalid cron expression" }, 400)
      return String(url).includes("/crews") ? json(CREWS) : json([])
    })
    render(<IntakeSurface workspaceId="ws" tab="recurring" onTabChange={() => {}} />)
    fireEvent.click(await screen.findByRole("button", { name: "New schedule" }))
    const dialog = await screen.findByRole("dialog")
    await waitFor(() => expect(within(dialog).getByRole("option", { name: "Research" })).toBeInTheDocument())
    fireEvent.change(within(dialog).getByLabelText("Crew"), { target: { value: "c-res" } })
    fireEvent.change(within(dialog).getByLabelText("Title"), { target: { value: "x" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Create schedule" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("invalid cron expression")
  })
})

describe("IntakeSurface — tabs and small screens", () => {
  it("switches tab through onTabChange", async () => {
    const onTabChange = vi.fn()
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={onTabChange} />)
    fireEvent.click(await screen.findByRole("tab", { name: /Recurring issues/ }))
    expect(onTabChange).toHaveBeenCalledWith("recurring")
  })

  it("lets rows wrap instead of overflowing a phone", async () => {
    render(<IntakeSurface workspaceId="ws" tab="triage" onTabChange={() => {}} />)
    const row = (await screen.findByText("Bugs to Ops")).closest("[data-slot='intake-row']")!
    expect(row.className).toMatch(/flex-wrap/)
  })
})
