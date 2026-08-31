import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// =============================================================================
// /journal reads its whole filter set out of the URL exactly once — at mount.
// `initialTab`, `traceId`, `initialTimeRange`, `initialSeverity` and
// `initialMuted` are all `useMemo(…, [])` or lazy `useState` initialisers, and
// nothing re-reads `searchParams` afterwards.
//
// That is fine for a cold load and broken for everything else. The Runs tab
// pushes `/journal?tab=timeline&trace_id=<id>` when a row is clicked — the
// SAME pathname, so the App Router re-renders JournalPage without unmounting
// it. The URL changes, the tab does not, and the user is left staring at the
// runs table while the address bar claims Timeline. Every in-app link into
// `/journal?…` from an already-mounted journal has the same problem.
//
// The mirror effect in the other direction is incomplete too: it writes
// `time`, `from`/`to`, `crew_id`, `agent_id`, `trace_id`, `severity`, `mute`
// and `tab`, but never `q`. A shared "here is the failure I found" link loses
// the query that found it. And it writes with `router.replace`, so Back skips
// the entire filter history and leaves the page.
//
// The Runs tab mirrors nothing at all: window, trigger, status and page are
// local component state, so a Runs view cannot be sent to anyone.
// =============================================================================

let searchParams = new URLSearchParams()
const push = vi.fn((url: string) => {
  searchParams = new URLSearchParams(url.split("?")[1] ?? "")
})
const replace = vi.fn((url: string) => {
  searchParams = new URLSearchParams(url.split("?")[1] ?? "")
})

vi.mock("next/navigation", () => ({
  useSearchParams: () => searchParams,
  useRouter: () => ({ push, replace }),
  usePathname: () => "/journal",
}))

let role = "OWNER"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false, role, capabilities: null }),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({
    role,
    loading: false,
    abilities: null,
    capabilities: null,
    hasCapability: () => true,
  }),
}))

// Per-user preferences are NOT part of the URL contract (see the test at the
// bottom). Stub them so nothing touches /api/v1/me/preferences.
vi.mock("@/hooks/use-user-preference", () => ({
  useUserPreference: <T,>(_key: string, def: T) => [def, vi.fn(), { ready: true }],
}))

let listParams: Record<string, string | undefined> = {}
vi.mock("@/hooks/use-journal-list", () => ({
  useJournalList: (opts: { params: Record<string, string | undefined> }) => {
    listParams = opts.params
    return {
      entries: [],
      nextCursor: null,
      loading: false,
      loadingMore: false,
      error: null,
      refresh: vi.fn(),
      loadMore: vi.fn(),
      prependLive: vi.fn(),
    }
  },
}))
vi.mock("@/hooks/use-journal-stream", () => ({
  useJournalStream: () => ({ status: "connected" }),
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => [] })),
}))

vi.mock("@/components/features/logs/resources-strip", () => ({
  ResourcesStrip: () => <div data-testid="resources-strip" />,
}))
vi.mock("@/components/features/journal/journal-spend-view", () => ({
  JournalSpendView: () => <div data-testid="spend-view" />,
}))

/* eslint-disable @typescript-eslint/no-explicit-any */
let logsPanelProps: any = {}
vi.mock("@/components/features/logs/logs-panel", () => ({
  LogsPanel: (props: Record<string, unknown>) => {
    logsPanelProps = props
    return (
      <div
        data-testid="logs-panel"
        data-trace={String(props.traceId ?? "")}
        data-query={String(props.query ?? "")}
        data-severity={String(props.severity ?? "")}
      />
    )
  },
}))

let runsViewProps: any = {}
vi.mock("@/components/features/journal/runs-view", () => ({
  RunsView: (props: Record<string, unknown>) => {
    runsViewProps = props
    return (
      <div
        data-testid="runs-view"
        data-window={String(props.window ?? "")}
        data-status={String(props.statusFilter ?? "")}
        data-trigger={String(props.triggerFilter ?? "")}
        data-page={String(props.page ?? "")}
      />
    )
  },
}))
/* eslint-enable @typescript-eslint/no-explicit-any */

import JournalPage from "../page"

/** Every URL the page wrote, in order. */
function writtenUrls(): string[] {
  return [...push.mock.calls, ...replace.mock.calls].map((c) => String(c[0]))
}

function lastPushed(): string {
  const calls = push.mock.calls
  return calls.length > 0 ? String(calls[calls.length - 1][0]) : ""
}

function mountAt(query: string) {
  searchParams = new URLSearchParams(query)
  return render(<JournalPage />)
}

beforeEach(() => {
  role = "OWNER"
  push.mockClear()
  replace.mockClear()
  listParams = {}
  logsPanelProps = {}
  runsViewProps = {}
})

describe("JournalPage — URL as the source of truth", () => {
  it("hydrates the tab from ?tab= on a cold load", async () => {
    mountAt("tab=runs")
    expect(await screen.findByTestId("runs-view")).toBeTruthy()
  })

  // THE row-click bug. RunsView pushes the same pathname with a new query,
  // so the router re-renders the page without unmounting it.
  it("follows a same-pathname navigation into another tab", async () => {
    const { rerender } = mountAt("tab=runs")
    expect(await screen.findByTestId("runs-view")).toBeTruthy()

    // Exactly what runs-view.tsx does on a row click.
    push("/journal?tab=timeline&trace_id=msg_1788028824981726523")
    rerender(<JournalPage />)

    const panel = await screen.findByTestId("logs-panel")
    expect(screen.queryByTestId("runs-view")).toBeNull()
    expect(panel.getAttribute("data-trace")).toBe("msg_1788028824981726523")
    await waitFor(() => {
      expect(listParams.trace_id).toBe("msg_1788028824981726523")
    })
  })

  it("follows a same-pathname navigation that only changes a filter", async () => {
    const { rerender } = mountAt("")
    await screen.findByTestId("logs-panel")

    push("/journal?severity=error&crew_id=crew-9")
    rerender(<JournalPage />)

    await waitFor(() => {
      expect(listParams.severity).toBe("error")
      expect(listParams.crew_id).toBe("crew-9")
    })
  })

  it("writes the search query into the URL", async () => {
    mountAt("")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onServerSearch("routine:nightly-digest outcome:failed")
    await waitFor(() => {
      expect(writtenUrls().some((u) => u.includes("q=routine"))).toBe(true)
    })
  })

  it("restores the search query from the URL", async () => {
    mountAt("q=payment+declined")
    const panel = await screen.findByTestId("logs-panel")
    expect(panel.getAttribute("data-query")).toBe("payment declined")
    await waitFor(() => {
      expect(listParams.q).toBe("payment declined")
    })
  })

  // router.replace makes Back skip the whole filter history and leave the
  // page. Filter changes are navigations the user should be able to undo.
  it("pushes a filter change so Back steps through it", async () => {
    mountAt("")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onSeverityChange("error")
    await waitFor(() => {
      expect(lastPushed()).toContain("severity=error")
    })
  })

  // LogsPanel's "clear all filters" (and its Escape shortcut) calls three
  // setters in one handler. `router.push` does not update `searchParams`
  // synchronously, so each write sees the query string this render was given
  // — without composition the last write silently discards the others.
  it("composes two filter writes made in the same handler", async () => {
    mountAt("")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onSeverityChange("error")
    logsPanelProps.onMutedChange(new Set(["container"]))

    await waitFor(() => {
      expect(lastPushed()).toContain("mute=container")
    })
    expect(lastPushed()).toContain("severity=error")
  })

  it("does not stack a history entry for a write that changes nothing", async () => {
    mountAt("severity=error")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onSeverityChange("error")
    logsPanelProps.onSeverityChange("error")
    await waitFor(() => {
      expect(screen.getByTestId("logs-panel")).toBeTruthy()
    })
    expect(push.mock.calls.length).toBe(0)
  })

  it("mirrors the Runs window / status / trigger into the URL", async () => {
    mountAt("tab=runs")
    await screen.findByTestId("runs-view")

    runsViewProps.onStatusFilterChange("FAILED")
    await waitFor(() => {
      expect(lastPushed()).toContain("run_status=FAILED")
    })
    expect(lastPushed()).toContain("tab=runs")

    runsViewProps.onWindowChange("7d")
    await waitFor(() => {
      expect(lastPushed()).toContain("run_window=7d")
    })
  })

  it("hydrates the Runs filters from the URL", async () => {
    mountAt("tab=runs&run_window=7d&run_status=FAILED&run_trigger=CRON&run_page=3")
    const view = await screen.findByTestId("runs-view")
    expect(view.getAttribute("data-window")).toBe("7d")
    expect(view.getAttribute("data-status")).toBe("FAILED")
    expect(view.getAttribute("data-trigger")).toBe("CRON")
    expect(view.getAttribute("data-page")).toBe("3")
  })

  // A page number from the previous filter set almost always lands on an
  // empty result. Reset it in the same navigation, not a second one.
  // Every Runs filter narrows the result set, so any of them can strand the
  // reader on a page that no longer exists — page 3 of a 7-day window is
  // routinely empty once the window is 24h. Covered as a table rather than one
  // case because the original only exercised status, and the window handler
  // shipped without the reset precisely because nothing asked it for one.
  it.each([
    ["status", () => runsViewProps.onStatusFilterChange("FAILED"), "run_status=FAILED"],
    ["trigger", () => runsViewProps.onTriggerFilterChange("CRON"), "run_trigger=CRON"],
    ["window", () => runsViewProps.onWindowChange("7d"), "run_window=7d"],
  ])("drops the Runs page number when the %s filter changes", async (_name, act, expected) => {
    mountAt("tab=runs&run_page=3")
    await screen.findByTestId("runs-view")

    act()
    await waitFor(() => {
      expect(lastPushed()).toContain(expected)
    })
    expect(lastPushed()).not.toContain("run_page")
  })

  it("keeps the trace focus clearable from the timeline", async () => {
    mountAt("trace_id=t-1")
    const panel = await screen.findByTestId("logs-panel")
    expect(panel.getAttribute("data-trace")).toBe("t-1")

    logsPanelProps.onClearTraceId()
    await waitFor(() => {
      expect(push.mock.calls.length).toBeGreaterThan(0)
    })
    expect(lastPushed()).not.toContain("trace_id")
  })

  // Per-user reading preferences are deliberately NOT in the URL: a shared
  // link must not impose the sender's wrap / sort / refresh cadence on the
  // reader, and they already persist per user via /api/v1/me/preferences.
  it("keeps per-user reading preferences out of the URL", async () => {
    mountAt("")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onRefreshRateChange("10s")
    logsPanelProps.onLiveChange(false)
    await waitFor(() => {
      expect(screen.getByTestId("logs-panel")).toBeTruthy()
    })
    expect(writtenUrls().some((u) => /refresh|wrap|sort|dedup|live/.test(u))).toBe(false)
  })
})

describe("JournalPage — tab RBAC", () => {
  it("demotes an admin-only tab for a non-admin and cleans the URL", async () => {
    role = "MEMBER"
    mountAt("tab=spend")
    expect(await screen.findByTestId("logs-panel")).toBeTruthy()
    expect(screen.queryByTestId("spend-view")).toBeNull()
    await waitFor(() => {
      expect(replace.mock.calls.length).toBeGreaterThan(0)
    })
    expect(String(replace.mock.calls[0][0])).not.toContain("tab=spend")
  })

  it("keeps an admin-only tab for an admin", async () => {
    role = "ADMIN"
    mountAt("tab=spend")
    expect(await screen.findByTestId("spend-view")).toBeTruthy()
  })

  it("falls back to the timeline for an unknown tab", async () => {
    mountAt("tab=wat")
    expect(await screen.findByTestId("logs-panel")).toBeTruthy()
  })
})

describe("JournalPage — queryParams assembly", () => {
  it("turns muted groups into exclude_entry_type", async () => {
    mountAt("mute=container&severity=warn")
    await screen.findByTestId("logs-panel")
    await waitFor(() => {
      expect(listParams.severity).toBe("warn")
      expect(listParams.exclude_entry_type).toContain("container.")
    })
  })

  it("routes structured search tokens to server params", async () => {
    mountAt("q=agent%3Aviktor+boom")
    await screen.findByTestId("logs-panel")
    await waitFor(() => {
      expect(listParams.agent_id).toBe("viktor")
      expect(listParams.q).toBe("boom")
    })
  })
})
