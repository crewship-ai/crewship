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
      setSearchParams("agent=mia&tab=sessions")
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

    it("preserves unrelated query params", () => {
      setSearchParams("tab=sessions&sort=name")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectAgent("mia")
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).toContain("tab=sessions")
      expect(call).toContain("sort=name")
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

    it("leaves fields untouched when key not present in updates", () => {
      setSearchParams("agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.update({ agent: "filip" })
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).toContain("agent=filip")
      expect(call).toContain("crew=research")
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

    it("preserves unrelated params", () => {
      setSearchParams("agent=mia&crew=research&tab=sessions")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.clearSelection()
      })
      expect(mocks.replace).toHaveBeenCalledWith("/crews?tab=sessions", { scroll: false })
    })
  })

  describe("pathname agnostic", () => {
    it("uses current pathname, not hardcoded /crews", () => {
      mocks.pathname = "/crews/agents/mia"
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectCrew("research")
      })
      expect(mocks.replace).toHaveBeenCalledWith(
        "/crews/agents/mia?crew=research",
        { scroll: false },
      )
    })
  })

  describe("tab URL state", () => {
    it("defaults to overview when ?tab is absent", () => {
      setSearchParams("")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.activeTab).toBe("overview")
    })

    it("defaults to overview when ?tab is invalid", () => {
      setSearchParams("tab=sessions")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.activeTab).toBe("overview")
    })

    it("reads activity from ?tab=activity", () => {
      setSearchParams("tab=activity")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.activeTab).toBe("activity")
    })

    it("reads health from ?tab=health", () => {
      setSearchParams("tab=health")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.activeTab).toBe("health")
    })

    it("setTab writes ?tab=activity", () => {
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.setTab("activity")
      })
      expect(mocks.replace).toHaveBeenCalledWith("/crews?tab=activity", { scroll: false })
    })

    // Drops the param when the value equals the default so URLs stay clean.
    it("setTab(overview) removes ?tab param", () => {
      setSearchParams("tab=activity&agent=mia")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.setTab("overview")
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).not.toContain("tab=")
      expect(call).toContain("agent=mia")
    })

    it("preserves agent/crew when switching tabs", () => {
      setSearchParams("agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.setTab("health")
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).toContain("agent=mia")
      expect(call).toContain("crew=research")
      expect(call).toContain("tab=health")
    })
  })

  describe("drawer URL state", () => {
    it("activeDrawer is null when ?drawer is absent", () => {
      setSearchParams("")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.activeDrawer).toBeNull()
    })

    it("activeDrawer is null when ?drawer is invalid", () => {
      setSearchParams("drawer=xyz")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.activeDrawer).toBeNull()
    })

    it("reads chat from ?drawer=chat", () => {
      setSearchParams("drawer=chat")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.activeDrawer).toBe("chat")
    })

    it("openDrawer writes ?drawer=logs", () => {
      setSearchParams("agent=mia")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.openDrawer("logs")
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).toContain("agent=mia")
      expect(call).toContain("drawer=logs")
    })

    it("closeDrawer removes ?drawer param", () => {
      setSearchParams("agent=mia&drawer=settings")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.closeDrawer()
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).not.toContain("drawer=")
      expect(call).toContain("agent=mia")
    })
  })

  describe("update (atomic) — tab + drawer", () => {
    it("sets agent, tab, and drawer in a single call", () => {
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.update({ agent: "mia", tab: "activity", drawer: "chat" })
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).toContain("agent=mia")
      expect(call).toContain("tab=activity")
      expect(call).toContain("drawer=chat")
    })

    it("distinguishes drawer:null (close) from drawer absent (no-op)", () => {
      setSearchParams("agent=mia&drawer=chat")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.update({ agent: "mia" })
      })
      const call = mocks.replace.mock.calls[0][0] as string
      expect(call).toContain("drawer=chat")

      mocks.replace.mockClear()
      act(() => {
        result.current.update({ drawer: null })
      })
      const call2 = mocks.replace.mock.calls[0][0] as string
      expect(call2).not.toContain("drawer=")
    })
  })
})
