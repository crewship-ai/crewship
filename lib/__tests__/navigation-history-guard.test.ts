import { it, expect, vi } from "vitest"
import { HISTORY_NAVIGATION_GUARD_SCRIPT, registerHistoryNavigationGuard } from "../navigation-history-guard"

it("asks registered guards before a later router listener can unmount the editor", () => {
  const isolated = Object.assign(new EventTarget(), {
    history: { state: null, replaceState: vi.fn(), pushState: vi.fn(), go: vi.fn() },
  })
  // Execute the exact static head script without changing the shared DOM.
  new Function("window", HISTORY_NAVIGATION_GUARD_SCRIPT)(isolated)
  vi.stubGlobal("window", isolated)
  const router = vi.fn()
  window.addEventListener("popstate", router)
  const guard = vi.fn(() => false)
  const unregister = registerHistoryNavigationGuard(guard)
  try {
    window.dispatchEvent(new PopStateEvent("popstate"))
    expect(guard).toHaveBeenCalledOnce()
    expect(router).not.toHaveBeenCalled()
    guard.mockReturnValue(true)
    window.dispatchEvent(new PopStateEvent("popstate"))
    expect(router).toHaveBeenCalledOnce()
    unregister()
    window.dispatchEvent(new PopStateEvent("popstate"))
    expect(router).toHaveBeenCalledTimes(2)
    expect(guard).toHaveBeenCalledTimes(2)
  } finally {
    unregister()
    window.removeEventListener("popstate", router)
    vi.unstubAllGlobals()
  }
})


it.each([-1, -2, 1])("restores a declined traversal (%s) without deleting Forward entries", (delta) => {
  // A real traversal stack: push truncates Forward, go does not. Execute the
  // production bootstrap against it, including the compensating popstate.
  const target = new EventTarget()
  const entries: { state: Record<string, unknown>; url: string }[] = [{ state: {}, url: "/before" }]
  let position = 0
  const fakeWindow = Object.assign(target, {
    history: {
      get state() { return entries[position].state },
      replaceState(state: Record<string, unknown>, _title: string, url?: string) {
        entries[position] = { state, url: url ?? entries[position].url }
      },
      pushState(state: Record<string, unknown>, _title: string, url: string) {
        entries.splice(position + 1)
        entries.push({ state, url })
        position++
      },
      go(amount: number) {
        position += amount
        target.dispatchEvent(new PopStateEvent("popstate", { state: entries[position].state }))
      },
    },
    __crewshipHistoryGuards: undefined as Set<(event: PopStateEvent) => boolean> | undefined,
  })
  new Function("window", HISTORY_NAVIGATION_GUARD_SCRIPT)(fakeWindow)
  fakeWindow.history.pushState({ retained: "middle" }, "", "/middle")
  fakeWindow.history.pushState({ retained: "editor" }, "", "/editor")
  fakeWindow.history.pushState({ retained: "future" }, "", "/future")
  fakeWindow.history.go(-1)
  const router = vi.fn()
  target.addEventListener("popstate", router)
  const guard = vi.fn(() => false)
  fakeWindow.__crewshipHistoryGuards!.add(guard)
  const originalEntries = structuredClone(entries)
  fakeWindow.history.go(delta)
  expect(guard).toHaveBeenCalledOnce()
  expect(router).not.toHaveBeenCalled()
  expect(position).toBe(2)
  expect(entries).toEqual(originalEntries)
  guard.mockReturnValue(true)
  fakeWindow.history.go(1)
  expect(entries[position].url).toBe("/future")
  expect(entries[position].state.retained).toBe("future")
  expect(router).toHaveBeenCalledOnce()
})
