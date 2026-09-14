/**
 * Data & actions — the declaration behind each panel (proposal §4,
 * independent review §7).
 *
 * Four properties, each of which the design says out loud and none of which
 * survives on good intentions:
 *
 *  · A panel nothing has ever written to says "Awaiting first data". A zero or
 *    a dash there reads as a measurement, and §9b.4 has exactly one no-data
 *    glyph and one meaning for it.
 *  · "Manage producer access" navigates. There is no token form on this
 *    screen — issuing a producer credential is a permission, and a permission
 *    that can be issued from two places has two audit trails.
 *  · A single ambiguous Save that might be writing the live definition or an
 *    application draft is the failure §4 forbids by name. There is no Save
 *    here at all, and this suite pins that.
 *  · When a candidate declares a different definition, the reader is told
 *    which one they are looking at. A comparison that could not be made is
 *    never rendered as "no changes" (V05).
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

import { EditorDataActionsSection } from "@/components/features/pages/editor/section-data-actions"
import { derivePageCapabilities } from "@/components/features/pages/editor/use-page-capabilities"
import { NO_PAGE_CAPABILITIES, type PageCapabilities } from "@/lib/pages/editor-contract"
import type { WirePageDetail } from "@/hooks/use-page-grants"

// ── Fixtures ───────────────────────────────────────────────────────────────

const PAGE: WirePageDetail = {
  id: "cpage1",
  slug: "fleet-overview",
  name: "Fleet overview",
  owner: "crew/lookout",
  has_application: false,
  has_project: false,
  panels: [
    {
      id: "sluzby",
      schema: "status.v1",
      title: "Services",
      owner: "crew/lookout",
      producer: "routine/nightly",
      sla_seconds: 300,
      state: "fresh",
      data: { items: [] },
      provenance: { producer: "routine/nightly", run_id: "r1", produced_at: "2026-08-12T11:58:00Z" },
      // The authored half, echoed to a caller who may edit the spec. `routine`
      // has no home on the client's PageAction on purpose — a click posts an
      // action id and the server resolves what it runs — so this section reads
      // it straight off the declaration and only displays it.
      actions: [
        {
          id: "restart",
          kind: "call",
          label: "Restart collector",
          routine: "ops-restart",
          confirm: { title: "Restart the collector?", body: "Running services drop for a moment." },
        },
      ],
    },
    {
      id: "memory",
      schema: "metric.v1",
      title: "Memory",
      owner: "crew/lookout",
      producer: "script/collector",
      sla_seconds: 60,
      state: "never_produced",
    },
  ],
}

const APP_PAGE: WirePageDetail = { ...PAGE, has_application: true, has_project: true }

/** Application source that has never been published: `has_application` is
 *  false, and its candidate can still declare a different definition. */
const DRAFT_PAGE: WirePageDetail = { ...PAGE, has_application: false, has_project: true }

/** The candidate's definition, in the document shape `GET …/project` sends. */
function draft(panels: unknown[]) {
  return { revision: 7, source_digest: "sha256:x", definition: { spec: { panels } } }
}

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => null },
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

interface Harness {
  /** `null` stands for a detail read that failed — a 403 or a 500. */
  page?: WirePageDetail | null
  capabilities?: Partial<PageCapabilities>
  /** Answer for `GET …/project` — the candidate's definition. */
  project?: Response
}

function mount(harness: Harness = {}) {
  const page = harness.page === undefined ? PAGE : harness.page
  const calls: Array<{ method: string; url: string }> = []
  const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method ?? "GET").toUpperCase()
    calls.push({ method, url })
    if (url.includes("/project")) {
      return harness.project ?? jsonResponse(404, { error: "page has no project draft" })
    }
    return jsonResponse(404, { error: `unrouted ${method} ${url}` })
  })
  vi.stubGlobal("fetch", mockFetch)

  const onNavigate = vi.fn()
  const onDirtyChange = vi.fn()
  const capabilities: PageCapabilities = page
    ? { ...derivePageCapabilities(page), ...harness.capabilities }
    : { ...NO_PAGE_CAPABILITIES, ...harness.capabilities }

  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <EditorDataActionsSection
        workspaceId="ws-1"
        slug={page?.slug ?? "fleet-overview"}
        page={page}
        capabilities={capabilities}
        onNavigate={onNavigate}
        pane="section"
        onPaneChange={vi.fn()}
        onLeaveEditor={vi.fn()}
        onPageDeleted={vi.fn()}
        onDirtyChange={onDirtyChange}
      />
    </QueryClientProvider>,
  )
  return { calls, onNavigate, onDirtyChange }
}

function panelCard(id: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-slot='panel-data'][data-panel='${id}']`)
  if (!el) throw new Error(`no data card for panel ${id}`)
  return el
}

beforeEach(() => cleanup())
afterEach(() => vi.unstubAllGlobals())

// ── 1. Where the data comes from ───────────────────────────────────────────

describe("each panel says where its data comes from", () => {
  it("names the producer, the last accepted write and the declared interval", () => {
    mount()
    const services = panelCard("sluzby")
    expect(services.textContent).toContain("routine/nightly")
    expect(services.textContent).toContain("Last accepted")
    // The exact instant, not "recently".
    expect(services.textContent).toMatch(/Aug 12, 2026/)
    expect(services.textContent).toContain("Every 5m")
    expect(services.textContent).toContain("stale")
  })

  it("says 'Awaiting first data' for a panel nothing has written to, never a zero", () => {
    mount()
    const memory = panelCard("memory")
    expect(memory.textContent).toContain("Awaiting first data")
    expect(memory.textContent).not.toMatch(/\b0\b/)
    expect(memory.textContent).not.toContain("—")
  })
})

// ── 2. What the panel offers ───────────────────────────────────────────────

describe("each panel says what it offers", () => {
  it("names each action, the routine it calls and its confirm text", () => {
    mount()
    const action = panelCard("sluzby").querySelector<HTMLElement>("[data-slot='declared-action']")!
    expect(action.textContent).toContain("Restart collector")
    expect(action.textContent).toContain("Calls routine ops-restart")
    expect(action.textContent).toContain("Restart the collector?")
    expect(action.textContent).toContain("Running services drop for a moment.")
  })

  it("says an action is a real operation, and offers no way to fire one here", () => {
    mount()
    const services = panelCard("sluzby")
    expect(services.textContent).toContain("real operation, not a preview")
    // The only buttons on a panel card are navigation. Nothing here runs.
    for (const button of Array.from(services.querySelectorAll("button"))) {
      expect(button.textContent).toMatch(/manage producer access/i)
    }
  })

  it("says plainly when a panel declares no actions", () => {
    mount()
    expect(panelCard("memory").textContent).toContain("declares no actions")
  })
})

// ── 3. Access is one place ─────────────────────────────────────────────────

describe("producer access is a permission, and lives in Access", () => {
  it("navigates to Access rather than growing a second form", () => {
    const { onNavigate } = mount()
    fireEvent.click(
      screen.getAllByRole("button", { name: /manage producer access/i })[0],
    )
    expect(onNavigate).toHaveBeenCalledWith("access")
  })

  it("carries no token-minting form", () => {
    mount()
    // No text inputs, no secret, no "create token" anywhere on the section.
    expect(document.querySelectorAll("input")).toHaveLength(0)
    expect(document.querySelectorAll("form")).toHaveLength(0)
    const body = document.body.textContent ?? ""
    expect(body).not.toMatch(/create token/i)
    expect(body).not.toMatch(/producer token/i)
    expect(body).not.toMatch(/secret/i)
  })

  it("holds no save at all, ambiguous or otherwise", () => {
    mount()
    expect(screen.queryByRole("button", { name: /save/i })).toBeNull()
    expect(screen.queryByRole("button", { name: /publish/i })).toBeNull()
  })

  it("reports itself clean, so no other section's dirty flag follows the reader in", () => {
    const { onDirtyChange } = mount()
    expect(onDirtyChange).toHaveBeenCalledWith(false)
    expect(onDirtyChange).not.toHaveBeenCalledWith(true)
  })
})

// ── 4. Live definition versus an application candidate ─────────────────────

describe("the banner says which definition this is", () => {
  it("always states that the live definition is what is described", () => {
    mount()
    const banner = document.querySelector<HTMLElement>("[data-slot='live-definition-banner']")!
    expect(banner.textContent).toContain("live")
    expect(banner.dataset.comparison).toBe("none")
  })

  it("reads nothing about applications on a Page that has none", () => {
    const { calls } = mount()
    expect(calls.filter((c) => c.url.includes("/project"))).toHaveLength(0)
  })

  it("warns, and points at Content, when the candidate declares something else", async () => {
    mount({
      page: APP_PAGE,
      project: jsonResponse(
        200,
        draft([
          // The live Page declares `sluzby` with a 300s SLA and a restart
          // action; the candidate drops the action and renames the panel.
          { id: "sluzby", schema: "status.v1", title: "Service health", owner: "crew/lookout", producer: "routine/nightly", sla: "5m" },
          { id: "memory", schema: "metric.v1", title: "Memory", owner: "crew/lookout", producer: "script/collector", sla: "60s" },
        ]),
      ),
    })

    const banner = document.querySelector<HTMLElement>("[data-slot='live-definition-banner']")!
    await waitFor(() => expect(banner.dataset.comparison).toBe("differs"))
    expect(banner.textContent).toContain("not the same as the live one")

    expect(screen.getByRole("button", { name: /review the candidate in content/i })).toBeTruthy()
  })

  it("does not cry difference over `1h` versus 3600 seconds", async () => {
    mount({
      page: APP_PAGE,
      project: jsonResponse(
        200,
        draft([
          {
            id: "sluzby",
            schema: "status.v1",
            title: "Services",
            owner: "crew/lookout",
            producer: "routine/nightly",
            sla: "5m",
            actions: [{ id: "restart", kind: "call", label: "Restart collector", routine: "ops-restart" }],
          },
          { id: "memory", schema: "metric.v1", title: "Memory", owner: "crew/lookout", producer: "script/collector", sla: "1m" },
        ]),
      ),
    })

    const banner = document.querySelector<HTMLElement>("[data-slot='live-definition-banner']")!
    await waitFor(() => expect(banner.dataset.comparison).toBe("same"))
  })

  it("warns on a first publication too, whose application is only a draft", async () => {
    const { calls } = mount({
      page: DRAFT_PAGE,
      project: jsonResponse(
        200,
        draft([
          { id: "sluzby", schema: "status.v1", title: "Renamed by the candidate", owner: "crew/lookout", producer: "routine/nightly", sla: "5m" },
        ]),
      ),
    })

    // `has_application` is false here — keying the banner on it would leave
    // the one Page whose whole definition is about to change with no warning.
    expect(calls.filter((c) => c.url.includes("/project"))).toHaveLength(1)
    const banner = document.querySelector<HTMLElement>("[data-slot='live-definition-banner']")!
    await waitFor(() => expect(banner.dataset.comparison).toBe("differs"))
  })

  it("says the comparison is unavailable rather than implying there are no changes", async () => {
    mount({
      page: APP_PAGE,
      project: jsonResponse(503, { error: "Page project storage is not configured" }),
    })

    const banner = document.querySelector<HTMLElement>("[data-slot='live-definition-banner']")!
    await waitFor(() => expect(banner.dataset.comparison).toBe("unknown"))
    expect(banner.textContent).toContain("could not be compared")
    expect(banner.textContent).toContain("Page project storage is not configured")
    expect(banner.textContent).not.toMatch(/no changes/i)
  })
})

// ── 5. A Page that could not be read ───────────────────────────────────────

describe("a failed detail read", () => {
  it("says the Page could not be read instead of claiming it declares no panels", () => {
    mount({ page: null })
    const status = screen.getByRole("status")
    expect(status.textContent).toContain("could not be read")
    expect(status.textContent).toContain("not the same as none")
    // F7: the old copy turned a 403 into a statement about the Page's content.
    expect(document.body.textContent).not.toMatch(/declares no panels/)
    expect(document.querySelectorAll("[data-slot='panel-data']")).toHaveLength(0)
    expect(document.querySelector("[data-slot='live-definition-banner']")).toBeNull()
  })
})
