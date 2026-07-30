import { describe, it, expect } from "vitest"

import { canvasKey } from "@/components/features/crews/crews-layout"

// =============================================================================
// Why a one-line function gets its own test.
//
// Saving any field on the agent's Configuration tab threw the user back to
// Overview. The cause was three steps away from the symptom:
//
//   1. a successful PATCH calls onAgentChanged → onRefresh → fetchData()
//   2. fetchData, called without `silent`, clears the lists first:
//        setCrews([]); setAgents([]); setLoading(true)
//   3. selectedAgent is agents.find(...) — momentarily null — so the
//      AnimatePresence key flipped "alex" → "empty" → "alex", React threw the
//      canvas away and built a new one, and the new one starts on Overview.
//
// The tab state was never "reset". The whole component was destroyed. So the
// key must follow what the USER selected, which does not move while data is in
// flight, never the entity that happens to be resolved right now.
//
// (The refetch after a save is also silent now, so step 2 no longer blanks the
// lists at all. This is the second lock on the same door: any future code path
// that empties the list mid-session still cannot remount the canvas.)
// =============================================================================

describe("canvasKey", () => {
  it("does not change while the agent list is refetching", () => {
    const before = canvasKey("alex", null)
    const during = canvasKey("alex", null) // agents === [] — selection unchanged
    expect(during).toBe(before)
  })

  it("changes when the user actually picks something else", () => {
    expect(canvasKey("alex", null)).not.toBe(canvasKey("morgan", null))
    expect(canvasKey(null, "ops")).not.toBe(canvasKey("alex", null))
  })

  it("prefers the agent when both are selected", () => {
    expect(canvasKey("alex", "ops")).toBe(canvasKey("alex", null))
  })

  it("falls back to a stable key with no selection", () => {
    expect(canvasKey(null, null)).toBe("empty")
    expect(canvasKey(null, null)).toBe(canvasKey(null, null))
  })
})
