/**
 * The chain index hook, at the level only the hook can be wrong at.
 *
 * The shaping of what a chain MEANS is asserted against the pure functions in
 * lib/__tests__/activity-lenses.test.ts. What is left for this file is the part
 * those cannot see: which fields of the response reach the caller at all.
 *
 * `has_more` is the reason this file exists. The server has sent it since the
 * route was written and nothing read it, so every count the rail drew described
 * the newest 25 rows while reading as a fact about the workspace. A page that
 * cannot tell "4 agents worked today" from "4 agents worked in the last 25
 * chains" is not narrowing a claim, it is making a different one.
 */

import { renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { useChains } from "@/hooks/use-chains"

function reply(body: unknown, ok = true) {
  return Promise.resolve({ ok, status: ok ? 200 : 500, json: () => Promise.resolve(body) })
}

beforeEach(() => {
  apiFetch.mockReset()
})

describe("useChains", () => {
  it("reports that the window is partial when the server says so", async () => {
    apiFetch.mockReturnValue(reply({ chains: [], has_more: true, has_unrecorded_runs: false }))
    const { result } = renderHook(() => useChains("ws_1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.hasMore).toBe(true)
  })

  it("reports a complete window as complete", async () => {
    apiFetch.mockReturnValue(reply({ chains: [], has_more: false, has_unrecorded_runs: false }))
    const { result } = renderHook(() => useChains("ws_1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.hasMore).toBe(false)
  })

  it("does not claim completeness from a server that omits the field", async () => {
    // An older server sends no has_more. Reading its absence as `false` would
    // print "these are all of them" on the evidence of a field nobody sent —
    // the same mistake as reading an unloaded bucket's count as 0. Absent means
    // "cannot say", and the caller renders the notice rather than suppressing
    // it, because an unnecessary caveat costs a line and a missing one costs
    // the reader's trust in every number above it.
    apiFetch.mockReturnValue(reply({ chains: [] }))
    const { result } = renderHook(() => useChains("ws_1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.hasMore).toBe(true)
  })

  it("carries the rows and the unrecorded-runs flag through", async () => {
    apiFetch.mockReturnValue(
      reply({
        chains: [{ origin: "run_a", runs: 1 }],
        has_more: false,
        has_unrecorded_runs: true,
      }),
    )
    const { result } = renderHook(() => useChains("ws_1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.chains).toHaveLength(1)
    expect(result.current.hasUnrecordedRuns).toBe(true)
  })

  it("asks for the window size it was given", async () => {
    apiFetch.mockReturnValue(reply({ chains: [], has_more: false }))
    const { result } = renderHook(() => useChains("ws_1", 50))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(String(apiFetch.mock.calls[0][0])).toContain("limit=50")
  })

  it("leaves a failed fetch reporting an unknown window rather than a complete one", async () => {
    apiFetch.mockReturnValue(reply({}, false))
    const { result } = renderHook(() => useChains("ws_1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).not.toBeNull()
    expect(result.current.hasMore).toBe(true)
  })
})
