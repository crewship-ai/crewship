import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { InboxBanner } from "@/components/features/crews/inbox-banner"

describe("<InboxBanner>", () => {
  it("renders nothing when count is zero", () => {
    const { container } = render(<InboxBanner agentId="abc" count={0} />)
    expect(container.firstChild).toBeNull()
  })

  it("renders with singular wording for count=1", () => {
    render(<InboxBanner agentId="abc" count={1} />)
    expect(screen.getByText(/1 item waiting/)).toBeInTheDocument()
  })

  it("renders with plural wording for count>1", () => {
    render(<InboxBanner agentId="abc" count={3} />)
    expect(screen.getByText(/3 items waiting/)).toBeInTheDocument()
  })

  it("Open inbox link uses agent_id (the param /journal actually accepts)", () => {
    // Regression: an earlier version linked to /orchestration?tab=approvals&agent=<slug>,
    // but /orchestration ignores all query params and the link was a dead link.
    // This test pins the working contract: deep-link to /journal with
    // agent_id + entry_type filter (both of which /journal honors).
    render(<InboxBanner agentId="agent-uuid-123" count={2} summary="2 escalations" />)
    const link = screen.getByRole("link", { name: /Open inbox/ })
    const href = link.getAttribute("href")
    expect(href).toMatch(/^\/journal\?/)
    expect(href).toContain("agent_id=agent-uuid-123")
    expect(href).toContain("entry_type=")
    // Specifically NOT the dead orchestration deep-link:
    expect(href).not.toContain("/orchestration")
  })

  it("renders summary when provided", () => {
    render(<InboxBanner agentId="abc" count={2} summary="1 escalation · 1 assignment" />)
    expect(screen.getByText("1 escalation · 1 assignment")).toBeInTheDocument()
  })
})
