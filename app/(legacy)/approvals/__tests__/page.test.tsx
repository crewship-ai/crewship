import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, cleanup } from "@testing-library/react"
import { renderToStaticMarkup } from "react-dom/server"

// /approvals is a redirect, not a page: approval requests are decided in the
// Inbox (one decision layout for every request). The route must keep existing
// because notification emails, Slack "Review" buttons and
// `crewship open approvals` still build <server>/approvals.
const h = vi.hoisted(() => ({ query: "" }))
const replace = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace, push: vi.fn(), back: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(h.query),
}))

import LegacyApprovalsRedirect from "../page"

describe("/approvals", () => {
  beforeEach(() => {
    cleanup()
    replace.mockClear()
    h.query = ""
  })

  it("redirects to the decisions waiting in the Inbox", () => {
    render(<LegacyApprovalsRedirect />)
    expect(replace).toHaveBeenCalledWith("/inbox?attention=approvals")
  })

  it("carries the agent filter from escalation links", () => {
    h.query = "agent_id=ag_1"
    render(<LegacyApprovalsRedirect />)
    expect(replace).toHaveBeenCalledWith("/inbox?attention=approvals&agent_id=ag_1")
  })

  it("sends decided requests to Inbox History", () => {
    h.query = "status=decided"
    render(<LegacyApprovalsRedirect />)
    expect(replace).toHaveBeenCalledWith("/inbox?view=history")
  })

  // happy-dom serializes <noscript> as empty, so render to static markup.
  it("has a no-JavaScript fallback to the Inbox", () => {
    const html = renderToStaticMarkup(<LegacyApprovalsRedirect />)
    expect(html).toContain("url=/inbox?attention=approvals")
    expect(html).toContain('href="/inbox?attention=approvals"')
  })

  it("renders none of the old page", () => {
    const { container } = render(<LegacyApprovalsRedirect />)
    expect(container.textContent ?? "").not.toMatch(/admin surface|Inbox clear|not yet configured/i)
  })
})
