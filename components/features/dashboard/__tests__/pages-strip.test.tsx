/**
 * The Dashboard PAGES strip — PRD §10b.5d, §9b.2 (the header idiom), §9b.3
 * (empty states are instructions) and §9b.4 (the em-dash rule).
 *
 * `usePages` is mocked at the module boundary so this file is a pure render
 * test of `PagesStrip`'s own logic (the recency sort, the freshness hint,
 * which empty sentence fires) — the wire→PageView translation already has
 * its own coverage in `hooks/__tests__` / `components/features/pages/__tests__`.
 */
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, within } from "@testing-library/react"

import { toPageView, type WirePage } from "@/hooks/use-pages"
import { EM_DASH } from "@/components/features/pages/panels/freshness"

const NOW = new Date("2026-08-12T12:00:00Z")

let PAGES: ReturnType<typeof toPageView>[] = []
let LOADING = false
let ERROR: string | null = null

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
}))

vi.mock("@/hooks/use-pages", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-pages")>()
  return {
    ...actual,
    usePages: () => ({ pages: PAGES, loading: LOADING, error: ERROR, refresh: vi.fn() }),
  }
})

import { PagesStrip } from "../pages-strip"

const page = (over: Partial<WirePage>) =>
  toPageView({ id: over.slug ?? "p", slug: "p", name: "P", owner: "crew/lookout", ...over })

function renderStrip() {
  render(<PagesStrip now={NOW} />)
}

/** The card's own hint node — the right-aligned muted word in the header. */
function hint(): string {
  const label = screen.getByText("Pages")
  const header = label.closest("div")!.parentElement!
  return header.querySelector(".text-\\[10px\\]")?.textContent ?? ""
}

describe("PagesStrip", () => {
  beforeEach(() => {
    cleanup()
    PAGES = []
    LOADING = false
    ERROR = null
  })

  it("titles the card PAGES", () => {
    renderStrip()
    expect(screen.getByText("Pages")).toBeInTheDocument()
  })

  it("says 'n stale' once something has gone quiet, matching §9b.2's idiom", () => {
    PAGES = [
      page({
        slug: "fleet",
        name: "Flotila .201",
        panels: [{ id: "a", schema: "status.v1", state: "stale", provenance: { produced_at: "2026-08-12T06:00:00Z" } }],
      }),
    ]
    renderStrip()
    expect(hint()).toContain("1 stale")
  })

  it("says 'all fresh' — the answer, never a repeat of the label — once nothing is stale", () => {
    PAGES = [
      page({
        slug: "close",
        name: "Nightly close",
        panels: [{ id: "a", schema: "metric.v1", state: "fresh", provenance: { produced_at: "2026-08-12T11:00:00Z" } }],
      }),
    ]
    renderStrip()
    expect(hint()).toBe("all fresh")
  })

  it("prints an em dash, never a measured zero, when the index sent no freshness rollup (§9b.4)", () => {
    PAGES = [page({ slug: "a", name: "A", panels: 4 })]
    renderStrip()
    expect(hint()).toBe(EM_DASH)
  })

  it("lists the most recently updated pages first, capped at five", () => {
    PAGES = Array.from({ length: 7 }, (_, i) =>
      page({
        slug: `p${i}`,
        name: `Page ${i}`,
        panels: [{ id: "a", schema: "metric.v1", state: "fresh", provenance: { produced_at: `2026-08-1${i}T00:00:00Z` } }],
      }),
    )
    renderStrip()
    const rows = screen.getAllByRole("link")
    expect(rows).toHaveLength(5)
    // p6 (11 Aug) sorts before p0 (10 Aug) — newest produced_at first.
    expect(within(rows[0]).getByText("Page 6")).toBeInTheDocument()
  })

  it("links each row to its page", () => {
    PAGES = [
      page({
        slug: "close",
        name: "Nightly close",
        panels: [{ id: "a", schema: "metric.v1", state: "fresh", provenance: { produced_at: "2026-08-12T11:00:00Z" } }],
      }),
    ]
    renderStrip()
    expect(screen.getByRole("link", { name: /Nightly close/ })).toHaveAttribute("href", "/pages/close")
  })

  it("never renders a blank card when the workspace has no pages — names the next action (§9b.3)", () => {
    PAGES = []
    renderStrip()
    expect(screen.getByText("No pages yet")).toBeInTheDocument()
    expect(screen.getByText(/crewship page create --file/i)).toBeInTheDocument()
  })

  it("gives a different instruction when pages exist but none has ever received data", () => {
    PAGES = [page({ slug: "a", name: "A", panels: [{ id: "1", schema: "metric.v1", state: "never_produced" }] })]
    renderStrip()
    expect(screen.getByText("Nothing has run yet")).toBeInTheDocument()
    expect(screen.getByText(/crewship page set/i)).toBeInTheDocument()
  })

  it("shows a loading skeleton before the first page arrives", () => {
    LOADING = true
    PAGES = []
    const { container } = render(<PagesStrip now={NOW} />)
    expect(container.querySelectorAll("[data-slot='skeleton']").length).toBeGreaterThan(0)
  })

  it("surfaces a load error instead of an empty-workspace sentence", () => {
    ERROR = "pages: 500"
    renderStrip()
    expect(screen.getByText("pages: 500")).toBeInTheDocument()
  })
})
