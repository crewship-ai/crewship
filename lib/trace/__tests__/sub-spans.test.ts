import { describe, it, expect } from "vitest"
import { mapSubSpans, pickModel, layoutSubSpans } from "@/lib/trace/sub-spans"

describe("mapSubSpans", () => {
  it("maps wire rows to normalized SubSpans (snake → camel)", () => {
    const spans = mapSubSpans([
      {
        kind: "bash",
        name: "ansible-playbook",
        detail: "ansible-playbook -i 'localhost,' sysfacts.yml",
        started_at: "2026-06-30T14:57:00.000Z",
        duration_ms: 2100,
        status: "ok",
        attributes: { tool: "ansible", host: "localhost" },
      },
    ])
    expect(spans).toHaveLength(1)
    expect(spans[0]).toEqual({
      kind: "bash",
      name: "ansible-playbook",
      detail: "ansible-playbook -i 'localhost,' sysfacts.yml",
      startedAt: "2026-06-30T14:57:00.000Z",
      durationMs: 2100,
      status: "ok",
      attributes: { tool: "ansible", host: "localhost" },
    })
  })

  it("orders by seq, not array order, stable on ties", () => {
    const spans = mapSubSpans([
      { kind: "bash", name: "third", seq: 2, status: "ok" },
      { kind: "write", name: "tie-a", seq: 1, status: "ok" },
      { kind: "think", name: "tie-b", seq: 1, status: "ok" },
      { kind: "read", name: "first", seq: 0, status: "ok" },
    ])
    // seq drives the order; tie-a and tie-b share seq=1, so the tie path
    // actually runs and must preserve their original array order (a before
    // b) — that's what proves the sort is stable, which unique seqs never
    // exercised.
    expect(spans.map((s) => s.name)).toEqual(["first", "tie-a", "tie-b", "third"])
  })

  it("falls back to array order when seq is absent", () => {
    const spans = mapSubSpans([
      { kind: "bash", name: "a", status: "ok" },
      { kind: "bash", name: "b", status: "ok" },
    ])
    expect(spans.map((s) => s.name)).toEqual(["a", "b"])
  })

  it("coerces unknown kind/status to safe defaults", () => {
    const [s] = mapSubSpans([{ kind: "wormhole", status: "exploded" }])
    expect(s.kind).toBe("tool")
    expect(s.status).toBe("ok")
    expect(s.name).toBe("tool") // name falls back to kind
  })

  it("returns [] for the empty / no-drill-down cases", () => {
    expect(mapSubSpans(undefined)).toEqual([])
    expect(mapSubSpans(null)).toEqual([])
    expect(mapSubSpans({})).toEqual([])
    expect(mapSubSpans([])).toEqual([])
  })

  it("never throws on garbage and drops non-object rows", () => {
    expect(() => mapSubSpans("nope")).not.toThrow()
    expect(mapSubSpans("nope")).toEqual([])
    const spans = mapSubSpans([null, 42, "x", { kind: "read", name: "ok", status: "ok" }])
    expect(spans).toHaveLength(1)
    expect(spans[0].name).toBe("ok")
  })

  it("ignores malformed attributes / timing without throwing", () => {
    const [s] = mapSubSpans([
      { kind: "http", name: "call", status: "running", attributes: "bad", duration_ms: "nan" },
    ])
    expect(s.attributes).toEqual({})
    expect(s.durationMs).toBeUndefined()
    expect(s.startedAt).toBeUndefined()
    expect(s.status).toBe("running")
  })
})

describe("pickModel", () => {
  it("returns the first attributes.model present", () => {
    const spans = mapSubSpans([
      { kind: "bash", name: "a", status: "ok" },
      { kind: "tool", name: "b", status: "ok", attributes: { model: "opus-4-8" } },
    ])
    expect(pickModel(spans)).toBe("opus-4-8")
  })

  it("returns null when no span carries a model", () => {
    expect(pickModel(mapSubSpans([{ kind: "bash", name: "a", status: "ok" }]))).toBeNull()
    expect(pickModel([])).toBeNull()
  })
})

describe("layoutSubSpans", () => {
  it("returns [] for no spans", () => {
    expect(layoutSubSpans([])).toEqual([])
  })

  it("positions bars by started_at/duration within the window", () => {
    const spans = mapSubSpans([
      { kind: "write", name: "w", started_at: "2026-01-01T00:00:00.000Z", duration_ms: 1000, status: "ok" },
      { kind: "bash", name: "b", started_at: "2026-01-01T00:00:09.000Z", duration_ms: 1000, status: "ok" },
    ])
    const bars = layoutSubSpans(spans)
    // window = 0s → 10s. first bar at 0%, second at 90%.
    expect(bars[0].leftPct).toBeCloseTo(0, 1)
    expect(bars[1].leftPct).toBeCloseTo(90, 1)
    expect(bars[0].widthPct).toBeGreaterThan(0)
  })

  it("a single timed span spans the full window", () => {
    const spans = mapSubSpans([
      { kind: "bash", name: "b", started_at: "2026-01-01T00:00:00.000Z", duration_ms: 5000, status: "ok" },
    ])
    const [bar] = layoutSubSpans(spans)
    expect(bar.leftPct).toBeCloseTo(0, 1)
    expect(bar.widthPct).toBeCloseTo(100, 1)
  })

  it("never lets a bar overflow 100%", () => {
    const spans = mapSubSpans([
      { kind: "bash", name: "b", started_at: "2026-01-01T00:00:00.000Z", duration_ms: 1000, status: "ok" },
    ])
    const [bar] = layoutSubSpans(spans)
    expect(bar.leftPct + bar.widthPct).toBeLessThanOrEqual(100.001)
  })

  it("keeps a final 0ms span visible at the right edge", () => {
    const spans = mapSubSpans([
      { kind: "bash", name: "long", started_at: "2026-01-01T00:00:00.000Z", duration_ms: 10000, status: "ok" },
      // Instantaneous span at the very end of the window — leftPct would
      // compute to 100 and collapse the width to 0 without the clamp.
      { kind: "think", name: "tail", started_at: "2026-01-01T00:00:10.000Z", duration_ms: 0, status: "ok" },
    ])
    const bars = layoutSubSpans(spans)
    const tail = bars[1]
    expect(tail.widthPct).toBeGreaterThan(0)
    expect(tail.leftPct).toBeLessThan(100)
    expect(tail.leftPct + tail.widthPct).toBeLessThanOrEqual(100.001)
  })

  it("falls back to even slices when no span has timing", () => {
    const spans = mapSubSpans([
      { kind: "bash", name: "a", status: "ok" },
      { kind: "bash", name: "b", status: "ok" },
    ])
    const bars = layoutSubSpans(spans)
    expect(bars[0].leftPct).toBeCloseTo(0, 1)
    expect(bars[1].leftPct).toBeCloseTo(50, 1)
  })
})
