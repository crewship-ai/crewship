import React from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { RestrictedRoutines } from "../restricted-routines"
import { RestrictedPages } from "@/components/features/pages/restricted-pages"

// #2877: loading, empty, unavailable and runtime-missing are four exclusive
// states in both private catalogs — never two messages at once — and the
// runtime sentence appears only on the backend's explicit machine code.
const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
afterEach(() => { cleanup(); api.mockReset() })

const runtimeSentence = /Private execution isn't installed on this server/

const screens = [
  { name: "routines", Component: RestrictedRoutines, loading: "Loading routines…", empty: "No routines are available with your current access.", unavailable: "Routine catalog unavailable.", item: [{ slug: "allowed", name: "Allowed routine", definition_hash: "h", execution_hash: "e", inputs: [] }], option: "Allowed routine" },
  { name: "Pages", Component: RestrictedPages, loading: "Loading Page actions…", empty: "No Page actions are available with your current access.", unavailable: "Page actions unavailable.", item: [{ slug: "p", name: "Page", actions: [{ intent_hash: "i", panel_id: "panel", id: "go", label: "Go", inputs: [] }] }], option: "Page — Go" },
]

function json(body: unknown, status: number) { return new Response(JSON.stringify(body), { status }) }

describe.each(screens)("restricted $name catalog", ({ Component, loading, empty, unavailable, item, option }) => {
  function expectOnly(present: string | RegExp) {
    for (const text of [loading, empty, unavailable, runtimeSentence]) {
      if (String(text) === String(present)) continue
      expect(screen.queryByText(text)).toBeNull()
    }
  }

  it("shows only loading while the catalog request is in flight", () => {
    api.mockReturnValue(new Promise(() => {}))
    render(<Component workspaceId="workspace" />)
    expect(screen.getByText(loading)).toBeTruthy()
    expectOnly(loading)
  })

  it("shows only the empty sentence for a successful empty catalog", async () => {
    api.mockResolvedValue(json([], 200))
    render(<Component workspaceId="workspace" />)
    await screen.findByText(empty)
    expectOnly(empty)
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("shows the catalog for a successful non-empty catalog", async () => {
    api.mockResolvedValue(json(item, 200))
    render(<Component workspaceId="workspace" />)
    await screen.findByRole("option", { name: option })
    expect(screen.queryByText(empty)).toBeNull()
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it.each([
    ["an opaque 404", () => json({ error: "unavailable" }, 404)],
    ["a 503 without the runtime code", () => json({ error: "restricted workflow unavailable" }, 503)],
    ["a 500", () => json({ error: "Internal server error" }, 500)],
    ["a 503 with a non-JSON body", () => new Response("bad gateway", { status: 503 })],
  ])("shows only the unavailable alert for %s", async (_, reply) => {
    api.mockResolvedValue(reply())
    render(<Component workspaceId="workspace" />)
    expect((await screen.findByRole("alert")).textContent).toBe(unavailable)
    expectOnly(unavailable)
  })

  it("shows only the unavailable alert when the request itself fails", async () => {
    api.mockRejectedValue(new TypeError("network down"))
    render(<Component workspaceId="workspace" />)
    expect((await screen.findByRole("alert")).textContent).toBe(unavailable)
    expectOnly(unavailable)
  })

  it("names the missing runtime only on the explicit backend code", async () => {
    api.mockResolvedValue(json({ error: "private execution is not installed on this server", code: "restricted_runtime_unavailable" }, 503))
    render(<Component workspaceId="workspace" />)
    await screen.findByText(runtimeSentence)
    expectOnly(runtimeSentence)
    expect(screen.queryByRole("alert")).toBeNull()
  })
})
