/**
 * Tabs on a page — PRD §9 (rendering), §2.3 (one shape for everyone), §3
 * (never colour alone), §4 (a panel that stops reporting says so), §10b.8
 * (print).
 *
 * A tab HIDES panels, which is why most of this file is not about layout. The
 * two assertions that matter most are the ones a reviewer would call cosmetic:
 *
 *  · the header's freshness summary does not move when the tab does — because
 *    if it did, a failing panel on a hidden tab would be silent, and silent old
 *    numbers are the failure this whole feature exists to prevent;
 *  · a tab whose panels are ALL sealed still appears — because dropping it
 *    reflows the page per viewer and its absence would itself say whose data it
 *    held.
 */
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

// The tab in the URL is read through useSearchParams, so a "reload on this
// link" is a render with these params. The global setup mocks the module with
// an empty URLSearchParams; this overrides it with one a test can set.
let currentSearch = new URLSearchParams()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  usePathname: () => "/pages/sit",
  useSearchParams: () => currentSearch,
  useParams: () => ({}),
}))

import { PageView } from "@/components/features/pages/page-view"
import { PAGE_TABS_PRINT_CSS, resolveTabId } from "@/components/features/pages/page-tabs"
import { derivePageTabs, tabSlug, toPageView, type WirePage } from "@/hooks/use-pages"

const NOW = new Date("2026-08-12T12:00:00Z")

/** Two producers, three tabs — the page the owner described. */
const WIRE: WirePage = {
  id: "cpage1",
  slug: "sit",
  name: "Síť",
  owner: "crew/ops",
  panels: [
    {
      id: "dosah",
      schema: "status.v1",
      title: "Dosažitelnost",
      tab: "Síť",
      owner: "crew/ops",
      sla_seconds: 30,
      span: 6,
      state: "fresh",
      data: { items: [{ name: "8.8.8.8", state: "ok", label: "12 ms" }] },
      provenance: { producer: "script/ping-go", run_id: "crun1", produced_at: "2026-08-12T11:59:40Z" },
    },
    {
      id: "latence",
      schema: "metric.v1",
      title: "Odezva 8.8.8.8",
      tab: "Odezva",
      owner: "crew/ops",
      sla_seconds: 30,
      span: 6,
      state: "stale",
      data: { value: 12, unit: "ms" },
      provenance: { producer: "script/ping-go", produced_at: "2026-08-12T11:00:00Z" },
    },
    {
      id: "ztraty",
      schema: "metric.v1",
      title: "Ztrátovost",
      tab: "Odezva",
      owner: "crew/ops",
      sla_seconds: 30,
      span: 6,
      state: "failed",
      reason: "the producer's last push reported a failure",
    },
    {
      id: "disk",
      schema: "metric.v1",
      title: "Místo na disku",
      tab: "Disk",
      owner: "crew/engineering",
      sla_seconds: 300,
      span: 12,
      state: "fresh",
      data: { value: 41, unit: "GB" },
    },
  ],
}

function renderPage(wire: WirePage = WIRE) {
  return render(
    <PageView
      page={toPageView(wire)}
      slug={wire.slug ?? "sit"}
      loading={false}
      error={null}
      notFound={false}
      onBack={vi.fn()}
      now={NOW}
    />,
  )
}

function tabButtons(): HTMLElement[] {
  return screen.getAllByRole("tab")
}

function group(container: HTMLElement, id: string): HTMLElement {
  const el = container.querySelector<HTMLElement>(`[data-slot='tab-group'][data-tab='${id}']`)
  if (!el) throw new Error(`no tab group ${id} in ${container.innerHTML.slice(0, 400)}`)
  return el
}

beforeEach(() => {
  cleanup()
  currentSearch = new URLSearchParams()
})

describe("tabSlug", () => {
  it("addresses a tab without filling the URL with percent-escapes", () => {
    expect(tabSlug("Síť")).toBe("sit")
    expect(tabSlug("Odezva")).toBe("odezva")
    expect(tabSlug("Disk & I/O")).toBe("disk-i-o")
  })
})

describe("derivePageTabs", () => {
  it("orders the bar by FIRST appearance in the panel list", () => {
    const tabs = derivePageTabs(toPageView(WIRE).panels!)
    expect(tabs.map((t) => t.name)).toEqual(["Síť", "Odezva", "Disk"])
    // A name declared twice is one tab, holding both panels in spec order.
    expect(tabs[1].panels.map((p) => p.spec.id)).toEqual(["latence", "ztraty"])
  })

  it("lands an untabbed panel on the first tab, not on one of its own", () => {
    // This is what makes "adding a tab is one word" true: declaring a tab on
    // ONE panel of an existing page must produce a working page.
    const panels = toPageView({
      ...WIRE,
      panels: [
        { id: "sluzby", schema: "status.v1", title: "Jede to?", sla_seconds: 30, state: "fresh" },
        ...(WIRE.panels as object[]),
      ],
    } as WirePage).panels!
    const tabs = derivePageTabs(panels)
    expect(tabs.map((t) => t.name)).toEqual(["Síť", "Odezva", "Disk"])
    expect(tabs[0].panels.map((p) => p.spec.id)).toEqual(["sluzby", "dosah"])
  })

  it("gives each tab the WORST state of its own panels", () => {
    const tabs = derivePageTabs(toPageView(WIRE).panels!)
    expect(tabs[0].state).toBe("fresh")
    // failed outranks stale: a producer that ran and failed is a stated fault.
    expect(tabs[1].state).toBe("failed")
    expect(tabs[2].state).toBe("fresh")
  })

  it("returns nothing for a page where no panel declares a tab", () => {
    const untabbed = toPageView({
      ...WIRE,
      panels: (WIRE.panels as Record<string, unknown>[]).map(({ tab: _tab, ...rest }) => rest),
    } as WirePage)
    expect(derivePageTabs(untabbed.panels!)).toEqual([])
  })
})

describe("resolveTabId", () => {
  const tabs = derivePageTabs(toPageView(WIRE).panels!)

  it("falls back to the first tab for a link naming one this page does not have", () => {
    expect(resolveTabId(tabs, "pamet")).toBe("sit")
    expect(resolveTabId(tabs, null)).toBe("sit")
  })

  it("answers a link written with the visible name rather than the slug", () => {
    expect(resolveTabId(tabs, "Odezva")).toBe("odezva")
  })
})

describe("PageView with tabs", () => {
  it("draws the bar under the breadcrumb, in spec order", () => {
    renderPage()
    expect(tabButtons().map((b) => b.textContent?.trim())).toEqual(["Síť", "Odezva", "Disk"])
  })

  it("shows one tab's panels and hides the rest", () => {
    const { container } = renderPage()
    expect(group(container, "sit").hidden).toBe(false)
    expect(group(container, "odezva").hidden).toBe(true)
    expect(within(group(container, "sit")).getByText("Dosažitelnost")).toBeTruthy()
    expect(within(group(container, "odezva")).getByText("Odezva 8.8.8.8")).toBeTruthy()

    fireEvent.click(screen.getByRole("tab", { name: /Odezva/ }))
    expect(group(container, "sit").hidden).toBe(true)
    expect(group(container, "odezva").hidden).toBe(false)
  })

  it("carries each tab's worst state as a glyph, not as colour alone (§3)", () => {
    renderPage()
    // The icon is the non-colour carrier; the tone is a token beside it.
    const odezva = screen.getByRole("tab", { name: /Odezva/ })
    expect(odezva.querySelector("svg.text-destructive")).toBeTruthy()
    // And the word, so a reader who cannot see the glyph is told which tab is
    // the one that is broken.
    expect(odezva.getAttribute("aria-label")).toBe("Odezva — Failed")
    expect(screen.getByRole("tab", { name: /Síť/ }).getAttribute("aria-label")).toBe("Síť — Fresh")
  })

  it("does NOT change the page's freshness summary when the tab changes", () => {
    // The rule that keeps tabs from undoing §4. The summary is computed over
    // every panel on the page; a page that read FRESH while a hidden tab was
    // failing would be the silent-old-numbers failure with a click in front.
    renderPage()
    const header = screen.getByText("4 panels").parentElement!.parentElement!
    const before = header.textContent

    fireEvent.click(screen.getByRole("tab", { name: /Disk/ }))
    expect(header.textContent).toBe(before)
    // And what it says is the worst panel on the PAGE, which lives on a tab
    // that is now hidden.
    expect(header.textContent).toContain("Failed")

    fireEvent.click(screen.getByRole("tab", { name: /Síť/ }))
    expect(header.textContent).toBe(before)
  })

  it("keeps a tab whose panels are all sealed, with no state it cannot know", () => {
    const wire: WirePage = {
      ...WIRE,
      panels: [
        (WIRE.panels as object[])[0],
        // §11b.14's placeholder, plus the tab — page structure, not data.
        { panel_id: "ucetni", span: 6, sealed: true, owner_crew_name: "Účetní", tab: "Účetnictví" },
      ] as WirePage["panels"],
    }
    const { container } = renderPage(wire)

    const names = tabButtons().map((b) => b.textContent?.trim())
    expect(names).toEqual(["Síť", "Účetnictví"])
    const sealedTab = screen.getByRole("tab", { name: /Účetnictví/ })
    // No glyph: the server sends no state for a panel this viewer may not see,
    // and the bar does not guess one.
    expect(sealedTab.querySelector("svg")).toBeNull()
    expect(sealedTab.getAttribute("aria-label")).toBe("Účetnictví")
    expect(
      within(group(container, "ucetnictvi")).getByText(/Účetní/),
    ).toBeTruthy()
  })

  it("opens on the tab the URL names, and rewrites it on a click", () => {
    currentSearch = new URLSearchParams("tab=disk")
    const { container } = renderPage()
    expect(group(container, "disk").hidden).toBe(false)
    expect(group(container, "sit").hidden).toBe(true)

    fireEvent.click(screen.getByRole("tab", { name: /Odezva/ }))
    expect(new URL(window.location.href).searchParams.get("tab")).toBe("odezva")
  })

  it("renders a page with no tabs exactly as it did before tabs existed", () => {
    const { container } = renderPage({
      ...WIRE,
      panels: (WIRE.panels as Record<string, unknown>[]).map(({ tab: _tab, ...rest }) => rest),
    } as WirePage)
    expect(screen.queryAllByRole("tab")).toHaveLength(0)
    expect(container.querySelectorAll("[data-slot='tab-group']")).toHaveLength(0)
    expect(container.querySelectorAll("[data-slot='panel-cell']")).toHaveLength(4)
  })

  it("prints every tab, under its name, with no bar (§10b.8)", () => {
    // Paper has no tabs. The groups are all in the DOM precisely so print can
    // reveal them; this pins the three rules that do it, because a print
    // regression is invisible until somebody prints.
    expect(PAGE_TABS_PRINT_CSS).toContain("@media print")
    expect(PAGE_TABS_PRINT_CSS).toMatch(/\[data-slot="page-tabs"\]\s*{\s*display:\s*none/)
    expect(PAGE_TABS_PRINT_CSS).toMatch(/\[data-slot="tab-group"\]\s*{\s*display:\s*block/)
    expect(PAGE_TABS_PRINT_CSS).toMatch(/\[data-slot="tab-group-name"\]\s*{\s*display:\s*block/)

    const { container } = renderPage()
    // Every group is mounted, hidden or not — print cannot reveal what was
    // never rendered.
    expect(container.querySelectorAll("[data-slot='tab-group']")).toHaveLength(3)
    expect(container.querySelectorAll("[data-slot='tab-group-name']")).toHaveLength(3)
    expect(container.querySelectorAll("[data-slot='panel-cell']")).toHaveLength(4)
  })
})
