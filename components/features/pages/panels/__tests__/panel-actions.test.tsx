/**
 * Panel actions — PRD `docs/prd/pages.md` §8b, and the four #1563 rules §8b.5
 * says must not come back.
 *
 * Every assertion here is about a property somebody could plausibly break
 * without noticing, because the UI would still look right:
 *
 *  · the request body carrying a routine name (§8b.2 — the wire format is the
 *    allow-list; if a routine can travel, the allow-list is gone),
 *  · a destructive action firing before the host dialog was answered
 *    (§8 rule 5),
 *  · a 429 read as a failure instead of "already running" (§8b.3),
 *  · a 202 toasted as a completion (§8b.3 — the run has not happened yet),
 *  · a refusal that clears the form the retry needs (#1563 rule 3),
 *  · a button on a public page (§7.3.2 rule 1).
 */
import React from "react"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

// The app-wide "what is running right now" feed (§8b.4). Mocked so a test can
// hand back the run the receipt's routine resolves to, without a poll loop.
let activeRuns: Array<{ id: string; pipeline_slug: string }> = []
vi.mock("@/hooks/use-active-routine-runs", () => ({
  useActiveRoutineRuns: () => ({
    runs: activeRuns,
    activeCount: activeRuns.length,
    awaitingApproval: 0,
    bySlug: new Map(activeRuns.map((r) => [r.pipeline_slug, r])),
    recentRuns: [],
    loading: false,
    error: null,
    refresh: () => {},
  }),
}))

// Stubbed so "did we reuse the existing rail?" is checkable as a prop
// assertion rather than by counting the rows it happens to draw today.
vi.mock("@/components/features/activity/pipeline-run-activity", async () => {
  const react = await import("react")
  return {
    PipelineRunActivity: (props: { workspaceId: string; slug: string; runId?: string | null }) =>
      react.createElement("div", {
        "data-slot": "pipeline-run-activity",
        "data-workspace": props.workspaceId,
        "data-pipeline": props.slug,
        "data-run": props.runId ?? "",
      }),
  }
})

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({
    workspaceId: "ws1",
    workspace: null,
    workspaces: [],
    role: null,
    capabilities: null,
    loading: false,
    setWorkspaceId: () => {},
    refresh: async () => {},
  }),
}))

import {
  PanelActionsProvider,
  normalizePanelActions,
  readDispatchAck,
  type PageAction,
} from "@/components/features/pages/panels/panel-actions"
import { PanelRenderer } from "@/components/features/pages/panels/registry"
import { PublicPanelGrid } from "@/components/features/pages/public/public-page-view"
import { PageView } from "@/components/features/pages/page-view"
import { toPageView, toPanelView } from "@/hooks/use-pages"
import type { PanelSpec } from "@/components/features/pages/panels/types"

const SLUG = "fleet-201"
const PANEL: PanelSpec = { id: "sluzby", schema: "metric.v1", title: "Uptime", span: 6 }

/** The 202 the server actually sends (`dispatchReceipt`,
 *  internal/api/pages_actions.go): a pending id and the routine the SERVER
 *  resolved — there is no run yet, because the dispatch path enqueues. */
const RECEIPT = {
  status: "SCHEDULED",
  pending_id: "pend_1",
  deduped: false,
  coalesced: false,
  page: "fleet-201",
  panel: "sluzby",
  action: "restart-api",
  routine: "restart-api",
}

function jsonResponse(status: number, body: unknown, headers?: Record<string, string>): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers(headers),
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response
}

let mockFetch: ReturnType<typeof vi.fn>
let qc: QueryClient

beforeEach(() => {
  activeRuns = []
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
  qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  qc.clear()
})

/** One panel with its actions, rendered exactly the way the page grid renders
 *  it: through the registry, inside the provider `page-view.tsx` mounts. */
function renderPanel(
  actions: readonly PageAction[],
  opts: { panel?: PanelSpec; publicView?: boolean } = {},
) {
  const panel = opts.panel ?? PANEL
  return render(
    <QueryClientProvider client={qc}>
      <PanelActionsProvider
        slug={SLUG}
        workspaceId="ws1"
        actions={new Map([[panel.id, actions]])}
      >
        <PanelRenderer
          panel={panel}
          data={{ state: "fresh", payload: { value: 99, unit: "%" } }}
          publicView={opts.publicView}
        />
      </PanelActionsProvider>
    </QueryClientProvider>,
  )
}

/** The wire shape of §8b.1, plus the fields a compromised producer would like
 *  us to read. `normalizePanelActions` is what stands between them. */
function wireAction(over: Record<string, unknown> = {}): Record<string, unknown> {
  return { id: "restart-api", kind: "call", label: "Restart API", ...over }
}

function action(over: Partial<PageAction> = {}): PageAction {
  return normalizePanelActions([wireAction(over as Record<string, unknown>)])[0]
}

function lastRequest(): { url: string; init: RequestInit } {
  const call = mockFetch.mock.calls.at(-1)!
  return { url: String(call[0]), init: (call[1] ?? {}) as RequestInit }
}

// ── §8b.2 — the button posts an id, and the body carries only inputs ───────

describe("§8b.2 — the wire format has no field a routine could travel in", () => {
  it("posts the action id to the panel's action endpoint with only the inputs", async () => {
    mockFetch.mockResolvedValue(jsonResponse(202, RECEIPT))
    renderPanel([action()])

    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))

    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))
    const { url, init } = lastRequest()
    expect(url).toContain("/api/v1/pages/fleet-201/panels/sluzby/actions/restart-api")
    expect(url).toContain("workspace_id=ws1")
    expect(init.method).toBe("POST")
    // The whole body. Not "contains inputs" — IS inputs, because §8b.2's
    // guarantee is that there is nowhere else for anything to be.
    expect(JSON.parse(String(init.body))).toEqual({ inputs: {} })
    expect(Object.keys(JSON.parse(String(init.body)))).toEqual(["inputs"])
    // One idempotency key per logical click (§8b.3, the Stripe pattern).
    expect(new Headers(init.headers).get("Idempotency-Key")).toBeTruthy()
  })

  it("never sends a routine name, however the wire tries to supply one", async () => {
    mockFetch.mockResolvedValue(jsonResponse(202, RECEIPT))
    const smuggled = normalizePanelActions([
      wireAction({ routine: "drop-database", params: { force: true }, url: "https://evil.example" }),
    ])

    // It never became part of the action object, so it cannot become part of
    // a request: the client has no field to put it in.
    expect(Object.keys(smuggled[0]).sort()).toEqual([
      "confirm",
      "id",
      "inputs",
      "kind",
      "label",
      "ref",
      "style",
      "target",
    ])
    expect(JSON.stringify(smuggled[0])).not.toContain("drop-database")

    renderPanel(smuggled)
    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))

    const { url, init } = lastRequest()
    const body = String(init.body)
    expect(body).not.toContain("routine")
    expect(body).not.toContain("drop-database")
    expect(body).not.toContain("force")
    expect(url).not.toContain("drop-database")
  })

  it("refuses an entry with no id, an unknown kind, or a duplicate id", () => {
    const actions = normalizePanelActions([
      wireAction({ id: "" }),
      wireAction({ id: "a", kind: "exec" }),
      wireAction({ id: "b", kind: "call", label: "First" }),
      wireAction({ id: "b", kind: "call", label: "Shadow" }),
      wireAction({ id: "c", kind: "call", label: "", style: "chartreuse" }),
      "not-an-object",
      null,
    ])
    expect(actions.map((a) => a.id)).toEqual(["b", "c"])
    expect(actions[0].label).toBe("First")
    // No label is a button nobody can describe afterwards; the id is at least
    // the thing the server logged.
    expect(actions[1].label).toBe("c")
    expect(actions[1].style).toBe("default")
  })

  it("reads a run id or a pending id off a 202, and neither off nonsense", () => {
    expect(readDispatchAck(RECEIPT)).toEqual({
      pendingId: "pend_1",
      routine: "restart-api",
      status: "SCHEDULED",
      deduped: false,
    })
    expect(readDispatchAck({ ...RECEIPT, status: "DEDUPED", deduped: true }).deduped).toBe(true)
    expect(readDispatchAck(null)).toEqual({
      pendingId: null,
      routine: null,
      status: null,
      deduped: false,
    })
  })
})

// ── §8 rule 5 / rule 7 — the confirmation is host chrome, and it is rare ───

describe("§8 rule 5 — the confirm dialog is drawn by the host", () => {
  const danger = () =>
    action({
      id: "wipe",
      label: "Wipe cache",
      style: "danger",
      confirm: {
        title: "Wipe the cache?",
        body: "Every cached answer is discarded and the next request is cold.",
        confirmLabel: "Wipe it",
        cancelLabel: "Keep it",
      },
    } as Partial<PageAction>)

  it("does not dispatch a danger action until the dialog is confirmed", async () => {
    mockFetch.mockResolvedValue(jsonResponse(202, RECEIPT))
    renderPanel([danger()])

    fireEvent.click(screen.getByRole("button", { name: "Wipe cache" }))

    // The dialog is up and NOTHING has been sent.
    expect(await screen.findByText("Wipe the cache?")).toBeInTheDocument()
    expect(mockFetch).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "Wipe it" }))
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))
    expect(lastRequest().url).toContain("/actions/wipe")
  })

  it("dispatches nothing when the dialog is dismissed", async () => {
    mockFetch.mockResolvedValue(jsonResponse(202, RECEIPT))
    renderPanel([danger()])

    fireEvent.click(screen.getByRole("button", { name: "Wipe cache" }))
    fireEvent.click(await screen.findByRole("button", { name: "Keep it" }))

    await waitFor(() => expect(screen.queryByText("Wipe the cache?")).not.toBeInTheDocument())
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("draws no dialog for an action that declares no confirm (§8 rule 7)", async () => {
    mockFetch.mockResolvedValue(jsonResponse(202, RECEIPT))
    renderPanel([action()])

    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))

    // Straight through. ~93 % of prompts get approved, so a universal dialog
    // is a rubber stamp; friction belongs where the page spec put it.
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
  })

  it("keeps the confirm text as text — a panel cannot smuggle markup into the host dialog", async () => {
    const injected = normalizePanelActions([
      wireAction({
        id: "wipe",
        confirm: {
          title: "<img src=x onerror=alert(1)>",
          body: "<button>Not our button</button>",
          confirmLabel: "Go",
          cancelLabel: "Stop",
        },
      }),
    ])
    renderPanel(injected)
    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))

    const dialog = await screen.findByRole("alertdialog")
    expect(dialog.querySelector("img")).toBeNull()
    // Two buttons in the footer: ours. The panel's string is a text node.
    expect(within(dialog).getAllByRole("button").map((b) => b.textContent)).toEqual(["Stop", "Go"])
    expect(dialog.textContent).toContain("<button>Not our button</button>")
  })
})

// ── §8b.3 — 429 is "already running", 202 is not a completion ──────────────

describe("§8b.3 — the two answers a dispatch endpoint gives that a button must not flatten", () => {
  it("says already running, with the server's Retry-After, and not a generic error", async () => {
    mockFetch.mockResolvedValue(
      jsonResponse(
        429,
        {
          error: "this action is already running",
          reason: "another dispatch of this action is still queued or in flight",
          pending_id: "pend_1",
          retry_after_seconds: 5,
        },
        { "Retry-After": "5" },
      ),
    )
    const { container } = renderPanel([action()])

    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))

    const status = await waitFor(() => {
      const el = container.querySelector('[data-slot="panel-action-status"]')
      expect(el).not.toBeNull()
      return el!
    })
    expect(status.getAttribute("data-state")).toBe("already-running")
    expect(status.textContent).toBe("this action is already running — try again in 5 s.")
    // A graceful resolution, not a failure: it is announced as a status, and
    // nothing on the panel calls it an error.
    expect(status.getAttribute("role")).toBe("status")
    expect(container.querySelector('[role="alert"]')).toBeNull()
    expect(container.textContent).not.toContain("Request failed")
  })

  it("shows progress after a 202 and never claims the work is finished", async () => {
    mockFetch.mockResolvedValue(jsonResponse(202, RECEIPT))
    const { container } = renderPanel([action()])

    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))

    const status = await waitFor(() => {
      const el = container.querySelector('[data-slot="panel-action-status"]')
      expect(el).not.toBeNull()
      return el!
    })
    expect(status.getAttribute("data-state")).toBe("accepted")
    expect(status.textContent).toContain("has not finished yet")
    expect(container.textContent).not.toContain("— done")
    expect(container.querySelector('[data-slot="panel-action-progress"]')).not.toBeNull()
  })

  it("hands the run to the existing rail once the run feed knows its routine", async () => {
    // The client is never told which routine an action runs (§8b.2), so the
    // slug comes from the server's own active-run feed.
    activeRuns = [{ id: "run_1", pipeline_slug: "restart-api" }]
    mockFetch.mockResolvedValue(jsonResponse(202, RECEIPT))
    const { container } = renderPanel([action()])

    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))

    const rail = await waitFor(() => {
      const el = container.querySelector('[data-slot="pipeline-run-activity"]')
      expect(el).not.toBeNull()
      return el!
    })
    expect(rail.getAttribute("data-pipeline")).toBe("restart-api")
    expect(rail.getAttribute("data-run")).toBe("run_1")
    expect(rail.getAttribute("data-workspace")).toBe("ws1")
    // While that run is live the button says so and cannot be pressed again.
    expect(screen.getByRole("button", { name: "Running…" })).toBeDisabled()
  })
})

// ── #1563 — a refusal says the server's words and destroys nothing ─────────

describe("§8b.5 / #1563 — what a refused write must do", () => {
  const withInputs = () =>
    normalizePanelActions([
      wireAction({
        id: "restart-api",
        inputs: [
          { name: "reason", type: "text", required: true },
          { name: "notes", type: "textarea" },
        ],
      }),
    ])

  it("shows the server's sentence and keeps the collected inputs", async () => {
    mockFetch.mockResolvedValue(jsonResponse(403, { error: "crew lookout may not restart the API" }))
    renderPanel(withInputs())

    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))

    const dialog = await screen.findByRole("dialog")
    const reason = within(dialog).getByLabelText(/reason/i) as HTMLInputElement
    fireEvent.change(reason, { target: { value: "deploy wedged" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Restart API" }))

    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({
      inputs: { reason: "deploy wedged", notes: "" },
    })

    // Rule 2: the server's own words, not a status-mapped guess.
    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toBe("crew lookout may not restart the API")
    // Rule 3: the dialog is still open and the typed value is still in it.
    expect((screen.getByLabelText(/reason/i) as HTMLInputElement).value).toBe("deploy wedged")
    // Re-sending is the submit button, which mints a FRESH idempotency key over
    // whatever the values are now. The same-key "Try again" is deliberately not
    // offered beside an editable form: the server rejects a replayed key whose
    // inputs changed (`actionIdempotency`, the Stripe rule), so a user who
    // corrected a field and pressed it would be told off for our design.
    expect(within(dialog).queryByText("Try again")).toBeNull()
    expect(within(dialog).getByRole("button", { name: "Restart API" })).toBeEnabled()
  })

  it("offers the same-key retry for an action with no form to edit", async () => {
    mockFetch.mockResolvedValue(jsonResponse(500, { error: "the queue is down" }))
    renderPanel([action()])

    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))
    expect((await screen.findByRole("alert")).textContent).toBe("the queue is down")

    const first = new Headers(lastRequest().init.headers).get("Idempotency-Key")
    fireEvent.click(screen.getByText("Try again"))
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(2))
    // The Stripe pattern: a retry of THIS click carries the key the click was
    // issued with, so a refusal the server had already recorded cannot become
    // two runs.
    expect(new Headers(lastRequest().init.headers).get("Idempotency-Key")).toBe(first)
  })

  it("reports a transport failure as a transport failure, not as a refusal", async () => {
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"))
    renderPanel([action()])

    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))

    // Rule 4: `catch` covers transport only, so a dropped connection is never
    // dressed up as something the server said.
    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toBe(
      "The request did not reach the server. Check your connection and try again.",
    )
  })

  it("does not dispatch until a required input is filled in", async () => {
    mockFetch.mockResolvedValue(jsonResponse(202, RECEIPT))
    renderPanel(withInputs())

    fireEvent.click(screen.getByRole("button", { name: "Restart API" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Restart API" }))

    expect(mockFetch).not.toHaveBeenCalled()
    expect(await screen.findByText("reason is required.")).toBeInTheDocument()
  })
})

// ── §8b.1 — the kinds that issue no request ────────────────────────────────

describe("§8b.1 — link, toggle and custom", () => {
  it("builds a link's route from an entity ref and never from a URL", () => {
    const actions = normalizePanelActions([
      wireAction({ id: "open-run", kind: "link", label: "Open the run", ref: { kind: "run", id: "run_8812" } }),
      wireAction({ id: "leak", kind: "link", label: "Elsewhere", ref: { kind: "webhook", id: "x" } }),
      wireAction({ id: "leak2", kind: "link", label: "Also elsewhere", url: "https://evil.example" }),
    ])
    const { container } = renderPanel(actions)

    const links = Array.from(container.querySelectorAll("a"))
    expect(links).toHaveLength(1)
    expect(links[0].getAttribute("href")).toBe("/activity?run=run_8812")
    // A ref whose kind is not in the route table, and an action carrying a
    // URL instead of a ref, both render nothing at all.
    expect(container.textContent).not.toContain("Elsewhere")
    expect(container.innerHTML).not.toContain("evil.example")
  })

  it("hides the panels a toggle targets, locally, and issues no request", () => {
    // `PanelAction.Target` is the panel ids a toggle shows or hides, so this
    // is asserted on the real grid rather than on the button's own state.
    const page = toPageView({
      slug: SLUG,
      name: "Flotila",
      panels: [
        {
          id: "sluzby",
          schema: "metric.v1",
          title: "Uptime",
          state: "fresh",
          data: { value: 99 },
          actions: [
            { id: "compact", kind: "toggle", label: "Compact", target: ["detail", "sluzby"] },
          ],
        },
        { id: "detail", schema: "table.v1", title: "Detail", state: "fresh", data: { rows: [] } },
      ],
    })

    const { container } = render(
      <QueryClientProvider client={qc}>
        <PageView
          page={page}
          slug={SLUG}
          loading={false}
          error={null}
          notFound={false}
          onBack={() => {}}
          workspaceId="ws1"
        />
      </QueryClientProvider>,
    )

    const cells = () =>
      Array.from(container.querySelectorAll('[data-slot="panel-cell"]')).map((c) =>
        c.querySelector("[data-panel-id]")!.getAttribute("data-panel-id"),
      )
    expect(cells()).toEqual(["sluzby", "detail"])

    const button = screen.getByRole("button", { name: "Compact" })
    expect(button).toHaveAttribute("aria-pressed", "false")
    fireEvent.click(button)

    // "detail" is gone; "sluzby" is NOT, even though the spec named it — a
    // toggle that hid its own button would leave no way back.
    expect(cells()).toEqual(["sluzby"])
    expect(screen.getByRole("button", { name: "Compact" })).toHaveAttribute("aria-pressed", "true")

    fireEvent.click(screen.getByRole("button", { name: "Compact" }))
    expect(cells()).toEqual(["sluzby", "detail"])
    // Local only: the whole exchange happened without a request.
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("renders no button for a custom action whose handler this build does not have", () => {
    const { container } = renderPanel(
      normalizePanelActions([wireAction({ id: "x", kind: "custom", label: "Do the thing" })]),
    )
    expect(container.querySelectorAll("button")).toHaveLength(0)
    // And no empty rule across the card where the bar would have been.
    expect(container.querySelector('[data-slot="panel-actions"]')).toBeNull()
  })
})

// ── §7.3.2 rule 1 / §12 v1 / §11b.14 — where a button must never appear ────

describe("no button, on the surfaces that must not have one", () => {
  it("renders none in a public panel grid, even when the wire carries actions", () => {
    // The public wire has no action field — this asserts that a future one
    // could not quietly become a button, which is the whole risk.
    const panels = [
      {
        id: "sluzby",
        schema: "metric.v1",
        title: "Uptime",
        span: 6,
        state: "fresh" as const,
        data: { value: 99, unit: "%" },
        produced_at: "2026-08-12T11:59:40Z",
        actions: [wireAction({ label: "Restart API" })],
      },
    ]
    const { container } = render(
      <PublicPanelGrid
        page={
          {
            name: "Flotila",
            description: null,
            panels,
            generated_at: "2026-08-12T12:00:00Z",
            expires_at: null,
            show_provenance: false,
          } as unknown as React.ComponentProps<typeof PublicPanelGrid>["page"]
        }
        now={new Date("2026-08-12T12:00:00Z")}
      />,
    )
    const grid = container.querySelector('[data-slot="public-panel-grid"]')!
    expect(
      grid.querySelectorAll('button, a[href], input, [role="button"], form'),
    ).toHaveLength(0)
    expect(container.textContent).not.toContain("Restart API")
  })

  it("renders none in a public view even inside a provider that has actions", () => {
    const { container } = renderPanel([action()], { publicView: true })
    expect(container.querySelectorAll("button")).toHaveLength(0)
    expect(container.querySelector('[data-slot="panel-actions"]')).toBeNull()
  })

  it("renders none on narrative.v1, which carries no actions in this release", () => {
    const { container } = renderPanel([action()], {
      panel: { id: "sluzby", schema: "narrative.v1", title: "Shrnutí" },
    })
    expect(container.querySelectorAll("button")).toHaveLength(0)
  })

  it("drops the actions of a sealed panel before they reach the renderer", () => {
    const view = toPanelView({
      panel_id: "hidden",
      sealed: true,
      span: 6,
      owner_crew_name: "Účetní",
      actions: [wireAction()],
    })
    expect(view.actions).toEqual([])
  })

  it("carries a normal panel's actions through the normaliser", () => {
    const view = toPanelView({
      id: "sluzby",
      schema: "metric.v1",
      state: "fresh",
      actions: [wireAction()],
    })
    expect(view.actions.map((a) => a.id)).toEqual(["restart-api"])
  })
})
