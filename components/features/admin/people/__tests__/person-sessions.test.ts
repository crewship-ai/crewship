import { describe, it, expect } from "vitest"
import { ago } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { describeAgent } from "../person-sessions"

const now = Date.parse("2026-09-29T12:00:00Z")

describe("reading times and devices", () => {
  it("says how long ago in words", () => {
    expect(ago(new Date(now - 30e3).toISOString(), now)).toBe("just now")
    expect(ago(new Date(now - 12 * 60e3).toISOString(), now)).toBe("12 min ago")
    expect(ago(new Date(now - 864e5 - 1).toISOString(), now)).toBe("yesterday")
    expect(ago(null, now)).toBe("never")
    // SQLite's datetime('now') text, read as UTC.
    expect(ago(new Date(now - 3 * 3600e3).toISOString().replace("T", " ").slice(0, 19), now)).toBe("3 h ago")
  })

  it("names the browser and system", () => {
    expect(describeAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1")).toBe("Safari · iOS")
    expect(describeAgent(null)).toBe("Unknown device")
  })
})
