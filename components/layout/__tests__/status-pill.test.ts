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
  it("states the census when everything is healthy", () => {
    // Not "idle": a claim about right now, on a column that flips for the six
    // seconds an agent takes to answer, is wrong more often than it is right.
    // How many agents exist is true whenever anyone looks.
    expect(describeSystemStatus("connected", "connected", OK).fleet).toEqual({
      label: "7 agents",
      tone: "muted",
    })
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
    expect(s.fleet?.label).toBe("7 agents")
  })

  it("caps a runaway count so the pill cannot widen the bar", () => {
    const s = describeSystemStatus("connected", "connected", { ...OK, error: 250 })
    expect(s.fleet?.label).toBe("99+ errors")
  })

  it("pluralises honestly", () => {
    expect(describeSystemStatus("connected", "connected", { ...OK, error: 1 }).fleet?.label).toBe("1 error")
    expect(describeSystemStatus("connected", "connected", { ...OK, total: 1, idle: 1 }).fleet?.label).toBe("1 agent")
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

  it("says only what it knows when disconnected", () => {
    expect(describeSystemStatus("offline", "disconnected", OK).ariaLabel).toBe("System offline")
  })
})
