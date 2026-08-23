/**
 * Importing a page — the surface, not the hook.
 *
 * Two things are pinned here and they pull in opposite directions.
 *
 * The first is the SHELL. This surface was one of the two that hand-rolled its
 * own `fixed inset-0` overlay with `bg-background/70 backdrop-blur-md`, which
 * meant no focus trap, no Esc, and a frosted page that made Import read as a
 * different application. It now mounts `CreateSurface`, and the assertions
 * below say so in the only terms that survive a refactor: the Radix content
 * node, the one width `sm` is allowed to be, and the absence of the blur.
 *
 * The second is that NOTHING ELSE MOVED. Same local format check, same request
 * body, same rule about when `slug` and `bind` are on the wire at all, and —
 * the reason this surface is worth preserving verbatim — the 422 still arrives
 * as a WORKLIST: every reference the server could not bind, named, with what it
 * is used by, next to the input that overrides it.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))

import { PageImportDialog } from "@/components/features/pages/page-import-dialog"
import type { WirePageBundle } from "@/hooks/use-page-sharing"

// ── Fixtures ───────────────────────────────────────────────────────────────

const BUNDLE: WirePageBundle = {
  format: "crewship-page-bundle/v1",
  page: {
    name: "Fleet health",
    slug: "fleet-health",
    panels: [{ id: "runs" }, { id: "failing" }],
  },
  references: [
    { ref: "crew:platform", kind: "crew", bindable: true, used_by: ["runs", "failing"] },
    { ref: "routine:nightly-sweep", kind: "routine", bindable: true, used_by: ["failing"] },
    // Not bindable: there is no table of scripts to point it at, so it must
    // never appear as a question.
    { ref: "script:sweep.sh", kind: "script", bindable: false, used_by: ["runs"] },
  ],
  metadata: { exported_at: "2026-08-19T10:00:00Z", panel_count: 6 },
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

interface Call {
  method: string
  url: string
  body: Record<string, unknown> | null
}

function mount(install?: Response) {
  const calls: Call[] = []
  const onImported = vi.fn()
  const onClose = vi.fn()

  const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method ?? "GET").toUpperCase()
    let body: Record<string, unknown> | null = null
    if (typeof init?.body === "string") body = JSON.parse(init.body) as Record<string, unknown>
    calls.push({ method, url, body })
    if (url.includes("/pages/import")) return install ?? jsonResponse(200, { slug: "fleet-health" })
    return jsonResponse(404, { error: `unrouted ${method} ${url}` })
  })
  vi.stubGlobal("fetch", mockFetch)

  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <PageImportDialog workspaceId="ws-1" onClose={onClose} onImported={onImported} />
    </QueryClientProvider>,
  )
  return { calls, onImported, onClose }
}

/** Drop a file on the surface's one file input and wait for it to be read. */
async function choose(contents: string, name = "fleet-health.json") {
  const input = document.querySelector<HTMLInputElement>("#page-import-file")!
  expect(input).toBeTruthy()
  const file = new File([contents], name, { type: "application/json" })
  fireEvent.change(input, { target: { files: [file] } })
  await waitFor(() => expect(screen.getByText(name)).toBeTruthy())
}

function importCalls(calls: Call[]) {
  return calls.filter((c) => c.url.includes("/pages/import"))
}

function surface(): HTMLElement {
  return document.querySelector<HTMLElement>('[data-slot="dialog-content"]')!
}

beforeEach(() => cleanup())
afterEach(() => vi.unstubAllGlobals())

// ── 1. The shell ───────────────────────────────────────────────────────────

describe("the shell", () => {
  it("is the shared CreateSurface at the one width `sm` means", () => {
    mount()
    // Radix, not a hand-rolled `fixed inset-0`: this is what buys the focus
    // trap, Esc, the scroll lock and the accessible name.
    const content = surface()
    expect(content).toBeTruthy()
    expect(content.className).toContain("sm:max-w-[480px]")
    // Import is an Import: one question, so `sm` — not one of the eleven
    // widths the twelve surfaces had each picked for themselves.
    expect(content.className).not.toContain("max-w-[560px]")
  })

  it("does not frost the page behind it", () => {
    mount()
    // The blur is the thing people named without prompting. Two surfaces out
    // of twelve had it; this was one of them.
    expect(document.querySelector(".backdrop-blur-md")).toBeNull()
  })

  it("keeps the file input focusable rather than `display: none`", () => {
    mount()
    const input = document.querySelector<HTMLInputElement>("#page-import-file")!
    // `sr-only`, with a label pointing at it. `hidden` takes the input out of
    // the tab order and makes Import mouse-only.
    expect(input.className).toContain("sr-only")
    expect(input.className).not.toContain("hidden")
    expect(document.querySelector('label[for="page-import-file"]')).toBeTruthy()
  })

  it("names itself as one path, from the header", () => {
    mount()
    // "Pages Import a page", not "Import a page": the header's `context ›
    // title` is one DialogTitle, and Radix labels the dialog from it. The
    // shell's `ariaLabel` prop cannot change this — `aria-labelledby` wins
    // over `aria-label` in the accessible-name computation.
    const dialog = screen.getByRole("dialog", { name: /Import a page/ })
    expect(dialog.textContent).toContain("Pages")
  })
})

// ── 2. What the shell now wires that nobody had wired by hand ──────────────

describe("the keyboard and the discard guard", () => {
  it("installs on ⌘↵", async () => {
    const { calls } = mount()
    await choose(JSON.stringify(BUNDLE))

    fireEvent.keyDown(surface(), { key: "Enter", metaKey: true })
    await waitFor(() => expect(importCalls(calls)).toHaveLength(1))
  })

  it("does not install on ⌘↵ before a bundle has been read", async () => {
    const { calls } = mount()
    fireEvent.keyDown(surface(), { key: "Enter", metaKey: true })
    await waitFor(() => expect(importCalls(calls)).toHaveLength(0))
  })

  it("asks before Cancel throws away a bundle that was read", async () => {
    const { onClose } = mount()
    await choose(JSON.stringify(BUNDLE))

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    // Not closed yet — the guard is between the click and the close, on the
    // footer's Cancel as well as on Esc.
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole("button", { name: "Discard" }))
    expect(onClose).toHaveBeenCalled()
  })

  it("closes straight away when the file was refused, because nothing is at stake", async () => {
    const { onClose } = mount()
    await choose("not json", "notes.txt")

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onClose).toHaveBeenCalled()
  })
})

// ── 3. The request ─────────────────────────────────────────────────────────

describe("Install", () => {
  it("is refused until a bundle has been read", () => {
    mount()
    const install = screen.getByRole("button", { name: "Install" })
    expect(install).toBeDisabled()
  })

  it("POSTs the bundle to the workspace, with no slug and no bind", async () => {
    const { calls, onImported } = mount()
    await choose(JSON.stringify(BUNDLE))

    fireEvent.click(screen.getByRole("button", { name: "Install" }))
    await waitFor(() => expect(importCalls(calls)).toHaveLength(1))

    const call = importCalls(calls)[0]
    expect(call.method).toBe("POST")
    expect(call.url).toBe("/api/v1/pages/import?workspace_id=ws-1")
    expect(call.body).toEqual({
      format: BUNDLE.format,
      page: BUNDLE.page,
      references: BUNDLE.references,
    })
    // An unchanged slug is NOT on the wire: sending it would pin the install
    // to a name the person never chose.
    expect(call.body).not.toHaveProperty("slug")
    expect(call.body).not.toHaveProperty("bind")

    await waitFor(() => expect(onImported).toHaveBeenCalledWith("fleet-health"))
  })

  it("sends a slug only when it differs from the bundle's own", async () => {
    const { calls } = mount()
    await choose(JSON.stringify(BUNDLE))

    const slug = screen.getByLabelText("Slug to install under")
    fireEvent.change(slug, { target: { value: "fleet-health-2" } })
    fireEvent.click(screen.getByRole("button", { name: "Install" }))

    await waitFor(() => expect(importCalls(calls)).toHaveLength(1))
    expect(importCalls(calls)[0].body).toMatchObject({ slug: "fleet-health-2" })
  })

  it("sends the overrides a person typed, and asks about nothing unbindable", async () => {
    const { calls } = mount()
    await choose(JSON.stringify(BUNDLE))

    // `script:sweep.sh` travels as a declaration — there is nothing to point
    // it at, so it is not a question.
    expect(screen.queryByLabelText("Bind script:sweep.sh")).toBeNull()

    fireEvent.change(screen.getByLabelText("Bind crew:platform"), { target: { value: "platform-eu" } })
    fireEvent.click(screen.getByRole("button", { name: "Install" }))

    await waitFor(() => expect(importCalls(calls)).toHaveLength(1))
    expect(importCalls(calls)[0].body).toMatchObject({
      bind: { "crew:platform": "platform-eu" },
    })
  })
})

// ── 4. The local format check ──────────────────────────────────────────────

describe("what it refuses before the server sees it", () => {
  it("names the right command when handed a page document", async () => {
    const { calls } = mount()
    await choose(JSON.stringify({ kind: "Page", metadata: { name: "fleet" } }), "page.yaml.json")

    await waitFor(() => expect(screen.getByText(/not an export bundle/i)).toBeTruthy())
    expect(importCalls(calls)).toHaveLength(0)
  })

  it("refuses a bundle that declares the format and carries no page", async () => {
    const { calls } = mount()
    await choose(JSON.stringify({ format: BUNDLE.format }), "truncated.json")

    await waitFor(() => expect(screen.getByText(/carries no page/i)).toBeTruthy())
    expect(importCalls(calls)).toHaveLength(0)
  })

  it("refuses a file that is not JSON at all", async () => {
    const { calls } = mount()
    await choose("not json", "notes.txt")

    await waitFor(() => expect(screen.getByText(/not JSON/i)).toBeTruthy())
    expect(importCalls(calls)).toHaveLength(0)
  })
})

// ── 5. The refusal IS the worklist ─────────────────────────────────────────

describe("a 422", () => {
  it("renders every unresolved reference as a row, not a paragraph", async () => {
    const { calls, onImported } = mount(
      jsonResponse(422, {
        error: "2 references could not be bound",
        unresolved: [
          {
            ref: "routine:nightly-sweep",
            kind: "routine",
            used_by: ["failing"],
            reason: "no routine of that slug exists here",
          },
          {
            ref: "crew:platform",
            kind: "crew",
            used_by: ["runs", "failing"],
            reason: "no crew of that slug exists here",
          },
        ],
      }),
    )
    await choose(JSON.stringify(BUNDLE))
    fireEvent.click(screen.getByRole("button", { name: "Install" }))

    await waitFor(() => expect(importCalls(calls)).toHaveLength(1))

    // The sentence.
    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("2 references could not be bound")
    // And the worklist: each name, each reason, and what it is used by — the
    // whole point of not paraphrasing this into a toast.
    expect(alert.textContent).toContain("routine:nightly-sweep")
    expect(alert.textContent).toContain("no routine of that slug exists here")
    expect(alert.textContent).toContain("failing")
    expect(alert.textContent).toContain("crew:platform")
    expect(alert.textContent).toContain("no crew of that slug exists here")

    // Nothing was written, so nothing opens.
    expect(onImported).not.toHaveBeenCalled()
    // And the form a person retries from is still there, still filled.
    expect(screen.getByLabelText("Bind routine:nightly-sweep")).toBeTruthy()
  })

  it("keeps the bundle so a second attempt is a click, not a re-upload", async () => {
    const { calls } = mount(jsonResponse(500, { error: "the importer fell over" }))
    await choose(JSON.stringify(BUNDLE))
    fireEvent.click(screen.getByRole("button", { name: "Install" }))

    await waitFor(() => expect(importCalls(calls)).toHaveLength(1))
    await screen.findByText(/the importer fell over/i)

    // Rule 3: never destroy the state a retry needs.
    expect(screen.getByRole("button", { name: "Install" })).toBeEnabled()
    expect(screen.getByText("Fleet health")).toBeTruthy()
  })
})
