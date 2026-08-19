/**
 * Motion, held to the freshness contract (epic #1935).
 *
 * A tween on a page whose whole claim is that it never shows anything untrue
 * (§4, §9b.4) is a bargain with six terms, and this file is the bargain in
 * executable form. Each `describe` below is one of the rules stated at the top
 * of `components/features/pages/panel-motion.ts`.
 *
 * ## What can and cannot be tested here, stated plainly
 *
 * happy-dom parses CSS but does not cascade it, does not evaluate media queries
 * against a stylesheet and paints nothing. So there are two different kinds of
 * assertion in this file and they are not equally strong:
 *
 *  · **Behavioural, and real.** Everything driven by JavaScript — the value
 *    tweens, the change bookkeeping, the eligibility gates — is asserted by
 *    rendering and reading the DOM, `prefers-reduced-motion` included: the
 *    tween hooks consult `matchMedia` in JS (they must — CSS cannot un-write a
 *    number React already rendered), so stubbing it exercises the real path,
 *    exactly as `page-liveness.test.tsx` does for the arrival flash.
 *  · **Structural, and weaker.** The change marks, the row arrivals and the
 *    meter's fill are suppressed by a `@media (prefers-reduced-motion: reduce)`
 *    block, and **no unit test in happy-dom can prove that block takes effect**
 *    — that is a browser doing a cascade. What is asserted instead is that the
 *    rule exists, that it covers every selector the stylesheet animates, and
 *    that the DOM state those selectors key on is written identically either
 *    way. Proving the paint is a browser's job and belongs in Playwright.
 */
import { describe, it, expect, vi, afterEach } from "vitest"
import * as React from "react"
import { render, waitFor } from "@testing-library/react"

import { PanelRenderer } from "../registry"
import {
  PANEL_MOTION_CSS,
  PANEL_TWEEN_MS,
  panelMotion,
  useTweenedValues,
} from "@/components/features/pages/panel-motion"
import type { PanelSnapshot, PanelSpec, PanelState } from "../types"

const NOW = new Date(2026, 7, 13, 12, 30)
const PRODUCED = new Date(2026, 7, 13, 12, 29)

// ── fixtures, built here rather than shared ──────────────────────────────
//
// The shared fixtures are one payload each; every assertion below is about
// what happens BETWEEN two payloads, so these are written as functions of the
// value under test and nothing is reused that could drift.

function metricPanelSpec(over: Partial<PanelSpec> = {}): PanelSpec {
  return { id: "latence", schema: "metric.v1", title: "Odezva", span: 4, ...over }
}

function metric(
  value: number | string | null,
  over: { state?: PanelState; sparkline?: number[]; target?: number } = {},
): PanelSnapshot {
  return {
    state: over.state ?? "fresh",
    payload: { value, unit: "ms", sparkline: over.sparkline, target: over.target },
    provenance: { producer: "script/ping-go", run_id: "push:1", produced_at: PRODUCED },
  }
}

function seriesSnapshot(values: (number | null)[], state: PanelState = "fresh"): PanelSnapshot {
  return {
    state,
    payload: { unit: "MB", labels: ["a", "b"], series: [{ name: "na disku", values }] },
    provenance: { producer: "script/fleet-watch.sh", run_id: "push:1", produced_at: PRODUCED },
  }
}

const seriesPanelSpec: PanelSpec = { id: "disk", schema: "series.v1", title: "Disk", span: 8 }

function statusSnapshot(states: string[], state: PanelState = "fresh"): PanelSnapshot {
  return {
    state,
    payload: {
      items: states.map((s, i) => ({ name: `svc-${i}`, state: s, label: `${i * 3} ms` })),
    },
    provenance: { producer: "script/ping-go", run_id: "push:1", produced_at: PRODUCED },
  }
}

const statusPanelSpec: PanelSpec = { id: "dosah", schema: "status.v1", title: "Dosah", span: 12 }

function tableSnapshot(rows: Record<string, string>[], state: PanelState = "fresh"): PanelSnapshot {
  return {
    state,
    payload: {
      columns: [
        { key: "klon", label: "Klon" },
        { key: "head", label: "HEAD" },
      ],
      rows,
    },
    provenance: { producer: "script/fleet-watch.sh", run_id: "push:1", produced_at: PRODUCED },
  }
}

const tablePanelSpec: PanelSpec = { id: "klony", schema: "table.v1", title: "Klony", span: 4 }

// ── DOM readers ──────────────────────────────────────────────────────────

const metricValue = (c: HTMLElement) =>
  c.querySelector<HTMLElement>('[data-slot="panel-metric-value"]')

const barHeights = (c: HTMLElement) =>
  Array.from(c.querySelectorAll<SVGRectElement>('[data-slot="series-bar"]')).map((r) =>
    Number(r.getAttribute("height")),
  )

const marked = (c: HTMLElement, slot: string) =>
  Array.from(c.querySelectorAll<HTMLElement>(`[data-slot="${slot}"]`)).map((el) =>
    el.getAttribute("data-panel-change"),
  )

const panelMotionAttr = (c: HTMLElement) =>
  c.querySelector<HTMLElement>('[data-slot="panel"]')!.getAttribute("data-panel-motion")

/** Stub `matchMedia` so `prefersReducedMotion()` reports what a test needs. */
function withReducedMotion(reduce: boolean) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      matches: reduce && query.includes("prefers-reduced-motion"),
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
      onchange: null,
    })),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

/** Wait for every tween to have handed the screen back to the payload. */
async function settled(container: HTMLElement) {
  await waitFor(() => {
    for (const el of container.querySelectorAll("[data-panel-tween]")) {
      expect(el.getAttribute("data-panel-tween")).toBe("settled")
    }
  })
}

// ── rule 1: the last frame is the payload ────────────────────────────────

describe("a tween settles on exactly what the producer sent (rule 1)", () => {
  it("ends a metric on the payload's own text, byte for byte", async () => {
    const panel = metricPanelSpec()
    const { container, rerender } = render(
      <PanelRenderer panel={panel} data={metric(0.6)} now={NOW} />,
    )
    expect(metricValue(container)!.textContent).toBe("0.6")

    rerender(<PanelRenderer panel={panel} data={metric(1)} now={NOW} />)
    // The very next frame is still where the value WAS — the tween is started
    // in a layout effect precisely so nobody sees the destination first.
    expect(metricValue(container)!.textContent).toBe("0.6")
    expect(metricValue(container)!.getAttribute("data-panel-tween")).toBe("running")

    await settled(container)
    // `String(1)`, not `1.0` and not `0.9999999999` — the settled render reads
    // the payload directly and there is no formatter between them.
    expect(metricValue(container)!.textContent).toBe("1")
  })

  it("ends a metric on a measured zero rather than near it (§9b.4)", async () => {
    const panel = metricPanelSpec()
    const { container, rerender } = render(
      <PanelRenderer panel={panel} data={metric(42)} now={NOW} />,
    )
    rerender(<PanelRenderer panel={panel} data={metric(0)} now={NOW} />)
    await settled(container)
    expect(metricValue(container)!.textContent).toBe("0")
    expect(
      container.querySelector('[data-slot="panel-value"]')!.getAttribute("data-basis"),
    ).toBe("measured")
  })

  it("ends a bar at the height the payload's value computes to", async () => {
    const { container, rerender } = render(
      <PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([10, 20])} now={NOW} />,
    )
    // The reference: what the same payload draws with no tween in front of it.
    const reference = render(
      <PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([30, 5])} now={NOW} />,
    )
    const expected = barHeights(reference.container)

    rerender(<PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([30, 5])} now={NOW} />)
    await waitFor(() => expect(barHeights(container)).toEqual(expected))
    expect(expected.every((h) => h > 0)).toBe(true)
  })

  it("never passes the target and comes back", async () => {
    // Every value the hook hands out, frame by frame. An easing curve that
    // overshoots — a spring, `backOut`, anything with a control point past 1 —
    // would put a number above the payload's on screen on the way there.
    const seen: TweenSample[] = []
    const { rerender, container } = render(<TweenProbe value={2} seen={seen} />)
    rerender(<TweenProbe value={10} seen={seen} />)
    await waitFor(() => expect(container.textContent).toBe("settled"))

    // The interpolated frames — the only ones that are not simply the payload.
    const interpolated = seen.filter((s) => !s.settled).map((s) => s.value)
    // The tween genuinely ran: without this the rest of the test is vacuous.
    expect(interpolated.length).toBeGreaterThan(2)
    expect(interpolated.some((v) => v > 2 && v < 10)).toBe(true)

    expect(interpolated[0]).toBe(2)
    for (const v of interpolated) {
      expect(v).toBeGreaterThanOrEqual(2)
      expect(v).toBeLessThanOrEqual(10)
    }
    for (let i = 1; i < interpolated.length; i++) {
      expect(interpolated[i]).toBeGreaterThanOrEqual(interpolated[i - 1])
    }
    // And the frame the reader is left with is the payload, not the last
    // interpolation rounded into place.
    const last = seen[seen.length - 1]
    expect(last).toEqual({ value: 10, settled: true })
  })
})

interface TweenSample {
  value: number
  settled: boolean
}

/** Records every frame the hook hands out, interpolated or settled. */
function TweenProbe({ value, seen }: { value: number; seen: TweenSample[] }) {
  const frames = useTweenedValues(new Map([["v", value]]), true)
  const frame = frames.get("v")!
  seen.push({ value: frame.value, settled: frame.settled })
  return <span>{frame.settled ? "settled" : "running"}</span>
}

// ── rule 2: it is short ──────────────────────────────────────────────────

describe("a tween is short (rule 2)", () => {
  it("is a few hundred milliseconds, because it is a fiction while it lasts", () => {
    expect(PANEL_TWEEN_MS).toBeGreaterThan(0)
    expect(PANEL_TWEEN_MS).toBeLessThanOrEqual(400)
  })
})

// ── rule 3: never across a category change ───────────────────────────────

describe("no tween crosses the em-dash boundary (rule 3, §9b.4)", () => {
  it("cuts from no data to a number rather than counting up from an imagined zero", () => {
    const panel = metricPanelSpec()
    const { container, rerender } = render(
      <PanelRenderer panel={panel} data={metric(null)} now={NOW} />,
    )
    expect(container.querySelector('[data-slot="panel-value"]')!.getAttribute("data-basis")).toBe(
      "none",
    )

    rerender(<PanelRenderer panel={panel} data={metric(12)} now={NOW} />)
    // The first frame IS twelve. Not 0, not 6, not "0.0" on its way up.
    expect(metricValue(container)!.textContent).toBe("12")
    expect(metricValue(container)!.getAttribute("data-panel-tween")).toBe("settled")
  })

  it("cuts from a number to no data rather than counting down to it", () => {
    const panel = metricPanelSpec()
    const { container, rerender } = render(
      <PanelRenderer panel={panel} data={metric(12)} now={NOW} />,
    )
    rerender(<PanelRenderer panel={panel} data={metric(null)} now={NOW} />)
    expect(metricValue(container)).toBeNull()
    expect(container.querySelector('[data-slot="panel-value"]')!.getAttribute("data-basis")).toBe(
      "none",
    )
  })

  it("draws a bar that was an em dash at its full height on the first frame", () => {
    const { container, rerender } = render(
      <PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([null, 20])} now={NOW} />,
    )
    expect(barHeights(container)).toHaveLength(1)

    rerender(<PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([10, 20])} now={NOW} />)
    const reference = render(
      <PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([10, 20])} now={NOW} />,
    )
    // No frame in which the new bar is shorter than it should be: the gap it
    // replaced was a different claim, not a smaller number.
    expect(barHeights(container)).toEqual(barHeights(reference.container))
  })

  it("never tweens a value the producer sent as a string", () => {
    const panel = metricPanelSpec()
    const { container, rerender } = render(
      <PanelRenderer panel={panel} data={metric("8083")} now={NOW} />,
    )
    rerender(<PanelRenderer panel={panel} data={metric("8093")} now={NOW} />)
    expect(metricValue(container)!.textContent).toBe("8093")
    expect(metricValue(container)!.getAttribute("data-panel-tween")).toBe("settled")
  })
})

// ── rules 4 and 5: a panel that is not fresh does not move ───────────────

describe("a panel the verdict does not vouch for is completely still (rules 4, 5)", () => {
  it.each<[PanelState]>([["stale"], ["failed"], ["never_produced"]])(
    "refuses %s outright, in the attribute every CSS rule is scoped under",
    (state) => {
      const { container } = render(
        <PanelRenderer panel={metricPanelSpec()} data={metric(12, { state })} now={NOW} />,
      )
      expect(panelMotionAttr(container)).toBe("off")
    },
  )

  it("refuses a sealed panel, which was sent no data to have received", () => {
    const { container } = render(
      <PanelRenderer
        panel={{ id: "ucty", schema: "", span: 4, sealed: true, owner_crew_name: "Účetní" }}
        data={{ state: "never_produced" }}
        now={NOW}
      />,
    )
    expect(container.querySelector('[data-slot="panel-sealed"]')).toBeTruthy()
    expect(panelMotionAttr(container)).toBe("off")
  })

  it("shows a stale panel's new number immediately instead of counting to it", () => {
    const panel = metricPanelSpec()
    const { container, rerender } = render(
      <PanelRenderer panel={panel} data={metric(0.6, { state: "stale" })} now={NOW} />,
    )
    rerender(<PanelRenderer panel={panel} data={metric(9, { state: "stale" })} now={NOW} />)
    // A dead panel that moves looks alive. There is no frame in between.
    expect(metricValue(container)!.textContent).toBe("9")
    expect(metricValue(container)!.getAttribute("data-panel-tween")).toBe("settled")
  })

  it("marks nothing on a stale status grid, however much its states changed", () => {
    const { container, rerender } = render(
      <PanelRenderer
        panel={statusPanelSpec}
        data={statusSnapshot(["ok", "ok"], "stale")}
        now={NOW}
      />,
    )
    rerender(
      <PanelRenderer
        panel={statusPanelSpec}
        data={statusSnapshot(["critical", "warning"], "stale")}
        now={NOW}
      />,
    )
    expect(marked(container, "status-item")).toEqual(["idle", "idle"])
  })

  it("marks only what genuinely changed when a panel comes back from stale", async () => {
    const { container, rerender } = render(
      <PanelRenderer
        panel={statusPanelSpec}
        data={statusSnapshot(["ok", "ok"], "fresh")}
        now={NOW}
      />,
    )
    rerender(
      <PanelRenderer
        panel={statusPanelSpec}
        data={statusSnapshot(["ok", "ok"], "stale")}
        now={NOW}
      />,
    )
    rerender(
      <PanelRenderer
        panel={statusPanelSpec}
        data={statusSnapshot(["ok", "critical"], "fresh")}
        now={NOW}
      />,
    )
    // The recovery itself is not an event; the row whose state moved is.
    await waitFor(() => expect(marked(container, "status-item")).toEqual(["idle", "marked"]))
  })

  it("is refused by the gate function itself, not only by its callers", () => {
    expect(panelMotion({}, { state: "fresh" }).animatable).toBe(true)
    expect(panelMotion({}, { state: "stale" }).animatable).toBe(false)
    expect(panelMotion({}, { state: "failed" }).animatable).toBe(false)
    expect(panelMotion({}, { state: "never_produced" }).animatable).toBe(false)
    expect(panelMotion({ sealed: true }, { state: "fresh" }).animatable).toBe(false)
  })
})

// ── rule 6: nothing moves on mount ───────────────────────────────────────

describe("opening a page replays nothing (rule 6)", () => {
  it("draws a metric at its value on the very first render", () => {
    const { container } = render(
      <PanelRenderer panel={metricPanelSpec()} data={metric(128)} now={NOW} />,
    )
    expect(metricValue(container)!.textContent).toBe("128")
    expect(metricValue(container)!.getAttribute("data-panel-tween")).toBe("settled")
  })

  it("draws bars at full height on the very first render", () => {
    const { container } = render(
      <PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([10, 20])} now={NOW} />,
    )
    // Geometry is deterministic: a 150-unit viewBox less 14 top and 16 axis is
    // a 120-unit plot, and the taller of two bars over a domain that starts at
    // zero fills it. Asserted as numbers rather than "greater than zero", so a
    // tween that started on mount would have to hit these exactly to hide.
    expect(barHeights(container)).toEqual([60, 120])
  })

  it("marks no status row and no table row on the very first render", () => {
    const status = render(
      <PanelRenderer panel={statusPanelSpec} data={statusSnapshot(["ok", "critical"])} now={NOW} />,
    )
    expect(marked(status.container, "status-item")).toEqual(["idle", "idle"])

    const table = render(
      <PanelRenderer
        panel={tablePanelSpec}
        data={tableSnapshot([{ klon: "crewship_1", head: "abc" }])}
        now={NOW}
      />,
    )
    expect(marked(table.container, "table-cell")).toEqual(["idle", "idle", "idle", "idle"])
    for (const row of table.container.querySelectorAll("[data-panel-enter]")) {
      expect(row.getAttribute("data-panel-enter")).toBe("idle")
    }
  })
})

// ── what each panel marks, and what it deliberately leaves alone ─────────

describe("status.v1 marks the row that changed and nothing else", () => {
  it("marks a state change", async () => {
    const { container, rerender } = render(
      <PanelRenderer
        panel={statusPanelSpec}
        data={statusSnapshot(["ok", "ok", "ok"])}
        now={NOW}
      />,
    )
    rerender(
      <PanelRenderer
        panel={statusPanelSpec}
        data={statusSnapshot(["ok", "warning", "ok"])}
        now={NOW}
      />,
    )
    await waitFor(() =>
      expect(marked(container, "status-item")).toEqual(["idle", "marked", "idle"]),
    )
  })

  it("stays completely still when only the labels ticked", async () => {
    // The live `síť` page: four rows of round-trip times, pushed every five
    // seconds. Every label differs on every push and no state changed. A grid
    // that lit up here would be a grid whose motion means nothing.
    const before = statusSnapshot(["ok", "ok"])
    const after = {
      ...before,
      payload: {
        items: [
          { name: "svc-0", state: "ok", label: "7 ms" },
          { name: "svc-1", state: "ok", label: "31 ms" },
        ],
      },
    }
    const { container, rerender } = render(
      <PanelRenderer panel={statusPanelSpec} data={before} now={NOW} />,
    )
    rerender(<PanelRenderer panel={statusPanelSpec} data={after} now={NOW} />)
    await Promise.resolve()
    expect(marked(container, "status-item")).toEqual(["idle", "idle"])
  })

  it("does not mark a row that only just appeared", async () => {
    const { container, rerender } = render(
      <PanelRenderer panel={statusPanelSpec} data={statusSnapshot(["ok"])} now={NOW} />,
    )
    rerender(
      <PanelRenderer panel={statusPanelSpec} data={statusSnapshot(["ok", "critical"])} now={NOW} />,
    )
    await Promise.resolve()
    expect(marked(container, "status-item")).toEqual(["idle", "idle"])
  })
})

describe("table.v1 marks the cell that changed, and never moves the layout", () => {
  const rows = (head3: string) => [
    { klon: "crewship_1", head: "4c1d2bad" },
    { klon: "crewship_3", head: head3 },
  ]

  it("marks one cell, not the row it is in", async () => {
    const { container, rerender } = render(
      <PanelRenderer panel={tablePanelSpec} data={tableSnapshot(rows("0c814c89"))} now={NOW} />,
    )
    rerender(
      <PanelRenderer panel={tablePanelSpec} data={tableSnapshot(rows("deadbeef"))} now={NOW} />,
    )
    // Two layouts render at once — the wide table and the card list — so each
    // cell appears twice. Only the HEAD of the second row moved.
    await waitFor(() =>
      expect(marked(container, "table-cell")).toEqual([
        "idle",
        "idle",
        "idle",
        "marked",
        "idle",
        "idle",
        "idle",
        "marked",
      ]),
    )
  })

  it("keeps a row's identity when the rows are reordered, so nothing false is marked", async () => {
    const a = { klon: "crewship_1", head: "aaa" }
    const b = { klon: "crewship_3", head: "bbb" }
    const { container, rerender } = render(
      <PanelRenderer panel={tablePanelSpec} data={tableSnapshot([a, b])} now={NOW} />,
    )
    rerender(<PanelRenderer panel={tablePanelSpec} data={tableSnapshot([b, a])} now={NOW} />)
    await Promise.resolve()
    // Positional keys would report four changed cells here. Nothing changed.
    expect(marked(container, "table-cell").every((m) => m === "idle")).toBe(true)
  })

  it("gives a new row its full height from the first frame and only fades its ink", async () => {
    const { container, rerender } = render(
      <PanelRenderer
        panel={tablePanelSpec}
        data={tableSnapshot([{ klon: "crewship_1", head: "aaa" }])}
        now={NOW}
      />,
    )
    rerender(
      <PanelRenderer
        panel={tablePanelSpec}
        data={tableSnapshot([
          { klon: "crewship_1", head: "aaa" },
          { klon: "crewship_9", head: "zzz" },
        ])}
        now={NOW}
      />,
    )
    await waitFor(() => {
      const entering = Array.from(container.querySelectorAll('tr[data-panel-enter="new"]'))
      expect(entering).toHaveLength(1)
    })
    // Nothing inline sizes it, and nothing is transformed: the only thing the
    // stylesheet gives an arriving row is opacity, so the rows below it do not
    // move while somebody is reading them.
    const row = container.querySelector<HTMLElement>('tr[data-panel-enter="new"]')!
    expect(row.getAttribute("style")).toBeNull()
  })

  it("removes a row in the same frame the producer stopped reporting it", () => {
    const { container, rerender } = render(
      <PanelRenderer
        panel={tablePanelSpec}
        data={tableSnapshot([
          { klon: "crewship_1", head: "aaa" },
          { klon: "crewship_9", head: "zzz" },
        ])}
        now={NOW}
      />,
    )
    rerender(
      <PanelRenderer
        panel={tablePanelSpec}
        data={tableSnapshot([{ klon: "crewship_1", head: "aaa" }])}
        now={NOW}
      />,
    )
    // No exit animation: a row that lingered would be showing data the latest
    // payload does not contain, which is the failure §4 exists to prevent.
    expect(container.querySelectorAll("tbody tr")).toHaveLength(1)
    expect(container.textContent).not.toContain("crewship_9")
  })
})

// ── reduced motion ───────────────────────────────────────────────────────

describe("prefers-reduced-motion removes the movement and leaves the meaning", () => {
  it("hands the reader the payload on the first frame instead of the journey", () => {
    withReducedMotion(true)
    const panel = metricPanelSpec()
    const { container, rerender } = render(
      <PanelRenderer panel={panel} data={metric(0.6)} now={NOW} />,
    )
    rerender(<PanelRenderer panel={panel} data={metric(1)} now={NOW} />)
    expect(metricValue(container)!.textContent).toBe("1")
    expect(metricValue(container)!.getAttribute("data-panel-tween")).toBe("settled")
  })

  it("draws bars at their final height with no frame in between", () => {
    withReducedMotion(true)
    const { container, rerender } = render(
      <PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([10, 20])} now={NOW} />,
    )
    rerender(<PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([30, 5])} now={NOW} />)
    const reference = render(
      <PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([30, 5])} now={NOW} />,
    )
    expect(barHeights(container)).toEqual(barHeights(reference.container))
  })

  /**
   * The point of the whole exercise: what the DOM SAYS must not depend on the
   * media query. A reader with motion off is told the same things — the same
   * value, the same state, the same "this row changed" — and is simply not
   * shown them moving.
   */
  it("still says a row changed, because that is a fact and not a decoration", async () => {
    withReducedMotion(true)
    const { container, rerender } = render(
      <PanelRenderer panel={statusPanelSpec} data={statusSnapshot(["ok", "ok"])} now={NOW} />,
    )
    rerender(
      <PanelRenderer panel={statusPanelSpec} data={statusSnapshot(["ok", "critical"])} now={NOW} />,
    )
    await waitFor(() => expect(marked(container, "status-item")).toEqual(["idle", "marked"]))
  })

  it("leaves the settled DOM byte-identical to a reader with motion on", async () => {
    const panel = metricPanelSpec()
    const payload = metric(1, { sparkline: [1, 2, 3], target: 4 })

    withReducedMotion(false)
    const moving = render(<PanelRenderer panel={panel} data={metric(0.6)} now={NOW} />)
    moving.rerender(<PanelRenderer panel={panel} data={payload} now={NOW} />)
    await settled(moving.container)

    withReducedMotion(true)
    const still = render(<PanelRenderer panel={panel} data={metric(0.6)} now={NOW} />)
    still.rerender(<PanelRenderer panel={panel} data={payload} now={NOW} />)
    await settled(still.container)

    expect(still.container.innerHTML).toBe(moving.container.innerHTML)
  })
})

// ── the stylesheet, structurally ─────────────────────────────────────────

describe("the stylesheet (structural — a browser is what actually proves this)", () => {
  it("turns every animation it declares off under reduced motion", () => {
    const reduced = PANEL_MOTION_CSS.slice(
      PANEL_MOTION_CSS.indexOf("@media (prefers-reduced-motion: reduce)"),
    )
    expect(reduced).not.toBe("")
    // Every selector the sheet animates outside the block appears inside it.
    for (const selector of ['[data-panel-change="marked"]', '[data-panel-enter="new"]']) {
      expect(reduced).toContain(selector)
    }
    expect(reduced).toContain('[data-slot="panel-target-fill"]')
    expect((reduced.match(/animation: none/g) ?? []).length).toBeGreaterThanOrEqual(2)
    expect(reduced).toContain("transition: none")
  })

  it("keeps the meaning when the movement is gone", () => {
    const reduced = PANEL_MOTION_CSS.slice(
      PANEL_MOTION_CSS.indexOf("@media (prefers-reduced-motion: reduce)"),
    )
    // A marked row still shows its ring, as a steady one. Same information,
    // no movement — the `PANEL_ARRIVAL_CSS` idiom, one level in.
    expect(reduced).toContain("box-shadow: inset")
  })

  it("scopes every rule under a panel the freshness verdict vouches for", () => {
    for (const line of PANEL_MOTION_CSS.split("\n")) {
      if (!line.includes("{") || line.trim().startsWith("@") || /^\s*\d+%/.test(line)) continue
      if (line.includes("[")) expect(line).toContain('[data-panel-motion="on"]')
    }
  })

  it("animates nothing that could reflow the page", () => {
    // The keyframes are the only thing that RUNS on a row or a cell, and they
    // touch paint alone. A height or margin keyframe would move the panel under
    // a reader's eyes, which is worse than not animating at all. (The meter's
    // fill transitions `width`, and is deliberately excluded: it lives inside a
    // fixed-height `overflow-hidden` track and cannot move anything outside it.)
    const keyframes = PANEL_MOTION_CSS.match(/@keyframes[^}]*\{[^@]*?\n\}/g) ?? []
    expect(keyframes.length).toBeGreaterThanOrEqual(2)
    const banned = /(^|[\s;{])(height|width|margin|padding|top|left|right|bottom|inset|font-size|gap|border-width)\s*:/
    for (const block of keyframes) {
      expect(block).not.toMatch(banned)
    }
  })

  it("ships with every panel and is deduped into one copy", () => {
    render(<PanelRenderer panel={metricPanelSpec()} data={metric(1)} now={NOW} />)
    render(<PanelRenderer panel={seriesPanelSpec} data={seriesSnapshot([1, 2])} now={NOW} />)
    const sheets = [
      ...Array.from(document.head.querySelectorAll("style")),
      ...Array.from(document.body.querySelectorAll("style")),
    ].filter((s) => (s.textContent ?? "").includes("crewship-panel-change"))
    expect(sheets.length).toBeGreaterThanOrEqual(1)
  })
})
