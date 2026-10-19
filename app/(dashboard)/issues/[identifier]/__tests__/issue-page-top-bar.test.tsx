import type { ReactNode } from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor, fireEvent, within, cleanup } from "@testing-library/react"

// /issues/<id> (v67 I-FULL, issues.js:182-187): the top bar has ↑/↓ for the
// previous and next issue, ⋯ (Move to project, Cancel, Delete) and ✕, and
// the Issues panel sits on the left as it does on /issues.

const push = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, replace: vi.fn() }),
  useParams: () => ({ identifier: "_" }),
  usePathname: () => (typeof window !== "undefined" ? window.location.pathname : "/"),
  useSearchParams: () => new URLSearchParams(typeof window !== "undefined" ? window.location.search : ""),
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", loading: false, role: "ADMIN" }) }))
vi.mock("@/hooks/use-auth", () => ({
  useSession: () => ({ data: { user: { id: "u1", name: "Demo", email: "d@e.f" } } }),
  useSessionSafe: () => ({ data: { user: { id: "u1", name: "Demo", email: "d@e.f" } } }),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeStatusSafe: () => "connected" }))
vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: [], loading: false, refetch: vi.fn() }) }))
vi.mock("@/hooks/use-user-preference", () => ({ useUserPreference: <T,>(_k: string, d: T) => [d, vi.fn()] }))
vi.mock("@/components/features/issues/tiptap-editor", () => ({ TiptapEditor: () => <div data-testid="tiptap" /> }))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({
  RunActivityTimeline: () => <div data-testid="run-timeline" />,
  RUN_WORK_ENTRY_TYPES: ["exec"],
}))
vi.mock("@/components/features/issues/markdown-content", () => ({
  MarkdownContent: ({ children }: { children: string }) => <div>{children}</div>,
}))
vi.mock("@/lib/agent-avatar", () => ({ getAgentAvatarUrl: () => "", subscribeAvatarStyles: () => () => {}, avatarStylesVersion: () => 0 }))
vi.mock("@/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ children }: { children: ReactNode }) => <>{children}</>,
  TooltipContent: () => null,
  TooltipProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock("@/lib/toast", () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { IssuePageClient } from "../issue-page-client"

const base = { crew_id: "crew-ops", crew_name: "Ops", workspace_id: "ws-test", description: "", labels: [], created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z" }
const LIST = [
  { ...base, id: "i3", identifier: "OPS-3", title: "Third", status: "TODO", sort_order: 1 },
  { ...base, id: "i4", identifier: "OPS-4", title: "Fetch current weather data from wttr.in", status: "TODO", sort_order: 2 },
  { ...base, id: "i5", identifier: "OPS-5", title: "Fifth", status: "TODO", sort_order: 3 },
]
const ISSUE = { ...LIST[1], status: "BACKLOG" }
const PROJECTS = [{ id: "p1", workspace_id: "ws-test", name: "Launch v2", slug: "launch-v2", icon: "rocket", color: "blue", health: "on_track", issue_count: 2, progress: 10, status: "in_progress", priority: "high" }]

type Call = { url: string; init?: RequestInit }
let calls: Call[] = []
const json = (body: unknown, status = 200) =>
  Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body), headers: new Headers({ "X-Total-Count": "3" }) } as unknown as Response)

const originalLocation = Object.getOwnPropertyDescriptor(window, "location")
function at(search: string) {
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: { ...window.location, pathname: "/issues/OPS-4", search, href: `https://x/issues/OPS-4${search}`, origin: "https://x" },
  })
}

beforeEach(() => {
  push.mockReset()
  calls = []
  at("")
  global.fetch = vi.fn((url: string, init?: RequestInit) => {
    const u = String(url)
    calls.push({ url: u, init })
    if (init?.method === "PATCH" || init?.method === "DELETE") return json({}, init.method === "DELETE" ? 200 : 200)
    if (u.includes("/api/v1/issues/OPS-4")) return json(ISSUE)
    if (/\/api\/v1\/issues\?/.test(u)) return json(LIST)
    if (u.includes("/api/v1/projects?")) return json(PROJECTS)
    return json([])
  }) as unknown as typeof fetch
})
afterEach(() => {
  cleanup()
  if (originalLocation) Object.defineProperty(window, "location", originalLocation)
})

async function ready() {
  render(<IssuePageClient />)
  await waitFor(() => expect(screen.getByText(/Fetch current weather data/)).toBeInTheDocument(), { timeout: 3000 })
}

describe("/issues/<id> — the Issues panel (ISS-02)", () => {
  it("shows the Issues panel with its views and projects", async () => {
    await ready()
    const panel = document.querySelector("[data-slot=secondary-panel][data-panel-id=issues]") as HTMLElement
    expect(panel).not.toBeNull()
    await waitFor(() => expect(within(panel).getByText("Launch v2")).toBeInTheDocument())
    expect(within(panel).getByText("All issues")).toBeInTheDocument()
  })

  it("a project picked in the panel opens it on /issues", async () => {
    await ready()
    const panel = document.querySelector("[data-slot=secondary-panel][data-panel-id=issues]") as HTMLElement
    fireEvent.click(await within(panel).findByText("Launch v2"))
    await waitFor(() => expect(push).toHaveBeenCalledWith("/issues?project=p1"))
  })
})

describe("/issues/<id> — the top bar (ISS-01)", () => {
  it("↑/↓ open the previous and next issue and keep where Back goes", async () => {
    at("?from=%2Finbox&fromLabel=Inbox")
    await ready()
    await waitFor(() => expect(screen.getByRole("button", { name: "Next issue" })).toBeEnabled())
    fireEvent.click(screen.getByRole("button", { name: "Next issue" }))
    expect(push).toHaveBeenLastCalledWith("/issues/OPS-5?from=%2Finbox&fromLabel=Inbox")
    fireEvent.click(screen.getByRole("button", { name: "Previous issue" }))
    expect(push).toHaveBeenLastCalledWith("/issues/OPS-3?from=%2Finbox&fromLabel=Inbox")
  })

  it("✕ goes back to the list", async () => {
    await ready()
    fireEvent.click(screen.getByRole("button", { name: "Back to list" }))
    expect(push).toHaveBeenCalledWith("/issues")
  })

  it("⋯ offers Copy link, Duplicate issue, Move to project, Cancel issue and Delete issue", async () => {
    await ready()
    fireEvent.keyDown(screen.getByRole("button", { name: "Issue options" }), { key: "Enter" })
    const menu = await screen.findByRole("menu")
    const items = within(menu).getAllByRole("menuitem").map((m) => m.textContent?.trim())
    expect(items).toEqual(["Copy link", "Duplicate issue", "Move to project…", "Cancel issue…", "Delete issue…"])
  })

  // What the server sends (issue_chat_metadata.go issueActionsForState): a
  // writer's actions without "delete" while the issue is not Backlog or
  // Cancelled. Delete stays in the menu, disabled with the reason (v67 imenu).
  it("⋯ keeps Delete issue… for a writer on an open issue, disabled with the reason", async () => {
    const WRITER = ["view", "comment", "edit_fields", "assign", "change_status", "change_workflow", "attach_files", "link", "start_work"]
    const open = { ...ISSUE, status: "TODO", allowed_actions: WRITER }
    global.fetch = vi.fn((url: string) => {
      const u = String(url)
      if (u.includes("/api/v1/issues/OPS-4")) return json(open)
      if (/\/api\/v1\/issues\?/.test(u)) return json(LIST)
      if (u.includes("/api/v1/projects?")) return json(PROJECTS)
      return json([])
    }) as unknown as typeof fetch
    await ready()
    fireEvent.keyDown(screen.getByRole("button", { name: "Issue options" }), { key: "Enter" })
    const menu = await screen.findByRole("menu")
    const del = within(menu).getByRole("menuitem", { name: "Delete issue…" })
    expect(del).toHaveAttribute("data-disabled")
    expect(del).toHaveAttribute("title", "Only Backlog or Cancelled issues can be deleted. Cancel it first.")
  })

  it("⋯ has no Delete issue… for a reader", async () => {
    const read = { ...ISSUE, status: "BACKLOG", allowed_actions: ["view"] }
    global.fetch = vi.fn((url: string) => {
      const u = String(url)
      if (u.includes("/api/v1/issues/OPS-4")) return json(read)
      if (/\/api\/v1\/issues\?/.test(u)) return json(LIST)
      if (u.includes("/api/v1/projects?")) return json(PROJECTS)
      return json([])
    }) as unknown as typeof fetch
    await ready()
    fireEvent.keyDown(screen.getByRole("button", { name: "Issue options" }), { key: "Enter" })
    const menu = await screen.findByRole("menu")
    expect(within(menu).queryByRole("menuitem", { name: "Delete issue…" })).toBeNull()
  })

  it("Move to project PATCHes the project and reloads the issue", async () => {
    await ready()
    fireEvent.keyDown(screen.getByRole("button", { name: "Issue options" }), { key: "Enter" })
    fireEvent.click(await screen.findByRole("menuitem", { name: "Move to project…" }))
    const dialog = await screen.findByRole("dialog", { name: /Move OPS-4 to a project/ })
    fireEvent.click(await within(dialog).findByRole("radio", { name: /Launch v2/ }))
    const reads = calls.filter((c) => c.url.includes("/api/v1/issues/OPS-4")).length
    fireEvent.click(within(dialog).getByRole("button", { name: "Move" }))
    await waitFor(() => expect(calls.some((c) => c.init?.method === "PATCH")).toBe(true))
    const patch = calls.find((c) => c.init?.method === "PATCH")!
    expect(patch.url).toBe("/api/v1/crews/crew-ops/issues/OPS-4?workspace_id=ws-test")
    expect(JSON.parse(String(patch.init!.body))).toEqual({ project_id: "p1" })
    await waitFor(() => expect(calls.filter((c) => c.url.includes("/api/v1/issues/OPS-4")).length).toBeGreaterThan(reads))
  })

  it("Cancel issue asks first, then sets Cancelled", async () => {
    await ready()
    fireEvent.keyDown(screen.getByRole("button", { name: "Issue options" }), { key: "Enter" })
    fireEvent.click(await screen.findByRole("menuitem", { name: "Cancel issue…" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(calls.some((c) => c.init?.method === "PATCH")).toBe(false)
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel issue" }))
    await waitFor(() => expect(calls.some((c) => c.init?.method === "PATCH")).toBe(true))
    expect(JSON.parse(String(calls.find((c) => c.init?.method === "PATCH")!.init!.body))).toEqual({ status: "CANCELLED" })
  })

  it("Delete issue asks with a red button, DELETEs, and goes back to the list", async () => {
    await ready()
    fireEvent.keyDown(screen.getByRole("button", { name: "Issue options" }), { key: "Enter" })
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete issue…" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent("Delete OPS-4?")
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete issue" }))
    await waitFor(() => expect(calls.some((c) => c.init?.method === "DELETE")).toBe(true))
    expect(calls.find((c) => c.init?.method === "DELETE")!.url).toBe("/api/v1/crews/crew-ops/issues/OPS-4?workspace_id=ws-test")
    await waitFor(() => expect(push).toHaveBeenCalledWith("/issues"))
  })
})
