import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { useUnsavedNavigationGuard } from "../use-unsaved-navigation-guard"
import { navigationAllowed } from "../use-navigation-guard"
import { ConfirmHost } from "@/components/ui/confirm-host"

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

  describe("inside the application shell", () => {
    it("asks in the page, not with the browser's confirm, and retries the navigation on Leave", async () => {
      render(<ConfirmHost />)
      renderHook(() => useUnsavedNavigationGuard(true, "Discard draft?"))
      const retry = vi.fn()
      let allowed = true
      act(() => { allowed = navigationAllowed(retry) })
      expect(allowed).toBe(false)
      const dialog = await screen.findByRole("alertdialog")
      expect(dialog).toHaveTextContent("Discard draft?")
      expect(window.confirm).not.toHaveBeenCalled()
      fireEvent.click(screen.getByRole("button", { name: "Leave" }))
      await waitFor(() => expect(retry).toHaveBeenCalledOnce())
    })

    it("stays on the page when the person keeps editing", async () => {
      render(<ConfirmHost />)
      renderHook(() => useUnsavedNavigationGuard(true, "Discard draft?"))
      const retry = vi.fn()
      act(() => { navigationAllowed(retry) })
      fireEvent.click(await screen.findByRole("button", { name: "Keep editing" }))
      await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
      expect(retry).not.toHaveBeenCalled()
    })

    it("refuses Back, then repeats the same traversal once Leave is chosen", async () => {
      render(<ConfirmHost />)
      renderHook(() => useUnsavedNavigationGuard(true, "Discard draft?"))
      const go = vi.spyOn(window.history, "go").mockImplementation(() => {})
      // The dispatcher has put the editor's entry (index 3) back by the time
      // the person answers; the refused traversal was to index 1.
      window.history.replaceState({ __crewshipHistoryIndex: 1 }, "", "/other")
      const guard = [...window.__crewshipHistoryGuards!][0]
      let allowed = true
      act(() => { allowed = guard(new PopStateEvent("popstate", { state: { __crewshipHistoryIndex: 1 } })) })
      expect(allowed).toBe(false)
      window.history.replaceState({ __crewshipHistoryIndex: 3 }, "", "/editor?draft=1#start")
      fireEvent.click(await screen.findByRole("button", { name: "Leave" }))
      await waitFor(() => expect(go).toHaveBeenCalledWith(-2))
      // The repeated traversal is not asked about a second time.
      window.history.replaceState({ __crewshipHistoryIndex: 1 }, "", "/other")
      expect(guard(new PopStateEvent("popstate", { state: { __crewshipHistoryIndex: 1 } }))).toBe(true)
    })
  })
})

describe("the in-page question", () => {
  it("uses a leading question of the message as its title instead of repeating it", async () => {
    window.__crewshipHistoryGuards = new Set()
    render(<ConfirmHost />)
    renderHook(() => useUnsavedNavigationGuard(true, "Leave without saving? Your unsaved changes will be lost."))
    act(() => { navigationAllowed(vi.fn()) })
    const dialog = await screen.findByRole("alertdialog", { name: "Leave without saving?" })
    expect(dialog.textContent?.match(/Leave without saving\?/g)).toHaveLength(1)
    expect(dialog).toHaveTextContent("Your unsaved changes will be lost.")
  })
})
