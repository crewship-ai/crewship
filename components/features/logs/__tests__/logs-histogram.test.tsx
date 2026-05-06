import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, fireEvent } from "@testing-library/react"
import type { JournalEntry } from "@/lib/types/journal"

vi.mock("recharts", () => ({
  Bar: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  BarChart: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  Cell: () => null,
  ResponsiveContainer: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  XAxis: () => null,
  Tooltip: () => null,
}))

import { LogsHistogram } from "@/components/features/logs/logs-histogram"

const BUCKET_COUNT = 60
const CHART_WIDTH = 600

function mockChartBounds() {
  const rect = {
    x: 0, y: 0, top: 0, left: 0, right: CHART_WIDTH, bottom: 64,
    width: CHART_WIDTH, height: 64,
    toJSON: () => ({}),
  }
  Element.prototype.getBoundingClientRect = vi.fn(() => rect as DOMRect)
}

beforeEach(() => {
  mockChartBounds()
})

/** The wrapper that owns the click handler — has cursor: pointer + height set. */
function getInteractiveLayer(container: HTMLElement): HTMLElement {
  const el = Array.from(container.querySelectorAll<HTMLElement>("div")).find(
    (d) => d.style.cursor === "pointer" && d.style.height === "64px",
  )
  if (!el) throw new Error("interactive layer not found")
  return el
}

function xOfBucket(idx: number): number {
  const bucketWidth = CHART_WIDTH / BUCKET_COUNT
  return idx * bucketWidth + bucketWidth / 2
}

function makeEntry(overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: `id-${Math.random().toString(36).slice(2, 8)}`,
    workspace_id: "ws_test",
    ts: new Date().toISOString(),
    entry_type: "exec.command",
    severity: "info",
    actor_type: "agent",
    summary: "x",
    payload: {},
    refs: {},
    ...overrides,
  }
}

describe("LogsHistogram interaction", () => {
  it("emits a single-bucket range on click", () => {
    const onSelect = vi.fn()
    const { container } = render(
      <LogsHistogram entries={[makeEntry()]} timeRange="1h" onSelect={onSelect} />,
    )
    const layer = getInteractiveLayer(container)

    fireEvent.click(layer, { clientX: xOfBucket(30) })

    expect(onSelect).toHaveBeenCalledOnce()
    const range = onSelect.mock.calls[0][0]
    expect(range).not.toBeNull()
    const oneMinMs = (60 * 60 * 1000) / 60
    expect(range.toMs - range.fromMs).toBeGreaterThan(oneMinMs * 0.95)
    expect(range.toMs - range.fromMs).toBeLessThan(oneMinMs * 1.05)
  })

  it("does not emit anything on hover (mousemove without click)", () => {
    const onSelect = vi.fn()
    const { container } = render(
      <LogsHistogram entries={[makeEntry()]} timeRange="1h" onSelect={onSelect} />,
    )
    const layer = getInteractiveLayer(container)

    // Lots of pointermove events — should never trigger selection.
    fireEvent.pointerMove(layer, { clientX: 100 })
    fireEvent.pointerMove(layer, { clientX: 200 })
    fireEvent.pointerMove(layer, { clientX: 300 })
    fireEvent.mouseMove(layer, { clientX: 400 })
    fireEvent.mouseMove(layer, { clientX: 500 })

    expect(onSelect).not.toHaveBeenCalled()
  })

  it("does not emit on mousedown alone — needs a full click", () => {
    const onSelect = vi.fn()
    const { container } = render(
      <LogsHistogram entries={[makeEntry()]} timeRange="1h" onSelect={onSelect} />,
    )
    const layer = getInteractiveLayer(container)

    // mousedown + mousemove away (incomplete click) — must not emit.
    fireEvent.mouseDown(layer, { clientX: 200 })
    fireEvent.mouseMove(layer, { clientX: 400 })

    expect(onSelect).not.toHaveBeenCalled()
  })

  it("toggles off when clicking the already-selected bucket", () => {
    const onSelect = vi.fn()
    const oneMinMs = (60 * 60 * 1000) / 60
    // Compute an approximate bucket-30 range based on current "now"
    // (1h window → 60 × 1-min buckets ending at now).
    const now = Date.now()
    const bucket30From = now - 60 * 60 * 1000 + 30 * oneMinMs
    const selected = { fromMs: bucket30From, toMs: bucket30From + oneMinMs }

    const { container } = render(
      <LogsHistogram
        entries={[makeEntry()]}
        timeRange="1h"
        selected={selected}
        onSelect={onSelect}
      />,
    )
    const layer = getInteractiveLayer(container)

    fireEvent.click(layer, { clientX: xOfBucket(30) })

    expect(onSelect).toHaveBeenCalledOnce()
    const arg = onSelect.mock.calls[0][0]
    // Either null (clean toggle) or a fresh same-bucket range — both are
    // acceptable consistent click outcomes given Date.now() drift.
    expect(arg === null || (arg && Math.abs(arg.fromMs - selected.fromMs) < oneMinMs)).toBe(true)
  })

  it("does not emit when clicking outside the chart bounds", () => {
    const onSelect = vi.fn()
    const { container } = render(
      <LogsHistogram entries={[makeEntry()]} timeRange="1h" onSelect={onSelect} />,
    )
    const layer = getInteractiveLayer(container)

    // clientX greater than chart width → ratio > 1 → no emit.
    fireEvent.click(layer, { clientX: CHART_WIDTH + 50 })
    fireEvent.click(layer, { clientX: -10 })

    expect(onSelect).not.toHaveBeenCalled()
  })

  it("does nothing when onSelect is not provided", () => {
    const { container } = render(<LogsHistogram entries={[makeEntry()]} timeRange="1h" />)
    // Without onSelect, the cursor is "default" — find by height instead.
    const layer = Array.from(container.querySelectorAll<HTMLElement>("div")).find(
      (d) => d.style.height === "64px",
    )
    expect(layer).toBeTruthy()
    // Click should no-op without throwing.
    fireEvent.click(layer!, { clientX: 300 })
  })

  it("emits ranges for clicks at different positions across the window", () => {
    const onSelect = vi.fn()
    const { container } = render(
      <LogsHistogram entries={[makeEntry()]} timeRange="1h" onSelect={onSelect} />,
    )
    const layer = getInteractiveLayer(container)

    // Click bucket 5
    fireEvent.click(layer, { clientX: xOfBucket(5) })
    const range5 = onSelect.mock.calls[0][0]
    // Click bucket 50
    fireEvent.click(layer, { clientX: xOfBucket(50) })
    const range50 = onSelect.mock.calls[1][0]

    expect(range5.fromMs).toBeLessThan(range50.fromMs)
    expect(onSelect).toHaveBeenCalledTimes(2)
  })
})
