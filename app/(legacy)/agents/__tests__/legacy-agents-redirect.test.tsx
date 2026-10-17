import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, cleanup } from "@testing-library/react"
import { renderToStaticMarkup } from "react-dom/server"

// /agents is a redirect stub, and the only thing worth asserting about it is
// where it sends people. It sent them to /crews/agents for as long as it has
// existed — a route with no page.tsx and no crews/agents.html in the static
// export, so the Go handler fell the click through to the SPA root and the
// user landed on the dashboard under a URL that said agents. The scan in
// app/(onboarding)/onboarding/__tests__/dead-agent-routes.test.ts pins the
// absence of the dead string; this pins the presence of a live destination,
// which is the half a string scan cannot check.
const replace = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace, push: vi.fn(), back: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
}))

import LegacyAgentsRedirect from "../page"

describe("/agents", () => {
  beforeEach(() => {
    cleanup()
    replace.mockClear()
  })

  // Plain /crews, not /crews?agent=<slug>: this page has no agent in scope,
  // and /crews matches ?agent= against agent.slug (hooks/use-crews-selection),
  // so a value it cannot supply would be discarded and leave an empty canvas.
  it("redirects to the /crews roster", () => {
    render(<LegacyAgentsRedirect />)
    expect(replace).toHaveBeenCalledWith("/crews")
  })

  // The route is not decoration — `crewship open agents` builds <server>/agents
  // (cmd/crewship/cmd_open.go), so deleting the page would break the CLI. It
  // has to keep existing AND keep pointing somewhere real.
  it("still exists as a route for `crewship open agents` to land on", () => {
    expect(typeof LegacyAgentsRedirect).toBe("function")
  })

  // The <noscript> path is the one a crawler or a scripting-disabled browser
  // takes; it carried the same dead URL twice — meta refresh and visible
  // link — and a test that only watched the router would have missed both.
  // Rendered through renderToStaticMarkup rather than the DOM: happy-dom
  // serializes <noscript> as empty, so the assertion would pass vacuously.
  it("sends the no-JavaScript fallback to the same live route", () => {
    const html = renderToStaticMarkup(<LegacyAgentsRedirect />)
    expect(html).not.toContain(["/crews", "agents"].join("/"))
    expect(html).toContain("url=/crews")
    expect(html).toContain('href="/crews"')
  })
})
