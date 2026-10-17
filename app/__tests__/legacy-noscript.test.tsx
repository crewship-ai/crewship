import { describe, it, expect, vi, beforeEach } from "vitest"
import { existsSync } from "node:fs"
import path from "node:path"
import { render, cleanup } from "@testing-library/react"
import { renderToStaticMarkup } from "react-dom/server"

// CHR-05 (#3482, inventory Z01/Z02): old links must redirect without
// JavaScript too. Inside the (dashboard) group the layout prerenders only its
// loading frame until the session resolves, so a page's <noscript> never
// reached the exported HTML: /agents showed the layout spinner, /audit had no
// meta refresh at all. Like /approvals (#3406) they live in the layout-less
// (legacy) group.
const replace = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace, push: vi.fn(), back: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  redirect: vi.fn(),
}))

const APP = path.resolve(__dirname, "..")

const ROUTES = [
  { route: "agents", target: "/crews", load: () => import("../(legacy)/agents/page") },
  { route: "audit", target: "/settings/audit", load: () => import("../(legacy)/audit/page") },
] as const

describe.each(ROUTES)("legacy /$route", ({ route, target, load }) => {
  beforeEach(() => {
    cleanup()
    replace.mockClear()
  })

  it("lives outside the dashboard layout", () => {
    expect(existsSync(path.join(APP, "(legacy)", route, "page.tsx"))).toBe(true)
    expect(existsSync(path.join(APP, "(dashboard)", route, "page.tsx"))).toBe(false)
  })

  it(`redirects to ${target} with JavaScript`, async () => {
    const { default: Page } = await load()
    render(<Page />)
    expect(replace).toHaveBeenCalledWith(target)
  })

  it(`redirects to ${target} without JavaScript (meta refresh and a link)`, async () => {
    const { default: Page } = await load()
    const html = renderToStaticMarkup(<Page />)
    expect(html).toContain(`<noscript>`)
    expect(html).toContain(`content="0;url=${target}"`)
    expect(html).toContain(`href="${target}"`)
  })
})
