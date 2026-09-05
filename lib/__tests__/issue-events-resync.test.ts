import { describe, expect, it } from "vitest"

import { detectSeqGap } from "@/lib/issue-events-resync"

// F43 (work package B11, #2368): ws.Hub.dispatch is non-blocking and drops
// a frame silently when a client's buffer is full. detectSeqGap is the
// pure decision that turns "the seq I just saw is not lastSeq+1" into
// "there is a gap, and here is the after_seq a resync should use".

describe("detectSeqGap", () => {
  it("the first observation for a mission is never a gap", () => {
    expect(detectSeqGap(null, 1)).toEqual({ hasGap: false, afterSeq: 1 })
    expect(detectSeqGap(null, 42)).toEqual({ hasGap: false, afterSeq: 42 })
  })

  it("a consecutive seq is not a gap", () => {
    expect(detectSeqGap(4, 5)).toEqual({ hasGap: false, afterSeq: 5 })
  })

  it("a skipped seq IS a gap, and afterSeq is the OLD lastSeq", () => {
    expect(detectSeqGap(4, 7)).toEqual({ hasGap: true, afterSeq: 4 })
  })

  it("a single skipped seq (off by one gap) is still a gap", () => {
    expect(detectSeqGap(4, 6)).toEqual({ hasGap: true, afterSeq: 4 })
  })

  it("a duplicate seq is not a gap, and does not move afterSeq backwards", () => {
    expect(detectSeqGap(5, 5)).toEqual({ hasGap: false, afterSeq: 5 })
  })

  it("an out-of-order OLDER seq is not a gap, and afterSeq stays at the newer lastSeq", () => {
    expect(detectSeqGap(10, 3)).toEqual({ hasGap: false, afterSeq: 10 })
  })

  it("a large jump right after startup is still correctly flagged", () => {
    // e.g. the client's very first two frames for a mission arrive as seq
    // 1 then seq 50 — the second one IS a gap even though the first was
    // exempted as "first observation".
    const first = detectSeqGap(null, 1)
    expect(first.hasGap).toBe(false)
    const second = detectSeqGap(first.afterSeq, 50)
    expect(second).toEqual({ hasGap: true, afterSeq: 1 })
  })
})
