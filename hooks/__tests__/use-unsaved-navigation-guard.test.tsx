import { act, cleanup, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { useUnsavedNavigationGuard } from "../use-unsaved-navigation-guard"
import { navigationAllowed } from "../use-navigation-guard"

const router = vi.hoisted(() => ({ push: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => router }))

beforeEach(() => {
  window.history.replaceState({}, "", "/editor?draft=1#start")
  window.__crewshipHistoryGuards = new Set()
  vi.stubGlobal("confirm", vi.fn(() => false))
})
afterEach(() => {
  cleanup()
  delete window.__crewshipHistoryGuards
  window.history.replaceState({}, "", "/")
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})
function traversal(url: string) {
  window.history.replaceState({}, "", url)
  const guard = [...window.__crewshipHistoryGuards!][0]
  expect(guard).toBeDefined()
  return guard(new PopStateEvent("popstate"))
}

describe("unsaved editor navigation", () => {
  it("leaves clean editors and beforeunload unguarded", () => {
    renderHook(() => useUnsavedNavigationGuard(false, "Discard draft?"))
    expect(navigationAllowed(vi.fn())).toBe(true)
    expect(window.__crewshipHistoryGuards!.size).toBe(0)
    const event = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(false)
    expect(window.confirm).not.toHaveBeenCalled()
  })

  it("protects reload and application navigation only while dirty", () => {
    const { rerender } = renderHook(({ dirty }) => useUnsavedNavigationGuard(dirty, "Discard draft?"), { initialProps: { dirty: true } })
    const event = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    expect(navigationAllowed(vi.fn())).toBe(false)
    expect(window.confirm).toHaveBeenCalledWith("Discard draft?")
    rerender({ dirty: false })
    expect(window.__crewshipHistoryGuards!.size).toBe(0)
    expect(navigationAllowed(vi.fn())).toBe(true)
    const cleanEvent = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(cleanEvent)
    expect(cleanEvent.defaultPrevented).toBe(false)
  })

  it("allows fragment and same-address overlay entries without discarding the editor", () => {
    renderHook(() => useUnsavedNavigationGuard(true, "Discard draft?"))
    expect(traversal("/editor?draft=1#next")).toBe(true)
    expect(traversal("/editor?draft=1#next")).toBe(true)
    expect(window.confirm).not.toHaveBeenCalled()
    expect(traversal("/other")).toBe(false)
    expect(window.confirm).toHaveBeenCalledOnce()
  })

  it.each(["/other?draft=1", "/editor?draft=2"])("honors the discard choice for traversal to %s", (url) => {
    renderHook(() => useUnsavedNavigationGuard(true, "Discard draft?"))
    expect(traversal(url)).toBe(false)
    vi.mocked(window.confirm).mockReturnValue(true)
    expect(traversal(url)).toBe(true)
  })

  it("uses the current message and unregisters both guards on unmount", () => {
    const { rerender, unmount } = renderHook(({ message }) => useUnsavedNavigationGuard(true, message), { initialProps: { message: "Discard old draft?" } })
    rerender({ message: "Discard current draft?" })
    expect(window.__crewshipHistoryGuards!.size).toBe(1)
    expect(traversal("/other")).toBe(false)
    expect(window.confirm).toHaveBeenLastCalledWith("Discard current draft?")
    unmount()
    expect(window.__crewshipHistoryGuards!.size).toBe(0)
    expect(navigationAllowed(vi.fn())).toBe(true)
    const event = new Event("beforeunload", { cancelable: true })
    window.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(false)
  })

  it("blocks later popstate listeners in a standalone editor without the head dispatcher", () => {
    delete window.__crewshipHistoryGuards
    const { unmount } = renderHook(() => useUnsavedNavigationGuard(true, "Discard draft?"))
    const later = vi.fn()
    window.addEventListener("popstate", later)
    try {
      window.history.replaceState({}, "", "/other")
      act(() => { window.dispatchEvent(new PopStateEvent("popstate")) })
      expect(later).not.toHaveBeenCalled()
      vi.mocked(window.confirm).mockReturnValue(true)
      act(() => { window.dispatchEvent(new PopStateEvent("popstate")) })
      expect(later).toHaveBeenCalledOnce()
      unmount()
      act(() => { window.dispatchEvent(new PopStateEvent("popstate")) })
      expect(later).toHaveBeenCalledTimes(2)
    } finally { window.removeEventListener("popstate", later) }
  })
})
