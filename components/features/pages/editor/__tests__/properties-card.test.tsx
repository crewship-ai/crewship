import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import { PropertiesCard } from "@/components/features/pages/editor/properties-card"
import { derivePageCapabilities } from "@/components/features/pages/editor/use-page-capabilities"
import type { WirePageDetail } from "@/hooks/use-page-grants"

/**
 * The Properties card is where the Page's folder and address moved when the
 * Content form stopped carrying things it does not write. The folder tests
 * came with them from section-content.test.tsx, unchanged in what they claim.
 */

const PAGE: WirePageDetail = {
  id: "cpage1",
  slug: "fleet-overview",
  name: "Fleet overview",
  description: "Services and container memory",
  owner: "crew/lookout",
  owner_crew_name: "Lookout",
  has_application: false,
  has_project: false,
  state: "fresh",
  last_produced_at: "2026-08-12T11:58:00Z",
  panels: [],
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

function mount(page: WirePageDetail = PAGE) {
  const calls: Array<{ method: string; url: string }> = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = (init?.method ?? "GET").toUpperCase()
      calls.push({ method, url })
      if (method === "GET" && url.startsWith("/api/v1/page-folders?")) {
        return jsonResponse(200, {
          folders: [{ id: "f1", slug: "ops", name: "Ops", icon: "rocket", color: "amber", owner: "crew/lookout", page_count: 1, acl_version: 2 }],
        })
      }
      return jsonResponse(404, { error: `unrouted ${method} ${url}` })
    }),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <PropertiesCard workspaceId="ws-1" slug={page.slug!} page={page} capabilities={derivePageCapabilities(page)} />
    </QueryClientProvider>,
  )
  return { calls }
}

beforeEach(() => cleanup())
afterEach(() => vi.unstubAllGlobals())

describe("the Properties card", () => {
  it("says the address as a fact with one control to copy it, and the owner with its kind", () => {
    mount()
    const card = screen.getByTestId("page-properties")
    // The address is a fact, not a field: the server refuses a slug change
    // through PATCH because every producer pushes to that address.
    expect(within(card).getByText("/pages/fleet-overview")).toBeTruthy()
    expect(within(card).getByRole("button", { name: /copy address/i })).toBeTruthy()
    // §7.1 rule 1: the kind is printed, never trimmed off.
    expect(card.textContent).toContain("crew")
    expect(card.textContent).toContain("Lookout")
    expect(card.textContent).toContain("Fresh")
  })

  it("says the Page has no application, or which publication it runs", () => {
    mount()
    expect(screen.getByTestId("page-properties").textContent).toContain("None")
    cleanup()
    mount({ ...PAGE, has_application: true, has_project: true, publication_version: 4 })
    expect(screen.getByTestId("page-properties").textContent).toContain("Publication 4")
  })
})

describe("where the Page is filed", () => {
  it("says the folder with its icon, and Change… opens the move dialog for this Page", async () => {
    const { calls } = mount({ ...PAGE, folder: { slug: "ops", name: "Ops", icon: "rocket", color: "amber" }, pages_version: 3 })
    const line = document.querySelector("[data-slot='page-folder']")!
    expect(line.textContent).toContain("Folder")
    expect(line.textContent).toContain("Ops")
    // The colour sits on the icon itself, as in the picker (audit 2026-09-13, F4).
    const glyph = line.querySelector<HTMLElement>("[data-slot='folder-glyph']")
    expect(glyph).toBeTruthy()
    expect(glyph!.style.color).not.toBe("")
    // Nothing is fetched to draw the line: the folder rides on the record.
    expect(calls.filter((c) => c.url.startsWith("/api/v1/page-folders"))).toHaveLength(0)

    fireEvent.click(within(line as HTMLElement).getByRole("button", { name: "Change…" }))
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByText("Move to folder")).toBeTruthy()
    expect(within(dialog).getByText("Fleet overview")).toBeTruthy()
    // The current folder is preselected, so the primary waits for a change.
    const ops = await within(dialog).findByRole("radio", { name: /^Ops/ })
    expect(ops.getAttribute("aria-checked")).toBe("true")
    expect(within(dialog).getByRole("button", { name: "Move" })).toBeDisabled()
  })

  it("says Unfiled when the Page is in no folder", () => {
    mount({ ...PAGE, folder: null, pages_version: 0 })
    expect(document.querySelector("[data-slot='page-folder']")!.textContent).toContain("Unfiled")
  })

  it("draws no folder row at all when the server does not say", () => {
    mount(PAGE)
    expect(document.querySelector("[data-slot='page-folder']")).toBeNull()
  })
})
