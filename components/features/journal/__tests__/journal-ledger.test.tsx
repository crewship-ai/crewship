import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

// =============================================================================
// The Journal's technical log and fleet runs, inside Activity (F-02). The URL
// is the filter state, exactly as it was on /journal — every value is a pure
// read of `searchParams`, writes happen in handlers, and a same-pathname push
// (a run row opening its trace) moves the whole view. These cases are the old
// /journal URL-state suite, carried over to /activity?section=log|runs.
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
  usePathname: () => "/activity",
}))

const role = "OWNER"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false, role, capabilities: null }),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ role, loading: false, abilities: null, capabilities: null, hasCapability: () => true }),
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

/* eslint-disable @typescript-eslint/no-explicit-any */
let savedViewsProps: any = {}
vi.mock("@/components/features/journal/journal-saved-views", () => ({
  JournalSavedViews: (props: Record<string, unknown>) => {
    savedViewsProps = props
    return <div data-testid="saved-views" />
  },
}))

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

import { JournalLedger } from "../journal-ledger"

/** What ActivityWorkspace does: `?section=` picks the view. */
function Harness() {
  return <JournalLedger view={searchParams.get("section") === "runs" ? "runs" : "log"} />
}

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
  return render(<Harness />)
}

beforeEach(() => {
  push.mockClear()
  replace.mockClear()
  listParams = {}
  logsPanelProps = {}
  runsViewProps = {}
  savedViewsProps = {}
})

describe("JournalLedger — the URL is the source of truth", () => {
  it("names the view in the Activity top bar", async () => {
    mountAt("section=log")
    await screen.findByTestId("logs-panel")
    expect(screen.getAllByText("Technical log").length).toBeGreaterThan(0)
    expect(screen.queryByText("Crew Journal")).toBeNull()
  })

  it("hydrates the runs ledger from ?section=runs on a cold load", async () => {
    mountAt("section=runs")
    expect(await screen.findByTestId("runs-view")).toBeTruthy()
  })

  // THE row-click bug. RunsView pushes the same pathname with a new query,
  // so the router re-renders the page without unmounting it.
  it("follows a same-pathname navigation from the runs into the log", async () => {
    const { rerender } = mountAt("section=runs")
    expect(await screen.findByTestId("runs-view")).toBeTruthy()

    // Exactly what runs-view.tsx does on a row click.
    push("/activity?section=log&trace_id=msg_1788028824981726523")
    rerender(<Harness />)

    const panel = await screen.findByTestId("logs-panel")
    expect(screen.queryByTestId("runs-view")).toBeNull()
    expect(panel.getAttribute("data-trace")).toBe("msg_1788028824981726523")
    await waitFor(() => {
      expect(listParams.trace_id).toBe("msg_1788028824981726523")
    })
  })

  it("follows a same-pathname navigation that only changes a filter", async () => {
    const { rerender } = mountAt("section=log")
    await screen.findByTestId("logs-panel")

    push("/activity?section=log&severity=error&crew_id=crew-9")
    rerender(<Harness />)

    await waitFor(() => {
      expect(listParams.severity).toBe("error")
      expect(listParams.crew_id).toBe("crew-9")
    })
  })

  it("switches between the log and the runs and keeps the section in the URL", async () => {
    mountAt("section=log&crew_id=c1")
    await screen.findByTestId("logs-panel")
    screen.getByRole("tab", { name: /Fleet runs/ }).click()
    await waitFor(() => expect(lastPushed()).toBe("/activity?section=runs&crew_id=c1"))
  })

  it("writes the search query into the URL", async () => {
    mountAt("section=log")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onServerSearch("routine:nightly-digest outcome:failed")
    await waitFor(() => {
      expect(writtenUrls().some((u) => u.startsWith("/activity?") && u.includes("section=log") && u.includes("q=routine"))).toBe(true)
    })
  })

  it("restores the search query from the URL", async () => {
    mountAt("section=log&q=payment+declined")
    const panel = await screen.findByTestId("logs-panel")
    expect(panel.getAttribute("data-query")).toBe("payment declined")
    await waitFor(() => {
      expect(listParams.q).toBe("payment declined")
    })
  })

  // router.replace makes Back skip the whole filter history and leave the
  // page. Filter changes are navigations the user should be able to undo.
  it("pushes a filter change so Back steps through it", async () => {
    mountAt("section=log")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onSeverityChange("error")
    await waitFor(() => {
      expect(lastPushed()).toContain("severity=error")
    })
    expect(lastPushed()).toContain("section=log")
  })

  it("composes two filter writes made in the same handler", async () => {
    mountAt("section=log")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onSeverityChange("error")
    logsPanelProps.onMutedChange(new Set(["container"]))

    await waitFor(() => {
      expect(lastPushed()).toContain("mute=container")
    })
    expect(lastPushed()).toContain("severity=error")
  })

  it("does not stack a history entry for a write that changes nothing", async () => {
    mountAt("section=log&severity=error")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onSeverityChange("error")
    logsPanelProps.onSeverityChange("error")
    await waitFor(() => {
      expect(screen.getByTestId("logs-panel")).toBeTruthy()
    })
    expect(push.mock.calls.length).toBe(0)
  })

  it("mirrors the runs window / status / trigger into the URL", async () => {
    mountAt("section=runs")
    await screen.findByTestId("runs-view")

    runsViewProps.onStatusFilterChange("FAILED")
    await waitFor(() => {
      expect(lastPushed()).toContain("run_status=FAILED")
    })
    expect(lastPushed()).toContain("section=runs")

    runsViewProps.onWindowChange("7d")
    await waitFor(() => {
      expect(lastPushed()).toContain("run_window=7d")
    })
  })

  it("hydrates the runs filters from the URL", async () => {
    mountAt("section=runs&run_window=7d&run_status=FAILED&run_trigger=CRON&run_page=3")
    const view = await screen.findByTestId("runs-view")
    expect(view.getAttribute("data-window")).toBe("7d")
    expect(view.getAttribute("data-status")).toBe("FAILED")
    expect(view.getAttribute("data-trigger")).toBe("CRON")
    expect(view.getAttribute("data-page")).toBe("3")
  })

  it.each([
    ["status", () => runsViewProps.onStatusFilterChange("FAILED"), "run_status=FAILED"],
    ["trigger", () => runsViewProps.onTriggerFilterChange("CRON"), "run_trigger=CRON"],
    ["window", () => runsViewProps.onWindowChange("7d"), "run_window=7d"],
  ])("drops the runs page number when the %s filter changes", async (_name, act, expected) => {
    mountAt("section=runs&run_page=3")
    await screen.findByTestId("runs-view")

    act()
    await waitFor(() => {
      expect(lastPushed()).toContain(expected)
    })
    expect(lastPushed()).not.toContain("run_page")
  })

  it("keeps the trace focus clearable from the log", async () => {
    mountAt("section=log&trace_id=t-1")
    const panel = await screen.findByTestId("logs-panel")
    expect(panel.getAttribute("data-trace")).toBe("t-1")

    logsPanelProps.onClearTraceId()
    await waitFor(() => {
      expect(push.mock.calls.length).toBeGreaterThan(0)
    })
    expect(lastPushed()).not.toContain("trace_id")
  })

  it("hydrates an issue focus from ?mission_id=, sends it to the list, and clears it", async () => {
    mountAt("section=log&mission_id=ENG-4")
    await waitFor(() => expect(listParams.mission_id).toBe("ENG-4"))
    expect(logsPanelProps.missionId).toBe("ENG-4")
    ;(logsPanelProps.onClearMissionId as () => void)()
    await waitFor(() => expect(lastPushed()).not.toContain("mission_id"))
  })

  it("reads crew and agent slugs the way entityHref writes them", async () => {
    mountAt("section=log&crew_id=engineering&agent_id=robin")
    await waitFor(() => expect(listParams.crew_id).toBe("engineering"))
    expect(listParams.agent_id).toBe("robin")
  })

  // Z29: the older `?crew=` / `?agent=` slug spelling.
  it("reads the older crew= / agent= spelling", async () => {
    mountAt("section=log&crew=engineering&agent=robin")
    await waitFor(() => expect(listParams.crew_id).toBe("engineering"))
    expect(listParams.agent_id).toBe("robin")
  })

  // Per-user reading preferences are deliberately NOT in the URL: a shared
  // link must not impose the sender's wrap / sort / refresh cadence on the
  // reader, and they already persist per user via /api/v1/me/preferences.
  it("keeps per-user reading preferences out of the URL", async () => {
    mountAt("section=log")
    await screen.findByTestId("logs-panel")

    logsPanelProps.onRefreshRateChange("10s")
    logsPanelProps.onLiveChange(false)
    await waitFor(() => {
      expect(screen.getByTestId("logs-panel")).toBeTruthy()
    })
    expect(writtenUrls().some((u) => /refresh|wrap|sort|dedup|live/.test(u))).toBe(false)
  })
})

describe("JournalLedger — saved views keep the Journal's vocabulary", () => {
  it("stores the runs ledger as tab=runs and the log without a tab", async () => {
    const { rerender } = mountAt("section=runs&run_status=FAILED")
    await screen.findByTestId("runs-view")
    expect(savedViewsProps.filters).toEqual({ tab: "runs", run_status: "FAILED" })

    searchParams = new URLSearchParams("section=log&severity=error")
    rerender(<Harness />)
    await screen.findByTestId("logs-panel")
    expect(savedViewsProps.filters).toEqual({ severity: "error" })
  })

  it.each([
    [{ tab: "runs", run_status: "FAILED" }, "/activity?section=runs&run_status=FAILED"],
    [{ severity: "error" }, "/activity?section=log&severity=error"],
    [{ tab: "timeline", trace_id: "t-1" }, "/activity?section=log&trace_id=t-1"],
    [{ tab: "spend" }, "/activity?mode=spend"],
    [{}, "/activity?section=log"],
  ])("applies %j as %s", async (params, target) => {
    mountAt("section=log&q=old")
    await screen.findByTestId("logs-panel")
    savedViewsProps.onApply(params)
    expect(lastPushed()).toBe(target)
  })
})

describe("JournalLedger — queryParams assembly", () => {
  it("turns muted groups into exclude_entry_type", async () => {
    mountAt("section=log&mute=container&severity=warn")
    await screen.findByTestId("logs-panel")
    await waitFor(() => {
      expect(listParams.severity).toBe("warn")
      expect(listParams.exclude_entry_type).toContain("container.")
    })
  })

  it("routes structured search tokens to server params", async () => {
    mountAt("section=log&q=agent%3Aviktor+boom")
    await screen.findByTestId("logs-panel")
    await waitFor(() => {
      expect(listParams.agent_id).toBe("viktor")
      expect(listParams.q).toBe("boom")
    })
  })
})
