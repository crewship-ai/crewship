import { describe, expect, it } from "vitest"
import { RoutineStartIntent } from "../routine-start-intent"

describe("manual routine start identity", () => {
  it("blocks a double submit and retains the key after an uncertain response", () => {
    const starts = new RoutineStartIntent()
    const first = starts.begin("/ws/a/run", { inputs: { count: 0 } })!
    expect(first.key).toBeTruthy()
    expect(starts.begin("/ws/a/run", { inputs: { count: 0 } })).toBeNull()
    first.finish(false)
    const retry = starts.begin("/ws/a/run", { inputs: { count: 0 } })!
    expect(retry.key).toBe(first.key)
    retry.finish(true)
    expect(starts.begin("/ws/a/run", { inputs: { count: 0 } })!.key).not.toBe(first.key)
  })

  it("keeps workspace, recipe, inputs and version identities separate", () => {
    const starts = new RoutineStartIntent()
    const attempts = [
      starts.begin("/ws/a/run", { inputs: { enabled: false }, pinned_version: 1 }),
      starts.begin("/other/a/run", { inputs: { enabled: false }, pinned_version: 1 }),
      starts.begin("/ws/b/run", { inputs: { enabled: false }, pinned_version: 1 }),
      starts.begin("/ws/a/run", { inputs: { enabled: true }, pinned_version: 1 }),
      starts.begin("/ws/a/run", { inputs: { enabled: false }, pinned_version: 2 }),
    ]
    expect(new Set(attempts.map((a) => a!.key)).size).toBe(5)
    attempts.forEach((a) => a!.finish(false))
    expect(starts.begin("/ws/a/run", { inputs: { enabled: false }, pinned_version: 1 })!.key).toBe(
      attempts[0]!.key,
    )
  })
})
