/**
 * A typed artifact never shows the panels empty state (owner, 28 Oct): a
 * Dashboard opened as a Page read "This page declares no panels". The view
 * names where it is read instead.
 */
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"

import { PageView } from "@/components/features/pages/page-view"
import { toPageView } from "@/hooks/use-pages"

const flags = vi.hoisted(() => ({ library: false }))
vi.mock("@/lib/feature-flags", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/feature-flags")>()),
  artifactsLibrary: () => flags.library,
}))

const NOW = new Date("2026-10-28T12:00:00Z")

function renderView(wire: Parameters<typeof toPageView>[0] & { slug: string }, over: Partial<React.ComponentProps<typeof PageView>> = {}) {
  return render(<PageView page={toPageView(wire)} slug={wire.slug} loading={false} error={null} notFound={false} onBack={() => {}} now={NOW} workspaceId="ws1" {...over} />)
}

afterEach(() => { cleanup(); flags.library = false })

describe("PageView for a typed artifact", () => {
  it.each(["project", "dashboard", "slides", "word", "sheet"])("never says a %s declares no panels", (kind) => {
    renderView({ id: "p1", slug: "pipeline", name: "Sales pipeline", panels: [], artifact_kind: kind })
    expect(screen.queryByText(/declares no panels/i)).toBeNull()
    expect(screen.getByText("Can't show this artifact here")).toBeTruthy()
  })

  it("trusts the kind the layout resolved when the detail read sent none", () => {
    renderView({ id: "p1", slug: "pipeline", name: "Sales pipeline", panels: [] }, { artifactKind: "dashboard" })
    expect(screen.queryByText(/declares no panels/i)).toBeNull()
  })

  it("links a versioned kind to its reader with history while the library is off", () => {
    renderView({ id: "p1", slug: "pipeline", name: "Sales pipeline", panels: [], artifact_kind: "dashboard" })
    expect(screen.getByRole("link", { name: "Open in its viewer" }).getAttribute("href")).toBe("/pages/pipeline?workspace_id=ws1&artifact=1")
  })

  it("links every kind to the library detail when the library is on", () => {
    flags.library = true
    renderView({ id: "p1", slug: "brief", name: "Partner brief", panels: [], artifact_kind: "word" })
    expect(screen.getByRole("link", { name: "Open in its viewer" }).getAttribute("href")).toBe("/artifacts?workspace_id=ws1&item=brief")
  })

  it("opens an @<id> reference at the library's ID address and never treats a slug as an ID", async () => {
    const { artifactReaderHref } = await import("../page-view")
    flags.library = true
    expect(artifactReaderHref("@p1", "word", "ws1")).toBe("/artifacts/p1?workspace_id=ws1")
    expect(artifactReaderHref("brief", "word", "ws1")).toBe("/artifacts?workspace_id=ws1&item=brief")
  })

  it("keeps the panels empty state for a legacy Page", () => {
    renderView({ id: "p1", slug: "fleet", name: "Fleet", panels: [] })
    expect(screen.getByText(/declares no panels/i)).toBeTruthy()
  })
})
