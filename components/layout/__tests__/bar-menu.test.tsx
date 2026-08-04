import { useState } from "react"
import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitForElementToBeRemoved } from "@testing-library/react"
import { Bell } from "lucide-react"

import {
  BarMenu,
  BarMenuBody,
  BarMenuEmpty,
  BarMenuFooter,
  BarMenuFooterAction,
  BarMenuFooterLink,
  BarMenuHeader,
  BarMenuRow,
  BarMenuSection,
} from "../bar-menu"

afterEach(cleanup)

// The top bar's three popovers (Activity, Inbox, Notifications) render from
// this one kit so they cannot drift apart again. What is asserted here is the
// shared contract — badge, open/close, section header, row, footer — not any
// one bell's data.

function Harness({ badge, onOpenChange }: { badge?: number; onOpenChange?: (o: boolean) => void }) {
  const [open, setOpen] = useState(false)
  return (
    <BarMenu
      icon={Bell}
      ariaLabel="Notifications"
      open={open}
      onOpenChange={(v: boolean) => {
        setOpen(v)
        onOpenChange?.(v)
      }}
      badge={badge != null ? { count: badge, tone: "info" } : undefined}
      testId="probe"
    >
      <BarMenuHeader title="Notifications" meta="3 unread" />
      <BarMenuBody>
        <BarMenuSection label="Recent" count={2}>
          <BarMenuRow testId="probe-row-a" title="Row A" meta="alex · chat.replies" trailing="3 Aug" onClick={vi.fn()} />
        </BarMenuSection>
      </BarMenuBody>
      <BarMenuFooter>
        <BarMenuFooterAction onClick={vi.fn()}>Mark 3 read</BarMenuFooterAction>
        <BarMenuFooterLink onClick={vi.fn()}>Open all →</BarMenuFooterLink>
      </BarMenuFooter>
    </BarMenu>
  )
}

describe("BarMenu", () => {
  it("hides the badge at zero and shows it above zero", () => {
    const { rerender } = render(<Harness badge={0} />)
    expect(screen.queryByTestId("probe-badge")).not.toBeInTheDocument()

    rerender(<Harness badge={7} />)
    expect(screen.getByTestId("probe-badge")).toHaveTextContent("7")
  })

  it("caps the badge at 99+ so a busy workspace cannot widen the top bar", () => {
    render(<Harness badge={412} />)
    expect(screen.getByTestId("probe-badge")).toHaveTextContent("99+")
  })

  it("opens on click and closes on Escape", async () => {
    render(<Harness />)
    const trigger = screen.getByTestId("probe-trigger")
    expect(screen.queryByTestId("probe-popover")).not.toBeInTheDocument()

    fireEvent.click(trigger)
    expect(screen.getByTestId("probe-popover")).toBeInTheDocument()
    expect(trigger).toHaveAttribute("aria-expanded", "true")

    // Radix gave the old dropdowns Escape for free; the inbox popover it is
    // being aligned to never had it. The kit owes every bell the same key.
    fireEvent.keyDown(document, { key: "Escape" })
    expect(trigger).toHaveAttribute("aria-expanded", "false")
    // The panel plays its exit before unmounting, so assert on removal
    // rather than on the frame right after the key.
    await waitForElementToBeRemoved(() => screen.queryByTestId("probe-popover"))
  })

  it("closes when the backdrop is clicked", async () => {
    render(<Harness />)
    fireEvent.click(screen.getByTestId("probe-trigger"))
    fireEvent.click(screen.getByTestId("probe-backdrop"))
    await waitForElementToBeRemoved(() => screen.queryByTestId("probe-popover"))
  })

  it("renders the section header as label + right-aligned count", () => {
    render(<Harness />)
    fireEvent.click(screen.getByTestId("probe-trigger"))

    const section = screen.getByTestId("bar-menu-section-recent")
    expect(within(section).getByText("Recent")).toBeInTheDocument()
    expect(within(section).getByText("2")).toBeInTheDocument()
  })

  it("gives every row the same title / meta / trailing skeleton", () => {
    render(<Harness />)
    fireEvent.click(screen.getByTestId("probe-trigger"))

    const row = screen.getByTestId("probe-row-a")
    expect(within(row).getByText("Row A")).toBeInTheDocument()
    expect(within(row).getByText("alex · chat.replies")).toBeInTheDocument()
    expect(within(row).getByText("3 Aug")).toBeInTheDocument()
  })

  it("keeps the footer's secondary action left and the primary link right", () => {
    render(<Harness />)
    fireEvent.click(screen.getByTestId("probe-trigger"))

    const footer = screen.getByTestId("bar-menu-footer")
    const [first, second] = within(footer).getAllByRole("button")
    expect(first).toHaveTextContent("Mark 3 read")
    expect(second).toHaveTextContent("Open all →")
    expect(second.className).toContain("ml-auto")
  })
})

describe("BarMenuEmpty", () => {
  it("states the empty case rather than rendering a blank panel", () => {
    render(<BarMenuEmpty icon={Bell} message="All caught up" />)
    expect(screen.getByText("All caught up")).toBeInTheDocument()
  })
})
