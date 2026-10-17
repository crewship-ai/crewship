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

  it("opens the agent's waiting work in Activity, not the old Journal (F-02)", () => {
    // Regression: an earlier version linked to /orchestration?tab=approvals&agent=<slug>,
    // which ignored its query; then to /journal, which only redirects now.
    render(<InboxBanner agentId="agent-uuid-123" count={2} summary="2 escalations" />)
    const link = screen.getByRole("link", { name: "Open in Activity" })
    expect(link).toHaveAttribute("href", "/activity?view=need&agent=agent-uuid-123")
  })

  it("renders summary when provided", () => {
    render(<InboxBanner agentId="abc" count={2} summary="1 escalation · 1 assignment" />)
    expect(screen.getByText("1 escalation · 1 assignment")).toBeInTheDocument()
  })
})
