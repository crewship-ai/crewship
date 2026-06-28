import type { ReactNode } from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// Mock next/navigation. useParams() deliberately returns the static-export
// "_" placeholder — the regression is that the page must NOT use it and
// must read the identifier from window.location.pathname instead.
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useParams: () => ({ identifier: "_" }),
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useSession: () => ({ data: { user: { name: "Demo", email: "d@e.f" } } }),
}))
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
}))

// Stub the heavy children so the test stays about data-fetch wiring.
vi.mock("@/components/features/issues/tiptap-editor", () => ({
  TiptapEditor: () => <div data-testid="tiptap" />,
}))
vi.mock("@/components/features/issues/activity-feed", () => ({
  ActivityFeed: () => <div data-testid="activity-feed" />,
}))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({
  RunActivityTimeline: () => <div data-testid="run-timeline" />,
  RUN_WORK_ENTRY_TYPES: ["exec"],
}))
vi.mock("@/components/features/orchestration/issue-sidebar", () => ({
  IssueSidebar: () => <div data-testid="sidebar" />,
  IssueSidebarMobile: () => <div data-testid="sidebar-mobile" />,
}))
vi.mock("@/components/features/issues/markdown-content", () => ({
  MarkdownContent: ({ children }: { children: string }) => <div>{children}</div>,
}))
vi.mock("@/lib/agent-avatar", () => ({
  getAgentAvatarUrl: () => "",
}))
// Tooltip needs a TooltipProvider ancestor the real app supplies higher up;
// stub it to passthroughs so the header renders in isolation.
vi.mock("@/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ children }: { children: ReactNode }) => <>{children}</>,
  TooltipContent: () => null,
}))

import { IssuePageClient } from "../issue-page-client"

const ISSUE = {
  id: "iss-1",
  identifier: "OPS-4",
  title: "Fetch current weather data from wttr.in",
  description: "",
  status: "BACKLOG",
  crew_id: "crew-ops",
  crew_name: "Ops",
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
  labels: [],
}

describe("<IssuePageClient> — identifier resolution (static-export regression)", () => {
  beforeEach(() => {
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...window.location, pathname: "/issues/OPS-4", href: "https://x/issues/OPS-4" },
    })

    global.fetch = vi.fn((url: string) => {
      const u = String(url)
      if (u.includes("/api/v1/issues/OPS-4")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(ISSUE) }) as unknown as Promise<Response>
      }
      if (u.includes("/api/v1/issues/_")) {
        // The bug path: must never be hit.
        return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) }) as unknown as Promise<Response>
      }
      // comments / activity / relations / sidebar lists
      return Promise.resolve({ ok: true, json: () => Promise.resolve([]) }) as unknown as Promise<Response>
    }) as unknown as typeof fetch
  })

  afterEach(() => vi.restoreAllMocks())

  it("fetches the real issue (OPS-4 from the URL), not the '_' placeholder", async () => {
    render(<IssuePageClient />)

    // The issue loads and its title renders — proving the fetch used OPS-4.
    await waitFor(
      () => expect(screen.getByText(/Fetch current weather data/)).toBeInTheDocument(),
      { timeout: 3000 },
    )

    const calledUrls = (global.fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls.map(
      (c) => String(c[0]),
    )
    expect(calledUrls.some((u) => u.includes("/api/v1/issues/OPS-4"))).toBe(true)
    expect(calledUrls.some((u) => u.includes("/api/v1/issues/_"))).toBe(false)
  })
})
