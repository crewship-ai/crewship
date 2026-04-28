import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

const mocks = vi.hoisted(() => ({
  replace: vi.fn(),
  searchParams: new URLSearchParams(),
  pathname: "/crews",
}))

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    replace: mocks.replace,
    push: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  useSearchParams: () => mocks.searchParams,
  usePathname: () => mocks.pathname,
}))

import { useCrewsSelection } from "@/hooks/use-crews-selection"

function setSearchParams(init: string) {
  mocks.searchParams = new URLSearchParams(init)
}

describe("useCrewsSelection", () => {
  beforeEach(() => {
    mocks.replace.mockClear()
    mocks.pathname = "/crews"
    setSearchParams("")
  })

  describe("reading URL state", () => {
    it("returns null slugs when query is empty", () => {
      setSearchParams("")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedAgentSlug).toBeNull()
      expect(result.current.selectedCrewSlug).toBeNull()
    })

    it("reads agent slug from ?agent=", () => {
      setSearchParams("agent=mia")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedAgentSlug).toBe("mia")
      expect(result.current.selectedCrewSlug).toBeNull()
    })

    it("reads crew slug from ?crew=", () => {
      setSearchParams("crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedCrewSlug).toBe("research")
      expect(result.current.selectedAgentSlug).toBeNull()
    })

    it("reads both agent and crew when both present", () => {
      setSearchParams("agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedAgentSlug).toBe("mia")
      expect(result.current.selectedCrewSlug).toBe("research")
    })

    it("preserves unrelated query params", () => {
      setSearchParams("agent=mia&sort=name")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedAgentSlug).toBe("mia")
    })
  })

  describe("selectAgent", () => {
    it("writes agent slug to URL with scroll:false", () => {
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectAgent("mia")
      })
      expect(mocks.replace).toHaveBeenCalledWith("/crews?agent=mia", { scroll: false })
    })

    it("clears agent param when called with null", () => {
      setSearchParams("agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectAgent(null)
      })
      expect(mocks.replace).toHaveBeenCalledWith("/crews?crew=research", { scroll: false })
    })

    it("does not touch crew param", () => {
      setSearchParams("crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectAgent("mia")
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).toContain("crew=research")
      expect(call).toContain("agent=mia")
    })
  })

  describe("selectCrew (mutual exclusivity)", () => {
    it("writes crew slug and clears agent", () => {
      setSearchParams("agent=mia")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectCrew("research")
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).toContain("crew=research")
      expect(call).not.toContain("agent=")
    })

    it("clears crew param when called with null", () => {
      setSearchParams("crew=research&agent=mia")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectCrew(null)
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).not.toContain("crew=")
      expect(call).not.toContain("agent=")
    })
  })

  describe("update (atomic)", () => {
    it("sets both agent and crew in single call", () => {
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.update({ agent: "filip", crew: "devops" })
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).toContain("agent=filip")
      expect(call).toContain("crew=devops")
    })

    it("distinguishes null (clear) from undefined (no-op)", () => {
      setSearchParams("agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.update({ agent: null })
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).not.toContain("agent=")
      expect(call).toContain("crew=research")
    })
  })

  describe("clearSelection", () => {
    it("removes both agent and crew", () => {
      setSearchParams("agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.clearSelection()
      })
      expect(mocks.replace).toHaveBeenCalledWith("/crews", { scroll: false })
    })
  })

  describe("pathname agnostic", () => {
    it("uses current pathname, not hardcoded /crews", () => {
      mocks.pathname = "/some/other/path"
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectCrew("research")
      })
      expect(mocks.replace).toHaveBeenCalledWith(
        "/some/other/path?crew=research",
        { scroll: false },
      )
    })
  })
})
