import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, renderHook } from "@testing-library/react"

// Phone pilot (PILOT-TELEFON-STAV rows 3–5): list → detail → Back keeps the
// list's search, filters and scroll; Back from a detail opened by a direct
// link goes to the area's list, never out of the app; and one workspace's
// list state never comes back in another.

const router = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => router }))

import { listDetailKey, usePhoneListDetail } from "../use-phone-list-detail"

type State = { search: string; crew: string | null }

function setUrl(path: string) {
  window.history.replaceState(null, "", path)
}

beforeEach(() => {
  router.push.mockReset()
  router.replace.mockReset()
  sessionStorage.clear()
  setUrl("/inbox")
})

describe("usePhoneListDetail", () => {
  it("keys the state by area and workspace", () => {
    expect(listDetailKey("inbox", "ws-a")).not.toBe(listDetailKey("inbox", "ws-b"))
    expect(listDetailKey("inbox", "ws-a")).not.toBe(listDetailKey("settings", "ws-a"))
  })

  it("open pushes the detail; back after it goes back in history to the list", () => {
    const { result } = renderHook(() => usePhoneListDetail<State>({ area: "inbox", workspaceId: "ws-a", listHref: "/inbox" }))
    act(() => result.current.open("/inbox?item=42"))
    expect(router.push).toHaveBeenCalledWith("/inbox?item=42")
    setUrl("/inbox?item=42") // the router has navigated
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {})
    act(() => result.current.back())
    expect(back).toHaveBeenCalledTimes(1)
    expect(router.replace).not.toHaveBeenCalled()
    back.mockRestore()
  })

  it("back from a detail opened by a direct link replaces it with the area's list", () => {
    setUrl("/inbox?item=42")
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {})
    const { result } = renderHook(() => usePhoneListDetail<State>({ area: "inbox", workspaceId: "ws-a", listHref: "/inbox?view=updates" }))
    act(() => result.current.back())
    expect(back).not.toHaveBeenCalled()
    expect(router.replace).toHaveBeenCalledWith("/inbox?view=updates")
    back.mockRestore()
  })

  it("survives a reload on the detail: the marker is in session storage", () => {
    const first = renderHook(() => usePhoneListDetail<State>({ area: "inbox", workspaceId: "ws-a", listHref: "/inbox" }))
    act(() => first.result.current.open("/inbox?item=7"))
    first.unmount()
    setUrl("/inbox?item=7")
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {})
    const again = renderHook(() => usePhoneListDetail<State>({ area: "inbox", workspaceId: "ws-a", listHref: "/inbox" }))
    act(() => again.result.current.back())
    expect(back).toHaveBeenCalledTimes(1)
    back.mockRestore()
  })

  it("stores and reads the list state per workspace", () => {
    const a = renderHook(() => usePhoneListDetail<State>({ area: "inbox", workspaceId: "ws-a", listHref: "/inbox" }))
    act(() => a.result.current.saveList({ search: "VAT", crew: "finance" }))
    expect(a.result.current.readList()).toEqual({ search: "VAT", crew: "finance" })
    const b = renderHook(() => usePhoneListDetail<State>({ area: "inbox", workspaceId: "ws-b", listHref: "/inbox" }))
    expect(b.result.current.readList()).toBeNull()
  })

  it("remembers and restores the list's scroll position", () => {
    const { result } = renderHook(() => usePhoneListDetail<State>({ area: "inbox", workspaceId: "ws-a", listHref: "/inbox" }))
    const el = document.createElement("div")
    el.scrollTop = 0
    Object.defineProperty(el, "scrollTop", { value: 480, writable: true })
    act(() => result.current.rememberScroll(el))
    const fresh = document.createElement("div")
    Object.defineProperty(fresh, "scrollHeight", { value: 2000 })
    Object.defineProperty(fresh, "clientHeight", { value: 600 })
    expect(result.current.restoreScroll(fresh)).toBe(true)
    expect(fresh.scrollTop).toBe(480)
  })

  it("does nothing with storage before the workspace is known", () => {
    const { result } = renderHook(() => usePhoneListDetail<State>({ area: "inbox", workspaceId: null, listHref: "/inbox" }))
    act(() => result.current.saveList({ search: "x", crew: null }))
    expect(result.current.readList()).toBeNull()
    expect(sessionStorage.length).toBe(0)
  })
})
