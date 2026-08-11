/**
 * The chain canvas fits its graph to the frame it is drawn in — and keeps
 * fitting it.
 *
 * React Flow's `fitView` prop fits ONCE, at init, and never again: the prop
 * is only read into `fitViewQueued` when its value changes, and ours is a
 * constant `true`. So the transform the graph gets on mount is the transform
 * it keeps. Narrow the frame afterwards — collapse the rail, open a detail
 * pane, resize the window — and the nodes on the right simply leave the box.
 * Measured on a live instance before the fix: the viewport transform stayed
 * `translate(188.5px, 97px) scale(1)` from a 1177px-wide frame down to a
 * 477px one, with three of four nodes outside it.
 *
 * React Flow itself is mocked here. Not to dodge the library, but because
 * jsdom/happy-dom has no layout: a real ReactFlow measures every node as
 * 0×0 and every fit is a no-op, so a test built on it would pass on the
 * broken code too. What IS ours — when we ask for a refit, and when we
 * refuse to — is exactly what the mock exposes. The rest (does fitView
 * actually fit) is verified in a browser against the live instance.
 */

import * as React from "react"
import { render, act } from "@testing-library/react"
import { describe, expect, it, vi, beforeEach } from "vitest"

/* ── the seam: what React Flow hands us, and what we call back ────── */

interface CapturedProps {
  onInit?: (instance: unknown) => void
  onMoveEnd?: (event: unknown, viewport: unknown) => void
  fitView?: boolean
  fitViewOptions?: Record<string, unknown>
  [key: string]: unknown
}

const captured: { flow: CapturedProps | null; controls: CapturedProps | null } = {
  flow: null,
  controls: null,
}

vi.mock("@xyflow/react", () => ({
  ReactFlow: (props: CapturedProps & { children?: React.ReactNode }) => {
    captured.flow = props
    return React.createElement("div", { "data-testid": "flow" }, props.children)
  },
  Background: () => React.createElement("div", { "data-testid": "background" }),
  BackgroundVariant: { Dots: "dots" },
  Controls: (props: CapturedProps) => {
    captured.controls = props
    return React.createElement("div", { "data-testid": "controls" })
  },
}))

vi.mock("@xyflow/react/dist/style.css", () => ({}))

vi.mock("@/components/features/activity/overview-nodes", () => ({
  overviewNodeTypes: {},
}))

/* ── a ResizeObserver the test can drive ──────────────────────────── */

type ROCallback = (entries: { contentRect: { width: number; height: number } }[]) => void

const observers: { cb: ROCallback; el: Element }[] = []

class TestResizeObserver {
  cb: ROCallback
  constructor(cb: ROCallback) {
    this.cb = cb
  }
  observe(el: Element) {
    observers.push({ cb: this.cb, el })
    // The real one fires once for the observation itself, at the size the
    // element already has. That is not a resize, and refitting on it would
    // race the mount fit.
    this.cb([{ contentRect: { width: 900, height: 380 } }])
  }
  unobserve() {}
  disconnect() {}
}

function resizeTo(width: number, height = 380) {
  act(() => {
    for (const o of observers) o.cb([{ contentRect: { width, height } }])
  })
}

const { ChainCanvas } = await import("../chain-canvas")

function nodesFor(...ids: string[]) {
  return ids.map((id) => ({ id, type: "overviewRun", position: { x: 0, y: 0 }, data: {} }))
}

let fitView: ReturnType<typeof vi.fn>

function mount(ids: string[] = ["run:a", "run:b"]) {
  fitView = vi.fn()
  const view = render(
    <ChainCanvas nodes={nodesFor(...ids) as never} edges={[]} />,
  )
  // React Flow reports its instance through onInit once the pane exists.
  act(() => {
    captured.flow?.onInit?.({ fitView })
  })
  return view
}

beforeEach(() => {
  observers.length = 0
  captured.flow = null
  captured.controls = null
  vi.stubGlobal("ResizeObserver", TestResizeObserver)
})

describe("ChainCanvas keeps the graph inside its frame", () => {
  it("refits when the frame it is drawn in gets narrower", () => {
    mount()
    expect(fitView).not.toHaveBeenCalled()
    resizeTo(520)
    expect(fitView).toHaveBeenCalled()
  })

  it("does not refit for a resize callback that reports the same size", () => {
    // ResizeObserver fires for the observation itself and can fire again
    // with an unchanged box. Refitting there would stamp on a reader's
    // zoom for no reason.
    mount()
    resizeTo(900)
    expect(fitView).not.toHaveBeenCalled()
  })

  it("refits when a different chain is loaded into the same canvas", () => {
    // Picking workflow B while workflow A is open does not remount the
    // canvas — same element, new nodes — and `fitView` is init-only, so B
    // inherited A's transform.
    const { rerender } = mount(["run:a", "run:b"])
    expect(fitView).not.toHaveBeenCalled()
    act(() => {
      rerender(<ChainCanvas nodes={nodesFor("run:c", "run:d", "run:e") as never} edges={[]} />)
    })
    expect(fitView).toHaveBeenCalled()
  })

  it("leaves a reader who has panned or zoomed alone", () => {
    // React Flow passes the originating event on a move it did not make
    // itself. A refit under someone mid-inspection is worse than the
    // clipping it fixes.
    mount()
    act(() => {
      captured.flow?.onMoveEnd?.(new MouseEvent("mouseup"), { x: 0, y: 0, zoom: 1 })
    })
    resizeTo(520)
    expect(fitView).not.toHaveBeenCalled()
  })

  it("takes control back when the reader picks another chain", () => {
    const { rerender } = mount(["run:a"])
    act(() => {
      captured.flow?.onMoveEnd?.(new MouseEvent("mouseup"), { x: 0, y: 0, zoom: 1 })
    })
    act(() => {
      rerender(<ChainCanvas nodes={nodesFor("run:z") as never} edges={[]} />)
    })
    fitView.mockClear()
    resizeTo(520)
    expect(fitView).toHaveBeenCalled()
  })

  it("ignores a programmatic move — its own fit is not a reader panning", () => {
    mount()
    act(() => {
      captured.flow?.onMoveEnd?.(null, { x: 0, y: 0, zoom: 1 })
    })
    resizeTo(520)
    expect(fitView).toHaveBeenCalled()
  })
})

describe("ChainCanvas chrome", () => {
  it("themes the zoom controls instead of shipping React Flow's white default", () => {
    // `@xyflow/react/dist/style.css` paints `.react-flow__controls-button`
    // #fefefe. On this dark surface that renders as a blank white block —
    // measured at 26×78px in the lower left of the live card, with the
    // icons white-on-white. Every other canvas in the repo overrides it;
    // this one had `!bottom-2 !left-2` and nothing else.
    mount()
    const cls = String(captured.controls?.className ?? "")
    expect(cls, "controls must restyle their buttons, not just move them").toMatch(/\[&_button\]/)
    expect(cls).toMatch(/!bg-/)
  })
})
