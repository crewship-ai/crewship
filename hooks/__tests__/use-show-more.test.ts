import { act, renderHook } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { useShowMore } from "@/hooks/use-show-more"

const rows = (n: number) => Array.from({ length: n }, (_, i) => i)

describe("useShowMore", () => {
  it("shows the first step and says how many are left", () => {
    const { result } = renderHook(() => useShowMore(rows(60), { step: 25 }))
    expect(result.current.shown).toHaveLength(25)
    expect(result.current.total).toBe(60)
    expect(result.current.hasMore).toBe(true)
    expect(result.current.nextCount).toBe(25)
  })

  it("grows by one step and ends on the last row", () => {
    const { result } = renderHook(() => useShowMore(rows(60), { step: 25 }))
    act(() => result.current.showMore())
    expect(result.current.shown).toHaveLength(50)
    expect(result.current.nextCount).toBe(10)
    act(() => result.current.showMore())
    expect(result.current.shown).toHaveLength(60)
    expect(result.current.hasMore).toBe(false)
    expect(result.current.nextCount).toBe(0)
  })

  it("starts over when the filter changes", () => {
    const { result, rerender } = renderHook(({ key }) => useShowMore(rows(60), { step: 25, resetKey: key }), {
      initialProps: { key: "a" },
    })
    act(() => result.current.showMore())
    expect(result.current.shown).toHaveLength(50)
    rerender({ key: "b" })
    expect(result.current.shown).toHaveLength(25)
  })

  it("keeps a short list whole", () => {
    const { result } = renderHook(() => useShowMore(rows(7), { step: 25 }))
    expect(result.current.shown).toHaveLength(7)
    expect(result.current.hasMore).toBe(false)
    expect(result.current.paged).toBe(false)
  })
})
