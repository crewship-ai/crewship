/**
 * Content, on an ordinary panel Page — the independent review's U02.
 *
 * *"Běžný případ musí vypadat celistvě, ne jako ukázka s vypnutými funkcemi."*
 * That is not a styling note; it is the acceptance test for this section, and
 * it decomposes into assertions a regression can actually trip:
 *
 *  · The complete case renders completely — identity, address, the panels with
 *    their type, producer and state, a Save that says what it changes, and the
 *    application offer as an offer.
 *  · The application half is ABSENT, not disabled. An empty tab, a publication
 *    checklist or a greyed-out Publish on a Page that has no application is
 *    the exact failure U02 names, and each is asserted absent here.
 *  · `mayEditDocument: false` closes the document editor and nothing else. The
 *    old surface-wide `canEdit` took Access and History with it (V01), and the
 *    metadata form has no business being gated by a sealed panel: `PATCH
 *    /pages/{slug}` carries no panel list.
 *  · A refused save keeps what was typed (#1563 rule 3) and reports the
 *    server's own sentence (rule 2).
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor, within } from "@testing-library/react"

// ── The two integration seams ──────────────────────────────────────────────
//
// `PageFactsCard` is being exported from page-settings.tsx by another stream,
// and `application-review.tsx` is being written by S6. Both are mocked so this
// suite's verdict is about THIS file, not about how far the other streams got.

vi.mock("@/components/features/pages/page-settings", () => ({
  PageFactsCard: ({ slug }: { slug: string }) => <div data-slot="page-facts">facts for {slug}</div>,
}))

vi.mock("@/components/features/pages/editor/application-review", () => ({
  EditorApplicationReview: ({ slug }: { slug: string }) => (
    <div data-slot="application-review">review of {slug}</div>
  ),
}))

// The YAML document editor is the real one in the product and a CodeMirror
// mount in a test. Only the fact that Edit OPENS it is this file's business.
vi.mock("@/components/features/pages/page-editor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/components/features/pages/page-editor")>()),
  PageEditor: ({ mode }: { mode: string }) => <div data-slot="page-document-editor">document editor ({mode})</div>,
}))

import { EditorContentSection } from "@/components/features/pages/editor/section-content"
import { derivePageCapabilities } from "@/components/features/pages/editor/use-page-capabilities"
import type { PageCapabilities } from "@/lib/pages/editor-contract"
import type { WirePageDetail } from "@/hooks/use-page-grants"

// ── Fixtures ───────────────────────────────────────────────────────────────

/** An ordinary panel Page: no application, one panel with data and one that
 *  has never received any. `has_application: false` matters — the capability
 *  derivation reads a MISSING flag as "yes" on purpose. */
const PANEL_PAGE: WirePageDetail = {
  id: "cpage1",
  slug: "fleet-overview",
  name: "Fleet overview",
  description: "Services and container memory",
  owner: "crew/lookout",
  has_application: false,
  created_at: "2026-07-01T08:00:00Z",
  updated_at: "2026-08-10T08:00:00Z",
  panels: [
    {
      id: "sluzby",
      schema: "status.v1",
      title: "Services",
      owner: "crew/lookout",
      producer: "routine/nightly",
      sla_seconds: 300,
      span: 8,
      state: "fresh",
      data: { items: [] },
      provenance: { producer: "routine/nightly", run_id: "r1", produced_at: "2026-08-12T11:58:00Z" },
    },
    {
      id: "memory",
      schema: "metric.v1",
      title: "Memory",
      owner: "crew/lookout",
      producer: "script/collector",
      sla_seconds: 60,
      span: 4,
      state: "never_produced",
    },
  ],
}

const SEALED_PAGE: WirePageDetail = {
  ...PANEL_PAGE,
  panels: [
    ...(PANEL_PAGE.panels as object[]),
    { panel_id: "secret", sealed: true, span: 12, owner_crew_name: "Finance" },
  ] as WirePageDetail["panels"],
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
  page?: WirePageDetail
  capabilities?: Partial<PageCapabilities>
  /** Answer for `PATCH /api/v1/pages/{slug}`. */
  patch?: Response
  /** Answer for `GET …/project/preview` — the application-hosting probe. */
  probe?: Response
}

function mount(harness: Harness = {}) {
  const page = harness.page ?? PANEL_PAGE
  const calls: Array<{ method: string; url: string; body: unknown }> = []
  const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method ?? "GET").toUpperCase()
    calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : null })
    if (url.includes("/project/preview")) {
      return harness.probe ?? jsonResponse(404, { error: "page has no project draft" })
    }
    if (method === "PATCH") return harness.patch ?? jsonResponse(200, { slug: page.slug })
    return jsonResponse(404, { error: `unrouted ${method} ${url}` })
  })
  vi.stubGlobal("fetch", mockFetch)

  const onNavigate = vi.fn()
  const onDirtyChange = vi.fn()
  const onPaneChange = vi.fn()
  const capabilities: PageCapabilities = { ...derivePageCapabilities(page), ...harness.capabilities }

  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <EditorContentSection
        workspaceId="ws-1"
        slug={page.slug!}
        page={page}
        capabilities={capabilities}
        onNavigate={onNavigate}
        pane="section"
        onPaneChange={onPaneChange}
        onDirtyChange={onDirtyChange}
      />
    </QueryClientProvider>,
  )
  return { calls, onNavigate, onDirtyChange }
}

function panelRows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>("[data-slot='page-panel-row']"))
}

beforeEach(() => cleanup())
afterEach(() => vi.unstubAllGlobals())

// ── 1. The ordinary Page is complete ───────────────────────────────────────

describe("an ordinary panel Page renders as a complete product", () => {
  it("shows the identity, the address and the derived facts", () => {
    mount()

    expect((screen.getByLabelText("Page name") as HTMLInputElement).value).toBe("Fleet overview")
    expect((screen.getByLabelText("Description") as HTMLTextAreaElement).value).toBe(
      "Services and container memory",
    )
    // The address is a fact, not a field: the server refuses a slug change
    // through PATCH because every producer pushes to that address.
    expect(screen.getByText("/pages/fleet-overview")).toBeTruthy()
    expect(screen.getByRole("button", { name: /copy address/i })).toBeTruthy()
    // The derived facts come from the shared card, not from a second copy of it.
    expect(document.querySelector("[data-slot='page-facts']")).toBeTruthy()
  })

  it("lists every panel with its type, its producer and its state", () => {
    mount()
    const rows = panelRows()
    expect(rows).toHaveLength(2)

    const services = rows[0]
    expect(services.textContent).toContain("Services")
    expect(services.textContent).toContain("status.v1")
    expect(services.textContent).toContain("routine/nightly")
    expect(services.textContent).toContain("Fresh")

    const memory = rows[1]
    expect(memory.textContent).toContain("Memory")
    expect(memory.textContent).toContain("metric.v1")
    expect(memory.textContent).toContain("script/collector")
    // A panel nothing has ever pushed to says so in words. It must never be a
    // zero, which would read as a measurement (§9b.4).
    expect(memory.textContent).toContain("Never produced")
    expect(memory.textContent).not.toMatch(/\b0\b/)
  })

  it("says the save changes the live Page, at the control", () => {
    mount()
    const save = screen.getByRole("button", { name: /save changes/i })
    expect(save).toBeTruthy()
    expect(
      screen.getByText("Saving changes the live Page for everyone who can see it."),
    ).toBeTruthy()
  })

  it("offers a custom application last, as a secondary offer that promises no one click", () => {
    mount()
    const offer = document.querySelector<HTMLElement>("[data-slot='add-application']")!
    expect(offer).toBeTruthy()
    // Last in the section: an optional extra, not a missing half.
    const section = offer.parentElement!
    expect(section.lastElementChild).toBe(offer)

    const button = within(offer).getByRole("button", { name: /add a custom application/i })
    // A ghost button, never the section's primary action.
    expect(button.dataset.variant).toBe("ghost")

    fireEvent.click(button)
    const text = offer.textContent ?? ""
    expect(text).toContain("not created from here in one click")
    expect(text).toContain("MCP workflow")
    expect(text).toContain("build profile")
    expect(text).toContain("publishes")
  })

  it("opens the document editor from a panel's own Edit control", () => {
    mount()
    fireEvent.click(within(panelRows()[0]).getByRole("button", { name: /^edit$/i }))
    const editor = document.querySelector("[data-slot='page-document-editor']")
    expect(editor?.textContent).toContain("edit")
  })

  it("draws a sealed panel as sealed rather than as an unknown schema", () => {
    mount({ page: SEALED_PAGE })
    const sealed = panelRows().find((r) => r.dataset.sealed === "true")!
    expect(sealed.textContent).toContain("Sealed")
    expect(sealed.textContent).toContain("Finance")
  })
})

// ── 2. The application half is absent, not disabled ────────────────────────

describe("nothing application-shaped appears on a Page that has no application", () => {
  it("renders no application tab, no checklist and no disabled Publish", () => {
    mount()
    // No tabs at all: the four sections are the shell's, and this section adds none.
    expect(screen.queryAllByRole("tab")).toHaveLength(0)
    expect(screen.queryByRole("button", { name: /publish/i })).toBeNull()
    expect(screen.queryByRole("checkbox")).toBeNull()
    const body = document.body.textContent ?? ""
    expect(body).not.toMatch(/checklist/i)
    expect(body).not.toMatch(/I reviewed/i)
    expect(body).not.toMatch(/candidate/i)
    // Nothing on the screen is disabled-and-application-shaped: the only
    // disabled control an ordinary Page may show is Save with nothing to save.
    const disabled = Array.from(document.querySelectorAll<HTMLElement>("[disabled]"))
    for (const el of disabled) {
      expect(el.textContent ?? "").toMatch(/save changes/i)
    }
  })

  it("mounts the review surface only when the Page really has an application", async () => {
    mount({ page: { ...PANEL_PAGE, has_application: true }, capabilities: { hasApplication: true } })
    expect(await screen.findByText("review of fleet-overview")).toBeTruthy()
    // …and it is the whole of Content then: no panel list underneath it.
    expect(panelRows()).toHaveLength(0)
  })
})

// ── 3. Capabilities are per-section, not per-surface ───────────────────────

describe("a Page carrying a panel this viewer may not see", () => {
  it("refuses the document editor with the reason and still allows the metadata", () => {
    const refusal = "This Page carries a panel you may not see."
    mount({
      page: SEALED_PAGE,
      capabilities: { mayEditDocument: false, documentRefusal: refusal, mayEditMetadata: true },
    })

    expect(screen.getByText(refusal)).toBeTruthy()
    expect(screen.getByRole("button", { name: /edit document/i })).toBeDisabled()
    // The list is still there — refusing the save is not a reason to hide what
    // the Page contains.
    expect(panelRows().length).toBeGreaterThan(0)
    // …and the metadata form is untouched by it.
    expect(screen.getByLabelText("Page name")).not.toBeDisabled()
  })

  it("disables the metadata form only when the metadata capability is off", () => {
    mount({ capabilities: { mayEditMetadata: false } })
    expect(screen.getByLabelText("Page name")).toBeDisabled()
    expect(screen.getByRole("button", { name: /save changes/i })).toBeDisabled()
  })
})

// ── 4. Saving name and description ─────────────────────────────────────────

describe("saving the Page's name and description", () => {
  it("issues exactly one PATCH with the two fields, and reports success", async () => {
    const { calls, onDirtyChange } = mount()

    fireEvent.change(screen.getByLabelText("Page name"), { target: { value: "Fleet overview v2" } })
    await waitFor(() => expect(onDirtyChange).toHaveBeenCalledWith(true))

    fireEvent.click(screen.getByRole("button", { name: /save changes/i }))
    await waitFor(() => expect(screen.getByText(/^Saved\./)).toBeTruthy())

    const patches = calls.filter((c) => c.method === "PATCH")
    expect(patches).toHaveLength(1)
    expect(patches[0].url).toContain("/api/v1/pages/fleet-overview")
    expect(patches[0].url).toContain("workspace_id=ws-1")
    // No `panels` and no `slug`: an omitted panel list leaves the stored
    // panels, their gates and their automations exactly as they are.
    expect(patches[0].body).toEqual({
      name: "Fleet overview v2",
      description: "Services and container memory",
    })

    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(false))
  })

  it("keeps the typed values and says the server's own words when the save is refused", async () => {
    const refusal =
      "only the page owner, a workspace admin, or a write grantee may edit this page"
    mount({ patch: jsonResponse(403, { error: refusal }) })

    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "typed but unsaved" } })
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }))

    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe(refusal))
    // Rule 3: what was typed is what a retry needs.
    expect((screen.getByLabelText("Description") as HTMLTextAreaElement).value).toBe(
      "typed but unsaved",
    )
    // …and the section is still standing, refusal and all.
    expect(panelRows()).toHaveLength(2)
  })

  it("keeps the typed values when the network drops, and says so differently", async () => {
    const mockFetch = vi.fn(async () => {
      throw new Error("connection reset")
    })
    vi.stubGlobal("fetch", mockFetch)

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
    render(
      <QueryClientProvider client={qc}>
        <EditorContentSection
          workspaceId="ws-1"
          slug="fleet-overview"
          page={PANEL_PAGE}
          capabilities={derivePageCapabilities(PANEL_PAGE)}
          onNavigate={vi.fn()}
          pane="section"
          onPaneChange={vi.fn()}
          onDirtyChange={vi.fn()}
        />
      </QueryClientProvider>,
    )

    fireEvent.change(screen.getByLabelText("Page name"), { target: { value: "Renamed offline" } })
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }))

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("Could not reach the server"))
    expect((screen.getByLabelText("Page name") as HTMLInputElement).value).toBe("Renamed offline")
  })

  it("refuses to submit an empty name rather than letting the server delete it", () => {
    mount()
    fireEvent.change(screen.getByLabelText("Page name"), { target: { value: "   " } })
    expect(screen.getByRole("button", { name: /save changes/i })).toBeDisabled()
    expect(screen.getByText(/A Page needs a name/)).toBeTruthy()
  })
})

// ── 5. The application offer names the real limitation ─────────────────────

describe("the application offer is honest about this installation", () => {
  it("fetches nothing until it is opened", () => {
    const { calls } = mount()
    expect(calls.filter((c) => c.url.includes("/project/preview"))).toHaveLength(0)
  })

  it("names the concrete limitation when the build worker is not configured", async () => {
    const reason = "Page build worker is not configured"
    mount({ probe: jsonResponse(503, { error: reason }) })

    fireEvent.click(screen.getByRole("button", { name: /add a custom application/i }))
    await waitFor(() => expect(screen.getByText(new RegExp(reason))).toBeTruthy())
    expect(document.querySelector("[data-slot='offer-state']")?.textContent).toContain(
      "separate runtime origin",
    )
  })

  it("names the permission when the server refuses the caller", async () => {
    const reason = "reading or editing project sources requires page edit permission"
    mount({ probe: jsonResponse(403, { error: reason }) })

    fireEvent.click(screen.getByRole("button", { name: /add a custom application/i }))
    await waitFor(() => expect(screen.getByText(reason)).toBeTruthy())
  })
})
