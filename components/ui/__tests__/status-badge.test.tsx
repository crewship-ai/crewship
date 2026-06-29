import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { STATUS_BADGE_CLASSES, STATUS_DOT_CLASSES } from "@/lib/colors"

// These tests pin the exact rendered contract (label + className) of the
// shared status primitives. Any feature that migrates a local status map
// onto <StatusBadge>/<StatusDot> can only do so safely if the local map
// produced byte-identical output — so this is the reference the migration
// is checked against. Changing a class string here is a deliberate visual
// change, not an incidental one.

describe("StatusBadge", () => {
  // Label is humanized from the canonical status token.
  it.each([
    ["PENDING", "Pending"],
    ["BLOCKED", "Blocked"],
    ["IN_PROGRESS", "In Progress"],
    ["COMPLETED", "Completed"],
    ["FAILED", "Failed"],
    ["SKIPPED", "Skipped"],
    ["AWAITING_APPROVAL", "Awaiting Approval"],
  ])("status=%s humanizes label to %s", (status, label) => {
    render(<StatusBadge status={status} />)
    expect(screen.getByText(label)).toBeInTheDocument()
  })

  // className routes through STATUS_BADGE_CLASSES for every canonical key,
  // plus the fixed primitive chrome (border-transparent gap-1.5).
  it.each(Object.keys(STATUS_BADGE_CLASSES))(
    "status=%s emits STATUS_BADGE_CLASSES + chrome",
    (status) => {
      const { container } = render(<StatusBadge status={status} label="x" />)
      const badge = container.querySelector('[data-slot="badge"]')!
      expect(badge).toBeTruthy()
      for (const cls of STATUS_BADGE_CLASSES[status].split(" ")) {
        expect(badge.classList.contains(cls)).toBe(true)
      }
      expect(badge.classList.contains("border-transparent")).toBe(true)
      expect(badge.classList.contains("gap-1.5")).toBe(true)
      // outline variant is applied (its hover class survives tailwind-merge;
      // text-foreground is intentionally overridden by the status color).
      expect(badge.getAttribute("data-variant")).toBe("outline")
    },
  )

  it("falls back to muted classes for an unknown status", () => {
    const { container } = render(<StatusBadge status="NOT_A_STATUS" label="x" />)
    const badge = container.querySelector('[data-slot="badge"]')!
    expect(badge.classList.contains("bg-muted")).toBe(true)
    expect(badge.classList.contains("text-muted-foreground")).toBe(true)
  })

  it("honors an explicit label override", () => {
    render(<StatusBadge status="COMPLETED" label="Done deal" />)
    expect(screen.getByText("Done deal")).toBeInTheDocument()
    expect(screen.queryByText("Completed")).not.toBeInTheDocument()
  })

  it("merges a caller className without dropping status classes", () => {
    const { container } = render(
      <StatusBadge status="FAILED" label="x" className="text-[10px]" />,
    )
    const badge = container.querySelector('[data-slot="badge"]')!
    expect(badge.classList.contains("text-[10px]")).toBe(true)
    expect(badge.classList.contains("text-red-400")).toBe(true)
  })

  it("renders a leading dot only when withDot is set", () => {
    // The dot is the only inline-block span (the Badge itself is inline-flex).
    const { container: without } = render(
      <StatusBadge status="IN_PROGRESS" label="x" />,
    )
    expect(without.querySelector("span.inline-block")).toBeNull()

    const { container: withIt } = render(
      <StatusBadge status="IN_PROGRESS" label="x" withDot />,
    )
    expect(withIt.querySelector("span.inline-block")).toBeTruthy()
  })
})

describe("StatusDot", () => {
  it.each(Object.keys(STATUS_DOT_CLASSES))(
    "status=%s emits STATUS_DOT_CLASSES color",
    (status) => {
      const { container } = render(<StatusDot status={status} />)
      const dot = container.querySelector("span")!
      expect(dot.classList.contains(STATUS_DOT_CLASSES[status])).toBe(true)
      expect(dot.classList.contains("rounded-full")).toBe(true)
    },
  )

  it("falls back to slate for an unknown status", () => {
    const { container } = render(<StatusDot status="NOT_A_STATUS" />)
    expect(container.querySelector("span")!.classList.contains("bg-slate-400")).toBe(true)
  })

  it("adds the pulse class only when live", () => {
    const { container: still } = render(<StatusDot status="IN_PROGRESS" />)
    expect(still.querySelector("span")!.classList.contains("agent-active-dot")).toBe(false)

    const { container: live } = render(<StatusDot status="IN_PROGRESS" live />)
    expect(live.querySelector("span")!.classList.contains("agent-active-dot")).toBe(true)
  })

  it("is aria-hidden (decorative)", () => {
    const { container } = render(<StatusDot status="COMPLETED" />)
    expect(container.querySelector("span")!.getAttribute("aria-hidden")).toBe("true")
  })
})
