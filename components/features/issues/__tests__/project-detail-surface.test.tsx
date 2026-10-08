// The project detail edits a draft: every picker on the card lands on the
// page's floating save bar, and one Save sends one PATCH with what changed.
// A status or lead picked by accident is a click on Discard, not a write.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, within, waitFor, cleanup } from "@testing-library/react"

const h = vi.hoisted(() => ({ api: vi.fn(), push: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: h.push }) }))
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a>,
}))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
vi.mock("../project-milestones-card", () => ({ ProjectMilestonesCard: () => null }))

import { ProjectDetailSurface } from "../project-detail-surface"
import type { Project } from "@/lib/types/mission"

const PROJECT: Project = {
  id: "p1", workspace_id: "ws1", name: "File Operations", slug: "file-operations", description: "",
  icon: "folder", color: "blue", status: "in_progress", priority: "high", health: "on_track",
  lead_type: null, lead_id: null, start_date: null, target_date: null,
  created_at: "2026-07-26T12:00:00Z", updated_at: "2026-08-01T12:00:00Z", issue_count: 0, done_count: 0, progress: 0,
}

const patches = () => h.api.mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method === "PATCH")
const bar = () => screen.queryByRole("region", { name: "Unsaved changes" })

function pick(trigger: RegExp, option: RegExp) {
  fireEvent.click(screen.getByRole("button", { name: trigger }))
  fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: option }))
}

beforeEach(() => {
  h.api.mockReset()
  h.api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "PATCH") return new Response("{}", { status: 200 })
    if (String(url).includes("/agents")) return new Response(JSON.stringify([{ id: "agent-robin", name: "Robin", slug: "robin" }]), { status: 200 })
    return new Response("{}", { status: 200 })
  })
})
afterEach(() => cleanup())

describe("ProjectDetailSurface on the page save bar", () => {
  it("holds picker changes as a draft: nothing is sent, the bar counts them", async () => {
    render(<ProjectDetailSurface workspaceId="ws1" project={PROJECT} issues={[]} />)
    pick(/change project status/i, /^completed$/i)
    pick(/change project priority/i, /^urgent$/i)
    expect(patches()).toHaveLength(0)
    expect(bar()).toHaveTextContent("2 unsaved changes")
    // The card shows what was picked, not the saved value.
    expect(screen.getByRole("button", { name: /change project status/i })).toHaveTextContent(/completed/i)
  })

  it("Save sends one PATCH carrying every changed field", async () => {
    render(<ProjectDetailSurface workspaceId="ws1" project={PROJECT} issues={[]} />)
    pick(/change project status/i, /^completed$/i)
    pick(/change health/i, /off track/i)
    fireEvent.click(within(bar()!).getByRole("button", { name: "Save" }))
    await waitFor(() => expect(patches()).toHaveLength(1))
    const [url, init] = patches()[0]
    expect(String(url)).toContain("/api/v1/projects/p1")
    expect(JSON.parse(String((init as RequestInit).body))).toEqual({ status: "completed", health: "off_track" })
  })

  it("Discard puts the card back", async () => {
    render(<ProjectDetailSurface workspaceId="ws1" project={PROJECT} issues={[]} />)
    pick(/change project status/i, /^completed$/i)
    fireEvent.click(within(bar()!).getByRole("button", { name: "Discard" }))
    expect(bar()).toBeNull()
    expect(screen.getByRole("button", { name: /change project status/i })).toHaveTextContent(/in progress/i)
  })

  it("a rename survives clicking away and waits for Save", async () => {
    render(<ProjectDetailSurface workspaceId="ws1" project={PROJECT} issues={[]} />)
    fireEvent.click(screen.getByRole("button", { name: /edit project name/i }))
    const box = screen.getByRole("textbox", { name: /project name/i })
    fireEvent.change(box, { target: { value: "Disk I/O" } })
    fireEvent.blur(box)
    expect(patches()).toHaveLength(0)
    expect(bar()).toHaveTextContent("1 unsaved change")
    expect(screen.getByRole("heading", { name: "Disk I/O" })).toBeInTheDocument()
  })

  it("picking dates does not write each day stepped through", async () => {
    render(<ProjectDetailSurface workspaceId="ws1" project={PROJECT} issues={[]} />)
    fireEvent.click(screen.getByRole("button", { name: /change dates/i }))
    const start = within(screen.getByRole("dialog")).getByLabelText(/start date/i)
    fireEvent.change(start, { target: { value: "2026-09-01" } })
    fireEvent.change(start, { target: { value: "2026-09-02" } })
    expect(patches()).toHaveLength(0)
    expect(bar()).toHaveTextContent("1 unsaved change")
  })
})
