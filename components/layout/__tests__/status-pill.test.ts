import { describe, it, expect } from "vitest"

import { describeSystemStatus } from "../status-pill"

// The toolbar used to carry two pills: connection ("Online") and fleet
// ("Crews idle"). They competed for the same glance and the fleet one lost —
// its "N active" said what the Activity panel already says with a name, an
// elapsed time and a cost, while its idle state said nothing at all.
//
// One pill now: connection on the left, and on the right the thing only it
// knows — how many agents there are, and whether any are broken or queued.

const OK = { total: 7, running: 0, error: 0, idle: 7, queued: 0 }

describe("describeSystemStatus — connection", () => {
  it("is online only when the engine AND the realtime channel are both up", () => {
    expect(describeSystemStatus("connected", "connected", OK).connection.label).toBe("Online")
    expect(describeSystemStatus("connected", "connecting", OK).connection.label).toBe("Connecting")
    expect(describeSystemStatus("checking", "connected", OK).connection.label).toBe("Connecting")
  })

  it("tells a mid-deploy restart apart from a dead engine", () => {
    // "degraded" is one failed poll or a 429 — not a gone engine, and it must
    // not read like one.
    expect(describeSystemStatus("degraded", "connected", OK).connection.label).toBe("Reconnecting")
    expect(describeSystemStatus("degraded", "connected", OK).connection.tone).toBe("warn")
    expect(describeSystemStatus("offline", "disconnected", OK).connection.label).toBe("Offline")
    expect(describeSystemStatus("offline", "disconnected", OK).connection.tone).toBe("destructive")
  })
})

describe("describeSystemStatus — fleet", () => {
  it("says nothing at all when everything is healthy", () => {
    // The pill first said "Crews idle" — a claim about right now, on a column
    // that flips for the six seconds an agent takes to answer. Then it said
    // "7 agents", true but not news: on a healthy workspace it is a number
    // that never changes and never asks for anything. A status strip earns
    // its place by being quiet until it has something to say.
    //
    // The count is not lost — the tooltip still carries the full breakdown.
    expect(describeSystemStatus("connected", "connected", OK).fleet).toBeNull()
  })

  it("says so when the workspace has no agents at all", () => {
    const s = describeSystemStatus("connected", "connected", { ...OK, total: 0, idle: 0 })
    expect(s.fleet?.label).toBe("No agents")
  })

  it("surfaces errors, which live nowhere else in the product", () => {
    const s = describeSystemStatus("connected", "connected", { ...OK, error: 2, idle: 5 })
    expect(s.fleet).toEqual({ label: "2 errors", tone: "destructive" })
  })

  it("surfaces a queue, which also lives nowhere else", () => {
    const s = describeSystemStatus("connected", "connected", { ...OK, queued: 3 })
    expect(s.fleet).toEqual({ label: "3 queued", tone: "warn" })
  })

  it("puts a broken agent above a deep queue", () => {
    const s = describeSystemStatus("connected", "connected", { ...OK, error: 1, queued: 9 })
    expect(s.fleet?.label).toBe("1 error")
  })

  it("does not report what is running — Activity says that, with a name", () => {
    const s = describeSystemStatus("connected", "connected", { ...OK, running: 3, idle: 4 })
    expect(s.fleet).toBeNull()
  })

  it("caps a runaway count so the pill cannot widen the bar", () => {
    const s = describeSystemStatus("connected", "connected", { ...OK, error: 250 })
    expect(s.fleet?.label).toBe("99+ errors")
  })

  it("pluralises honestly", () => {
    expect(describeSystemStatus("connected", "connected", { ...OK, error: 1 }).fleet?.label).toBe("1 error")
    expect(describeSystemStatus("connected", "connected", { ...OK, error: 2 }).fleet?.label).toBe("2 errors")
    expect(describeSystemStatus("connected", "connected", { ...OK, queued: 1 }).fleet?.label).toBe("1 queued")
  })
})

describe("describeSystemStatus — honesty while disconnected", () => {
  it("drops the fleet entirely when the connection is not up", () => {
    // The counts are last-known, not current. Printing "7 agents" beside
    // "Offline" states as fact something nobody can currently know.
    for (const [engine, ws] of [["offline", "disconnected"], ["degraded", "connected"], ["checking", "connecting"]] as const) {
      expect(describeSystemStatus(engine, ws, { ...OK, error: 4 }).fleet).toBeNull()
    }
  })

  it("drops it while the counts have not arrived yet", () => {
    expect(describeSystemStatus("connected", "connected", null).fleet).toBeNull()
  })
})

describe("describeSystemStatus — screen readers", () => {
  it("reads as one sentence covering both halves", () => {
    const s = describeSystemStatus("connected", "connected", { ...OK, error: 2 })
    expect(s.ariaLabel).toBe("System online, 2 errors")
  })

  it("says just the connection when the fleet has nothing to report", () => {
    expect(describeSystemStatus("connected", "connected", OK).ariaLabel).toBe("System online")
  })

  it("says only what it knows when disconnected", () => {
    expect(describeSystemStatus("offline", "disconnected", OK).ariaLabel).toBe("System offline")
  })
})

describe("describeSystemStatus — what the tooltip may recite", () => {
  // The pill's fleet segment and the tooltip's breakdown answer different
  // questions, so they cannot share a condition. Gating the tooltip on the
  // segment would have hidden the counts exactly when the pill stopped
  // showing them — which is when the tooltip became their only home.
  it("knows the fleet on a healthy workspace, even with nothing to display", () => {
    const s = describeSystemStatus("connected", "connected", OK)
    expect(s.fleet).toBeNull()
    expect(s.fleetKnown).toBe(true)
  })

  it("does not know it while the link is down", () => {
    expect(describeSystemStatus("offline", "disconnected", OK).fleetKnown).toBe(false)
    expect(describeSystemStatus("degraded", "connected", OK).fleetKnown).toBe(false)
  })

  it("does not know it before the counts arrive", () => {
    expect(describeSystemStatus("connected", "connected", null).fleetKnown).toBe(false)
  })
})
