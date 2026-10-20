import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

const mocks = vi.hoisted(() => ({ searchParams: new URLSearchParams() }))
vi.mock("next/navigation", () => ({
  useSearchParams: () => mocks.searchParams,
  usePathname: () => "/crews",
}))

import { useCrewsSelection } from "@/hooks/use-crews-selection"

function setURL(search: string) {
  mocks.searchParams = new URLSearchParams(search)
  window.history.replaceState(null, "", search ? `/crews?${search}` : "/crews")
}

describe("useCrewsSelection — ?tab=", () => {
  beforeEach(() => setURL(""))

  it("reads ?tab= and writes it", () => {
    setURL("crew=ops&tab=settings")
    const { result } = renderHook(() => useCrewsSelection())
    expect(result.current.tab).toBe("settings")
    act(() => result.current.setTab("env"))
    expect(new URLSearchParams(window.location.search).get("tab")).toBe("env")
  })

  it("a new crew or agent opens on Overview", () => {
    setURL("crew=ops&tab=settings")
    const { result } = renderHook(() => useCrewsSelection())
    act(() => result.current.selectCrew("research"))
    expect(new URLSearchParams(window.location.search).has("tab")).toBe(false)
    act(() => result.current.setTab("team"))
    act(() => result.current.update({ agent: "scout", crew: "research" }))
    expect(new URLSearchParams(window.location.search).has("tab")).toBe(false)
  })

  it("an update that names the tab keeps it (a rename on the agent page)", () => {
    setURL("agent=scout&tab=persona")
    const { result } = renderHook(() => useCrewsSelection())
    act(() => result.current.update({ agent: "scout-2", tab: "persona" }))
    const q = new URLSearchParams(window.location.search)
    expect(q.get("agent")).toBe("scout-2")
    expect(q.get("tab")).toBe("persona")
  })
})
