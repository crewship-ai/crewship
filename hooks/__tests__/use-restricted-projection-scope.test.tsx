import { act, cleanup, renderHook } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { useRestrictedProjection } from "../use-restricted-projection"

afterEach(cleanup)

it("invalidates a captured save when the scope changes before any new read", () => {
  const { result, rerender } = renderHook(({ scope }) => useRestrictedProjection<string>(scope), { initialProps: { scope: "A" } })
  act(() => { result.current.read(new AbortController().signal).accept("verified A") })
  const saveA = result.current.captureSave()
  rerender({ scope: "B" })
  let accepted = false
  act(() => { accepted = saveA("late saved A") })
  expect(accepted).toBe(false)
  expect(result.current.value).toBeNull()
})

it("does not let an old read reverify A after A→B→A without a fresh read", () => {
  const { result, rerender } = renderHook(({ scope }) => useRestrictedProjection<string>(scope), { initialProps: { scope: "A" } })
  const oldA = result.current.read(new AbortController().signal)
  rerender({ scope: "B" })
  act(() => { oldA.accept("late old A") })
  rerender({ scope: "A" })
  const saveBeforeFreshRead = result.current.captureSave()
  let accepted = false
  act(() => { accepted = saveBeforeFreshRead("unverified A") })
  expect(accepted).toBe(false)
  expect(result.current.value).toBeNull()
  act(() => { result.current.read(new AbortController().signal).accept("fresh A") })
  const freshSave = result.current.captureSave()
  act(() => { accepted = freshSave("fresh saved A") })
  expect(accepted).toBe(true)
  expect(result.current.value).toBe("fresh saved A")
})
