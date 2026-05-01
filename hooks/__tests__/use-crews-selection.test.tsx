import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// useCrewsSelection delegates to useShallowSearchParam, which:
//   - reads the initial value from useSearchParams (next/navigation) at mount
//   - writes URL changes via window.history.replaceState — NOT through
//     next/navigation — so picking another agent/crew never re-evaluates
//     the dashboard layout subtree (see the docstring on
//     hooks/use-shallow-search-param.ts).
//
// The test surface therefore needs to mock useSearchParams / usePathname
// for the *read* path and assert against window.location for the *write*
// path. Mocking useRouter().replace would assert against an API the hook
// no longer touches — and was the cause of the earlier test failures.

const mocks = vi.hoisted(() => ({
  searchParams: new URLSearchParams(),
  pathname: "/crews",
}))

vi.mock("next/navigation", () => ({
  useSearchParams: () => mocks.searchParams,
  usePathname: () => mocks.pathname,
}))

import { useCrewsSelection } from "@/hooks/use-crews-selection"

function setURL(pathname: string, search: string) {
  mocks.pathname = pathname
  mocks.searchParams = new URLSearchParams(search)
  // happy-dom honours replaceState updates to window.location, so the
  // hook's reads of window.location.{pathname,search} also see this.
  const qs = search ? `?${search}` : ""
  window.history.replaceState(null, "", `${pathname}${qs}`)
}

describe("useCrewsSelection", () => {
  beforeEach(() => {
    setURL("/crews", "")
  })

  describe("reading URL state", () => {
    it("returns null slugs when query is empty", () => {
      setURL("/crews", "")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedAgentSlug).toBeNull()
      expect(result.current.selectedCrewSlug).toBeNull()
    })

    it("reads agent slug from ?agent=", () => {
      setURL("/crews", "agent=mia")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedAgentSlug).toBe("mia")
      expect(result.current.selectedCrewSlug).toBeNull()
    })

    it("reads crew slug from ?crew=", () => {
      setURL("/crews", "crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedCrewSlug).toBe("research")
      expect(result.current.selectedAgentSlug).toBeNull()
    })

    it("reads both agent and crew when both present", () => {
      setURL("/crews", "agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedAgentSlug).toBe("mia")
      expect(result.current.selectedCrewSlug).toBe("research")
    })

    it("preserves unrelated query params", () => {
      setURL("/crews", "agent=mia&sort=name")
      const { result } = renderHook(() => useCrewsSelection())
      expect(result.current.selectedAgentSlug).toBe("mia")
    })
  })

  describe("selectAgent", () => {
    it("writes agent slug to URL", () => {
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectAgent("mia")
      })
      expect(window.location.pathname + window.location.search).toBe(
        "/crews?agent=mia",
      )
    })

    it("clears agent param when called with null", () => {
      setURL("/crews", "agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectAgent(null)
      })
      expect(window.location.search).toBe("?crew=research")
    })

    it("does not touch crew param", () => {
      setURL("/crews", "crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectAgent("mia")
      })
      expect(window.location.search).toContain("crew=research")
      expect(window.location.search).toContain("agent=mia")
    })
  })

  describe("selectCrew (mutual exclusivity)", () => {
    it("writes crew slug and clears agent", () => {
      setURL("/crews", "agent=mia")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectCrew("research")
      })
      expect(window.location.search).toContain("crew=research")
      expect(window.location.search).not.toContain("agent=")
    })

    it("clears crew param when called with null", () => {
      setURL("/crews", "crew=research&agent=mia")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectCrew(null)
      })
      expect(window.location.search).not.toContain("crew=")
      expect(window.location.search).not.toContain("agent=")
    })
  })

  describe("update (atomic)", () => {
    it("sets both agent and crew in single call", () => {
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.update({ agent: "filip", crew: "devops" })
      })
      expect(window.location.search).toContain("agent=filip")
      expect(window.location.search).toContain("crew=devops")
    })

    it("distinguishes null (clear) from undefined (no-op)", () => {
      setURL("/crews", "agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.update({ agent: null })
      })
      expect(window.location.search).not.toContain("agent=")
      expect(window.location.search).toContain("crew=research")
    })
  })

  describe("clearSelection", () => {
    it("removes both agent and crew", () => {
      setURL("/crews", "agent=mia&crew=research")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.clearSelection()
      })
      expect(window.location.pathname).toBe("/crews")
      expect(window.location.search).toBe("")
    })
  })

  describe("pathname agnostic", () => {
    it("uses current pathname, not hardcoded /crews", () => {
      setURL("/some/other/path", "")
      const { result } = renderHook(() => useCrewsSelection())
      act(() => {
        result.current.selectCrew("research")
      })
      expect(window.location.pathname).toBe("/some/other/path")
      expect(window.location.search).toBe("?crew=research")
    })
  })
})
