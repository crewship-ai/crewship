import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { FINDING_ACTIONS, overLimit } from "../tabs/overview-tab"

// A finding without a verb is a worry; with one it is a task. The keys here
// must be real posture keys, so a renamed key on the Go side is caught here
// rather than by a client staring at a button that never appears.
describe("admin overview actions", () => {
  it("only names posture keys the server actually emits", () => {
    const go = readFileSync(join(process.cwd(), "internal", "api", "admin_security_posture.go"), "utf8")
    const emitted = new Set([...go.matchAll(/Key:\s*"([a-z_]+)"/g)].map((m) => m[1]))
    for (const key of Object.keys(FINDING_ACTIONS)) {
      expect(emitted.has(key), `FINDING_ACTIONS names ${key}, which admin_security_posture.go does not emit`).toBe(true)
    }
    // The one finding every fresh instance shows must have an action.
    expect(FINDING_ACTIONS.no_backup_recorded).toBeTruthy()
  })

  it("flags a licensed ceiling only when it is exceeded", () => {
    expect(overLimit(101, 15)).toBe(true)
    expect(overLimit(15, 15)).toBe(false)
    expect(overLimit(3, undefined)).toBe(false)
    expect(overLimit(3, 0)).toBe(false)
  })
})
