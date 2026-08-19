/**
 * Liveness — the two cues, and everything each of them refuses to claim
 * (epic #1935, PRD §4 / §9 / §10b.5b).
 *
 *   1. A panel flashes ONCE when its payload changed. Not on a timer, not on
 *      mount, not when the same bytes arrive again, and never on a panel whose
 *      freshness verdict is not `fresh`.
 *   2. The header indicator is lit while this page's channel is subscribed on a
 *      live socket, and visibly out otherwise — because this surface has no
 *      poll backstop, so a dropped socket means the numbers stop moving with
 *      nothing else on screen saying so.
 *
 * The assertions below are the ones that catch a cue quietly turning into a
 * heartbeat: a heartbeat passes "it animates" and fails "the same payload does
 * not flash".
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, act } from "@testing-library/react"

import {
  PageView,
  PANEL_ARRIVAL_CSS,
  PANEL_ARRIVAL_MS,
  panelArrivalSignature,
} from "@/components/features/pages/page-view"
import { LiveIndicator, pageLiveness } from "@/components/features/pages/live-indicator"
import { toPageView, toPanelView, type WirePage } from "@/hooks/use-pages"

const NOW = new Date("2026-08-12T12:00:00Z")

/** One panel, so a payload can be changed under a stable panel id. */
function wire(over: {
  state?: string
  data?: unknown
  producedAt?: string
}): WirePage {
  return {
    id: "cpage1",
    slug: "flotila",
    name: "Flotila",
    owner: "crew/lookout",
    panels: [
      {
        id: "sluzby",
        schema: "metric.v1",
        title: "Uptime",
        sla_seconds: 30,
        span: 6,
        state: over.state ?? "fresh",
        data: over.data,
        provenance: {
          producer: "script/watch.sh",
          run_id: "crun1",
          produced_at: over.producedAt ?? "2026-08-12T11:59:40Z",
        },
      },
    ],
  }
}

function renderPage(page: WirePage) {
  return render(
    <PageView
      page={toPageView(page)}
      slug="flotila"
      loading={false}
      error={null}
      notFound={false}
      onBack={vi.fn()}
      now={NOW}
    />,
  )
}

function arrival(container: HTMLElement): string | null {
  return container.querySelector("[data-slot='panel-cell']")!.getAttribute("data-panel-arrival")
}

describe("panelArrivalSignature", () => {
  const sig = (over: Parameters<typeof wire>[0]) =>
    panelArrivalSignature(toPanelView(wire(over).panels![0]))

  it("is the payload, and only the payload", () => {
    // Same numbers, a later run: the same signature, because a producer that
    // ran again and found nothing new is not an arrival.
    expect(sig({ data: { value: 3 } })).toBe(
      sig({ data: { value: 3 }, producedAt: "2026-08-12T11:59:59Z" }),
    )
    expect(sig({ data: { value: 3 } })).not.toBe(sig({ data: { value: 4 } }))
  })

  it("refuses any panel the freshness verdict does not call fresh", () => {
    for (const state of ["stale", "failed", "never_produced"]) {
      expect(sig({ state, data: { value: 3 } })).toBeNull()
    }
    // A state this build cannot read normalises to never_produced, never to
    // fresh — so an unknown verdict is silent too.
    expect(sig({ state: "who-knows", data: { value: 3 } })).toBeNull()
  })

  it("refuses a panel that arrived with nothing (the em-dash rule)", () => {
    expect(sig({ data: undefined })).toBeNull()
    expect(sig({ data: null })).toBeNull()
    expect(sig({ data: {} })).toBeNull()
    expect(sig({ data: [] })).toBeNull()
    expect(sig({ data: "   " })).toBeNull()
    // …but a measured zero IS data (§9b.4: `0` is a number we looked up).
    expect(sig({ data: { value: 0 } })).not.toBeNull()
  })
})

describe("the arrival flash", () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    cleanup()
  })

  it("does not flash on mount — opening a page is not an arrival", () => {
    const { container } = renderPage(wire({ data: { value: 1 } }))
    expect(arrival(container)).toBe("idle")
  })

  it("flashes exactly once when the payload changes, then decays", () => {
    const { container, rerender } = renderPage(wire({ data: { value: 1 } }))
    expect(arrival(container)).toBe("idle")

    act(() => {
      rerender(
        <PageView
          page={toPageView(wire({ data: { value: 2 } }))}
          slug="flotila"
          loading={false}
          error={null}
          notFound={false}
          onBack={vi.fn()}
          now={NOW}
        />,
      )
    })
    expect(arrival(container)).toBe("flash")

    // Still lit part-way through, so this pins a decay rather than a blink.
    act(() => void vi.advanceTimersByTime(PANEL_ARRIVAL_MS - 1))
    expect(arrival(container)).toBe("flash")

    act(() => void vi.advanceTimersByTime(1))
    expect(arrival(container)).toBe("idle")

    // And it stays out: nothing re-arms it but new data.
    act(() => void vi.advanceTimersByTime(PANEL_ARRIVAL_MS * 5))
    expect(arrival(container)).toBe("idle")
  })

  it("does not flash when the same payload arrives again", () => {
    const { container, rerender } = renderPage(wire({ data: { value: 1 } }))
    // A fresh push of identical numbers, with a newer produced_at — exactly
    // what a 30s cron over a quiet system sends.
    act(() => {
      rerender(
        <PageView
          page={toPageView(wire({ data: { value: 1 }, producedAt: "2026-08-12T11:59:59Z" }))}
          slug="flotila"
          loading={false}
          error={null}
          notFound={false}
          onBack={vi.fn()}
          now={NOW}
        />,
      )
    })
    expect(arrival(container)).toBe("idle")
  })

  it("keeps a stale panel silent whatever arrives", () => {
    const { container, rerender } = renderPage(wire({ state: "stale", data: { value: 1 } }))
    for (const value of [2, 3, 4]) {
      act(() => {
        rerender(
          <PageView
            page={toPageView(wire({ state: "stale", data: { value } }))}
            slug="flotila"
            loading={false}
            error={null}
            notFound={false}
            onBack={vi.fn()}
            now={NOW}
          />,
        )
      })
      expect(arrival(container)).toBe("idle")
    }
  })

  it("does not flash a panel that arrived with no data", () => {
    const { container, rerender } = renderPage(wire({ data: { value: 1 } }))
    act(() => {
      rerender(
        <PageView
          page={toPageView(wire({ state: "never_produced", data: undefined }))}
          slug="flotila"
          loading={false}
          error={null}
          notFound={false}
          onBack={vi.fn()}
          now={NOW}
        />,
      )
    })
    expect(arrival(container)).toBe("idle")
  })
})

describe("prefers-reduced-motion", () => {
  afterEach(() => cleanup())

  it("removes the motion in CSS and keeps the highlight", () => {
    // jsdom/happy-dom do not evaluate media queries against a stylesheet, so
    // the rule itself is what is asserted — the same way the reduced-motion
    // block for `.agent-active-*` in app/globals.css is written.
    expect(PANEL_ARRIVAL_CSS).toContain("@media (prefers-reduced-motion: reduce)")
    const reduced = PANEL_ARRIVAL_CSS.slice(
      PANEL_ARRIVAL_CSS.indexOf("@media (prefers-reduced-motion: reduce)"),
    )
    expect(reduced).toContain("animation: none")
    // The meaning survives: a steady ring, no ramp, same window.
    expect(reduced).toMatch(/box-shadow:\s*0 0 0 2px/)
  })

  it("ships the rule with the grid rather than relying on a global stylesheet", () => {
    renderPage(wire({ data: { value: 1 } }))
    const sheets = [
      ...Array.from(document.head.querySelectorAll("style")),
      ...Array.from(document.body.querySelectorAll("style")),
    ]
    expect(sheets.some((s) => (s.textContent ?? "").includes("crewship-panel-arrival"))).toBe(true)
  })

  it("says the same thing in the DOM when motion is off", () => {
    // The state is not gated on matchMedia anywhere: the attribute is set the
    // same, and only its painting is suppressed. A reader with motion off gets
    // the highlight; a test, and any future non-visual consumer, gets the fact.
    const matchMedia = vi.fn().mockImplementation((query: string) => ({
      matches: query.includes("prefers-reduced-motion"),
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
      onchange: null,
    }))
    vi.stubGlobal("matchMedia", matchMedia)
    try {
      const { container, rerender } = renderPage(wire({ data: { value: 1 } }))
      rerender(
        <PageView
          page={toPageView(wire({ data: { value: 9 } }))}
          slug="flotila"
          loading={false}
          error={null}
          notFound={false}
          onBack={vi.fn()}
          now={NOW}
        />,
      )
      expect(arrival(container)).toBe("flash")
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

describe("pageLiveness", () => {
  it("is lit only when the socket is up AND this page's channel is registered", () => {
    expect(pageLiveness("connected", "page:cpage1")).toBe("live")
  })

  it("is not lit while the channel is unknown, however healthy the socket is", () => {
    // The first read is still in flight: the socket is fine, but nothing is
    // subscribed to THIS page yet, so nothing would arrive.
    expect(pageLiveness("connected", null)).toBe("connecting")
    expect(pageLiveness("connecting", "page:cpage1")).toBe("connecting")
  })

  it("is out whenever the socket says otherwise", () => {
    expect(pageLiveness("disconnected", "page:cpage1")).toBe("offline")
    expect(pageLiveness("error", "page:cpage1")).toBe("offline")
    // No provider at all (a public surface, a unit test): not "unknown,
    // probably fine" — out.
    expect(pageLiveness(null, "page:cpage1")).toBe("offline")
    expect(pageLiveness(undefined, undefined)).toBe("offline")
  })
})

describe("LiveIndicator", () => {
  afterEach(() => cleanup())

  it("says it is live, and does not animate to say so", () => {
    const { container } = render(<LiveIndicator liveness="live" />)
    const el = container.querySelector("[data-slot='page-liveness']")!
    expect(el.getAttribute("data-liveness")).toBe("live")
    expect(screen.getByText("Live")).toBeTruthy()
    // No permanent pulse anywhere in the indicator: a dot that pulses forever
    // is lit the same on a page whose socket died (epic #1935).
    expect(container.innerHTML).not.toMatch(/animate-|animation:/)
  })

  it("warns that a page with no poll backstop may be showing old numbers", () => {
    render(<LiveIndicator liveness="offline" />)
    const el = screen.getByRole("status")
    expect(el.getAttribute("data-liveness")).toBe("offline")
    expect(el.textContent).toContain("Not live")
    expect(el.textContent).toMatch(/older than it looks/i)
    expect(el.getAttribute("title")).toMatch(/no polling fallback/i)
  })

  it("distinguishes lit from out by more than colour", () => {
    const { container: lit } = render(<LiveIndicator liveness="live" />)
    const { container: out } = render(<LiveIndicator liveness="offline" />)
    expect(lit.textContent).not.toBe(out.textContent)
  })
})

describe("PageView header", () => {
  afterEach(() => cleanup())

  it("carries exactly one indicator, and refuses to claim liveness by default", () => {
    const { container } = renderPage(wire({ data: { value: 1 } }))
    const indicators = container.querySelectorAll("[data-slot='page-liveness']")
    expect(indicators).toHaveLength(1)
    // A caller that passes nothing has no subscription to speak of.
    expect(indicators[0].getAttribute("data-liveness")).toBe("offline")
  })

  it("reports what the caller's subscription says", () => {
    const { container } = render(
      <PageView
        page={toPageView(wire({ data: { value: 1 } }))}
        slug="flotila"
        loading={false}
        error={null}
        notFound={false}
        onBack={vi.fn()}
        now={NOW}
        live="live"
      />,
    )
    expect(
      container.querySelector("[data-slot='page-liveness']")!.getAttribute("data-liveness"),
    ).toBe("live")
  })
})
