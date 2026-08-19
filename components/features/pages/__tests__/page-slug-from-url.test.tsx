import { renderHook } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { useUrlSegment } from "@/lib/use-url-segment"

vi.mock("next/navigation", () => ({
  usePathname: () => window.location.pathname,
}))

// The static-export placeholder bug, pinned for /pages.
//
// `/pages/[slug]` is exported ONCE, as /pages/_/index.html, and the Go binary
// serves that one file for every real slug. `useParams()` therefore reports
// the literal "_" and the surface renders `No page is addressed "_"` — which
// is what dev3 showed the first time a page was opened by URL.
//
// The regex is the contract; this test is here so a future refactor back to
// useParams() has to delete an assertion rather than quietly regress a route
// that only breaks in a production build.
const PAGE_PATH_RE = /^\/pages\/([^/]+)\/?$/

describe("the page slug comes from the URL, not the exported placeholder", () => {
  const at = (path: string) => {
    window.history.replaceState({}, "", path)
    return renderHook(() => useUrlSegment(PAGE_PATH_RE)).result.current
  }

  it("reads the real slug the binary was asked for", () => {
    expect(at("/pages/flotila-dev")).toBe("flotila-dev")
  })

  it("reads it with a trailing slash too — the export writes directories", () => {
    expect(at("/pages/flotila-dev/")).toBe("flotila-dev")
  })

  it("does not match the index route, which has no slug to read", () => {
    expect(at("/pages")).toBeNull()
  })

  it("would still surface the placeholder if the URL genuinely were it", () => {
    // Not a wish: /pages/_ is a real exported path. What matters is that the
    // value comes from the address bar, so a real slug can never be shadowed.
    expect(at("/pages/_")).toBe("_")
  })
})
