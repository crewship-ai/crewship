import { describe, it, expect, afterEach, vi } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"

// usePathname is the reactivity signal; in jsdom return the live pathname
// so changing window.location + rerender re-runs the effect (mirrors a
// client-side route change).
vi.mock("next/navigation", () => ({
  usePathname: () => (typeof window !== "undefined" ? window.location.pathname : "/"),
}))

import { useUrlSegment } from "../use-url-segment"

// Pin the static-export regression: pages must read their dynamic segment
// from window.location.pathname, because useParams() returns the "_"
// prerender placeholder. useUrlSegment is the shared primitive behind the
// issue / skill / mission / orchestration-redirect fixes.

const ISSUE_RE = /^\/issues\/([^/]+)\/?$/
const SKILL_RE = /^\/skills\/([^/]+)\/?$/
const MISSION_TIMELINE_RE = /^\/missions\/([^/]+)\/timeline\/?$/

function setPath(pathname: string) {
  Object.defineProperty(window, "location", {
    configurable: true,
    writable: true,
    value: { ...window.location, pathname },
  })
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe("useUrlSegment", () => {
  it("reads the real identifier from the URL (the OPS-4 inbox-link bug)", async () => {
    setPath("/issues/OPS-4")
    const { result } = renderHook(() => useUrlSegment(ISSUE_RE))
    await waitFor(() => expect(result.current).toBe("OPS-4"))
  })

  it("returns null before mount / on a non-matching path", () => {
    setPath("/something/else")
    const { result } = renderHook(() => useUrlSegment(ISSUE_RE))
    // No matching segment → stays null (callers treat null as "don't fetch").
    expect(result.current).toBeNull()
  })

  it("extracts a middle segment (mission id in /missions/<id>/timeline)", async () => {
    setPath("/missions/cmqx123/timeline")
    const { result } = renderHook(() => useUrlSegment(MISSION_TIMELINE_RE))
    await waitFor(() => expect(result.current).toBe("cmqx123"))
  })

  it("decodes percent-encoded segments", async () => {
    setPath("/skills/sk%20with%20space")
    const { result } = renderHook(() => useUrlSegment(SKILL_RE))
    await waitFor(() => expect(result.current).toBe("sk with space"))
  })

  it("never resolves to the '_' placeholder for a real URL", async () => {
    setPath("/skills/sk_8694035d52a4f792b8a411af")
    const { result } = renderHook(() => useUrlSegment(SKILL_RE))
    await waitFor(() => expect(result.current).toBe("sk_8694035d52a4f792b8a411af"))
    expect(result.current).not.toBe("_")
  })

  it("re-reads on client-side navigation between sibling routes (no remount)", async () => {
    setPath("/issues/OPS-1")
    const { result, rerender } = renderHook(() => useUrlSegment(ISSUE_RE))
    await waitFor(() => expect(result.current).toBe("OPS-1"))

    // Simulate an in-app Link nav to a sibling issue — same component
    // instance, new URL. Without the usePathname dep this stayed "OPS-1".
    setPath("/issues/OPS-2")
    rerender()
    await waitFor(() => expect(result.current).toBe("OPS-2"))
  })

  it("falls back to the raw segment on malformed percent-encoding (no crash)", async () => {
    setPath("/issues/50%off")
    const { result } = renderHook(() => useUrlSegment(ISSUE_RE))
    // decodeURIComponent('50%off') throws URIError; the hook must not crash.
    await waitFor(() => expect(result.current).toBe("50%off"))
  })
})
