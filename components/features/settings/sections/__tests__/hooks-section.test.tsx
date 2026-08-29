// Settings → Workspace → Lifecycle hooks (#2162).
//
// The registry had no screen at all: `crewship hooks list` was the only way to
// learn that a shell script runs on the crewshipd host before every LLM call.
// These tests pin the things that make the screen worth having rather than the
// markup — that a retired event is named as retired, that `blocking` is read
// off the event and not off the row, that a read failure is not rendered as an
// empty registry, and that the toggle is offered only to the tier the server
// will actually accept a write from.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { HooksSection } from "../hooks-section"
import { isSettingsSectionVisible, visibleSettingsSections } from "../../settings-nav"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}
function fail(status = 500) {
  return { ok: false, status, json: async () => ({}) } as unknown as Response
}

const HOOKS = [
  {
    id: "hk_guard",
    workspace_id: "ws1",
    event: "pre_llm_call",
    handler_kind: "shell",
    handler_config: { command: "scripts/guard-prompt.sh" },
    matcher: {},
    enabled: true,
    blocking: true,
    created_by: "u1",
    created_at: "2026-08-20T10:00:00Z",
    updated_at: "2026-08-20T10:00:00Z",
  },
  {
    id: "hk_audit",
    workspace_id: "ws1",
    event: "post_tool_call",
    handler_kind: "http",
    handler_config: { url: "https://audit.internal/events" },
    matcher: {},
    enabled: true,
    blocking: false,
    created_by: "u1",
    created_at: "2026-08-19T10:00:00Z",
    updated_at: "2026-08-19T10:00:00Z",
  },
  {
    id: "hk_legacy",
    workspace_id: "ws1",
    event: "pre_tool_call",
    handler_kind: "shell",
    handler_config: { command: "scripts/legacy-gate.sh" },
    matcher: {},
    enabled: false,
    blocking: true,
    created_by: "u1",
    created_at: "2026-08-01T10:00:00Z",
    updated_at: "2026-08-01T10:00:00Z",
  },
]

const JOURNAL = [
  {
    id: "j1",
    ts: "2026-08-29T12:00:00Z",
    entry_type: "hook.fired",
    severity: "warn",
    actor_id: "hk_audit",
    summary: "hook fired",
    payload: { hook_id: "hk_audit", outcome: "error", latency_ms: 4210, message: "502 from handler" },
  },
  {
    id: "j2",
    ts: "2026-08-29T11:00:00Z",
    entry_type: "hook.fired",
    severity: "info",
    actor_id: "hk_guard",
    summary: "hook fired",
    payload: { hook_id: "hk_guard", outcome: "pass", latency_ms: 38 },
  },
  {
    id: "j0",
    ts: "2026-08-28T09:00:00Z",
    entry_type: "hook.fired",
    severity: "info",
    actor_id: "hk_audit",
    summary: "hook fired",
    payload: { hook_id: "hk_audit", outcome: "pass", latency_ms: 90 },
  },
]

function route({
  hooks = HOOKS,
  journal = JOURNAL,
  hooksFail = false,
  journalFail = false,
  toggleFail = false,
}: {
  hooks?: unknown[]
  journal?: unknown[]
  hooksFail?: boolean
  journalFail?: boolean
  toggleFail?: boolean
} = {}) {
  h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url)
    if (u.includes("/api/v1/hooks/")) {
      return toggleFail ? fail(403) : ok({})
    }
    if (u.includes("/api/v1/hooks")) {
      return hooksFail ? fail() : ok({ rows: hooks, count: hooks.length })
    }
    if (u.includes("/api/v1/journal")) {
      return journalFail ? fail() : ok({ entries: journal, count: journal.length })
    }
    return ok({})
  })
}

function renderSection(role: string | null = "ADMIN") {
  return render(<HooksSection workspaceId="ws1" role={role} />)
}

function rowFor(name: string | RegExp) {
  return screen.getByRole("row", { name }) as HTMLElement
}

/**
 * Wait for the registry table, then assert inside it.
 *
 * `screen.getByText("pre_tool_call")` is ambiguous by design: the event name
 * appears in its row AND in the footnote explaining why a retired event never
 * fires. Scoping to the table keeps the assertion about the registry.
 */
async function table() {
  return waitFor(() => screen.getByRole("table"))
}

beforeEach(() => {
  h.apiFetch.mockReset()
})

describe("HooksSection — the registry", () => {
  it("lists every registered hook with its event, handler kind and target", async () => {
    route()
    renderSection()

    const t = await table()
    expect(within(t).getByText("pre_llm_call")).toBeInTheDocument()
    expect(within(t).getByText("post_tool_call")).toBeInTheDocument()
    expect(within(t).getByText("pre_tool_call")).toBeInTheDocument()

    // The target is what a person needs to judge the hook. Shell commands are
    // shown plainly on purpose — see the component's note.
    expect(screen.getByText("scripts/guard-prompt.sh")).toBeInTheDocument()
    expect(screen.getByText("https://audit.internal/events")).toBeInTheDocument()
  })

  it("says so when there is nothing registered, rather than showing a bare table", async () => {
    route({ hooks: [], journal: [] })
    renderSection()
    await waitFor(() => expect(screen.getByText(/no hooks are registered/i)).toBeInTheDocument())
  })

  it("reports a failed read instead of rendering an empty registry", async () => {
    route({ hooksFail: true })
    renderSection()

    await waitFor(() => expect(screen.getByText(/couldn't read/i)).toBeInTheDocument())
    // "No hooks registered" would be a lie: we do not know that.
    expect(screen.queryByText(/no hooks are registered/i)).not.toBeInTheDocument()
  })
})

describe("HooksSection — retired events", () => {
  it("marks a pre_tool_call row as retired and explains that it can never fire", async () => {
    route()
    renderSection()

    await table()
    const row = rowFor(/pre_tool_call/)
    expect(within(row).getByText(/retired/i)).toBeInTheDocument()
    expect(screen.getByText(/never fires/i)).toBeInTheDocument()
  })

  it("does not mark a live event as retired", async () => {
    route()
    renderSection()

    await waitFor(() => expect(screen.getByText("pre_llm_call")).toBeInTheDocument())
    expect(within(rowFor(/pre_llm_call/)).queryByText(/retired/i)).not.toBeInTheDocument()
  })
})

describe("HooksSection — blocking is a property of the event", () => {
  it("shows blocking for an event that fires at a cancellable call site", async () => {
    route()
    renderSection()
    await waitFor(() => expect(screen.getByText("pre_llm_call")).toBeInTheDocument())
    expect(within(rowFor(/pre_llm_call/)).getByText(/^blocking$/i)).toBeInTheDocument()
  })

  it("reads n/a on a post-event, even when the row says blocking: true", async () => {
    // hk_legacy carries blocking: true in the database and pre_tool_call is not
    // in SupportsBlocking's list — trusting the column would print "blocking"
    // for a hook that cannot block anything.
    route({
      hooks: [{ ...HOOKS[1], id: "hk_post", event: "post_memory_write", blocking: true }],
      journal: [],
    })
    renderSection()

    await waitFor(() => expect(screen.getByText("post_memory_write")).toBeInTheDocument())
    expect(within(rowFor(/post_memory_write/)).getByText(/n\/a/i)).toBeInTheDocument()
  })
})

describe("HooksSection — last result", () => {
  it("takes the newest journal entry per hook, not the first one it sees", async () => {
    route()
    renderSection()

    await waitFor(() => expect(screen.getByText("post_tool_call")).toBeInTheDocument())
    // hk_audit has a pass at 08-28 and an error at 08-29; the error is newer.
    expect(within(rowFor(/post_tool_call/)).getByText(/error/i)).toBeInTheDocument()
    expect(within(rowFor(/pre_llm_call/)).getByText(/pass/i)).toBeInTheDocument()
  })

  it("says a hook has never fired when the journal has nothing for it", async () => {
    route()
    renderSection()
    await table()
    expect(within(rowFor(/pre_tool_call/)).getByText(/never fired/i)).toBeInTheDocument()
  })

  it("still renders the registry when the journal read fails", async () => {
    // The registry is the point of the screen; the outcome column is a bonus.
    route({ journalFail: true })
    renderSection()
    await waitFor(() => expect(screen.getByText("pre_llm_call")).toBeInTheDocument())
    expect(screen.queryByText(/couldn't read the hook registry/i)).not.toBeInTheDocument()
  })
})

describe("HooksSection — the switch", () => {
  it("disables an enabled hook through the disable route", async () => {
    route()
    renderSection("ADMIN")

    await waitFor(() => expect(screen.getByText("pre_llm_call")).toBeInTheDocument())
    fireEvent.click(within(rowFor(/pre_llm_call/)).getByRole("switch"))

    await waitFor(() =>
      expect(h.apiFetch).toHaveBeenCalledWith(
        expect.stringContaining("/api/v1/hooks/hk_guard/disable"),
        expect.objectContaining({ method: "POST" }),
      ),
    )
  })

  it("enables a disabled hook through the enable route", async () => {
    route()
    renderSection("ADMIN")

    await table()
    fireEvent.click(within(rowFor(/pre_tool_call/)).getByRole("switch"))

    await waitFor(() =>
      expect(h.apiFetch).toHaveBeenCalledWith(
        expect.stringContaining("/api/v1/hooks/hk_legacy/enable"),
        expect.objectContaining({ method: "POST" }),
      ),
    )
  })

  it("puts the switch back and says why when the server refuses", async () => {
    route({ toggleFail: true })
    renderSection("ADMIN")

    await waitFor(() => expect(screen.getByText("pre_llm_call")).toBeInTheDocument())
    const sw = within(rowFor(/pre_llm_call/)).getByRole("switch")
    expect(sw).toBeChecked()
    fireEvent.click(sw)

    await waitFor(() => expect(screen.getByText(/couldn't change/i)).toBeInTheDocument())
    expect(within(rowFor(/pre_llm_call/)).getByRole("switch")).toBeChecked()
  })
})

describe("HooksSection — who may change what", () => {
  it("renders the registry read-only for a MEMBER", async () => {
    route()
    renderSection("MEMBER")

    await waitFor(() => expect(screen.getByText("pre_llm_call")).toBeInTheDocument())
    // The rows are the information; the switch is the power. A MEMBER gets the
    // first without the second — GET is open to every workspace member, the
    // enable/disable routes are roleManage.
    expect(screen.queryByRole("switch")).not.toBeInTheDocument()
    expect(screen.getByText(/only owners and admins/i)).toBeInTheDocument()
  })

  it("refuses the switch to a MANAGER, who is below the write tier", async () => {
    route()
    renderSection("MANAGER")
    await waitFor(() => expect(screen.getByText("pre_llm_call")).toBeInTheDocument())
    expect(screen.queryByRole("switch")).not.toBeInTheDocument()
  })

  it("offers the switch to an OWNER", async () => {
    route()
    renderSection("OWNER")
    await waitFor(() => expect(screen.getByText("pre_llm_call")).toBeInTheDocument())
    expect(screen.getAllByRole("switch").length).toBe(HOOKS.length)
  })
})

describe("the nav row", () => {
  // isSettingsSectionVisible alone would pass vacuously: it returns true for
  // any key it does not know about. Asserting against the flattened section
  // list is what actually proves the row is wired into the nav.
  it("is offered at every role, because every workspace member can read the registry", () => {
    for (const role of ["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"]) {
      expect(visibleSettingsSections(role).map((s) => s.key)).toContain("hooks")
      expect(isSettingsSectionVisible("hooks", role)).toBe(true)
    }
  })

  it("is labelled for a person, not for the API", () => {
    const row = visibleSettingsSections("OWNER").find((s) => s.key === "hooks")
    expect(row?.label).toBe("Lifecycle hooks")
  })
})
