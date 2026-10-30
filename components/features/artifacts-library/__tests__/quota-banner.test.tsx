import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { QuotaBanner, quotaLevel } from "../quota-banner"

afterEach(cleanup)

const q = (used: number, limit = 1000, extra: Record<string, unknown> = {}) => ({ used_bytes: used, limit_bytes: limit, warning: false, retention: "keep-all", ...extra })

describe("quotaLevel", () => {
  it.each([
    [0, "none"], [799, "none"], [800, "near"], [949, "near"], [950, "critical"], [999, "critical"], [1000, "full"], [1200, "full"],
  ])("derives %i of 1000 bytes as %s when the server sends no level", (used, want) => {
    expect(quotaLevel(q(used))).toBe(want)
  })

  it("prefers the server level and hides a withheld quota", () => {
    expect(quotaLevel(q(10, 1000, { level: "critical" }))).toBe("critical")
    expect(quotaLevel(q(0, 0, { retention: "shared-version-only" }))).toBeNull()
    expect(quotaLevel(null)).toBeNull()
  })
})

describe("QuotaBanner", () => {
  it("renders nothing below the near threshold or for withheld quotas", () => {
    const { container, rerender } = render(<QuotaBanner quota={q(100, 1000, { level: "none" })} />)
    expect(container).toBeEmptyDOMElement()
    rerender(<QuotaBanner quota={q(0, 0)} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("warns politely when near, with used and limit", () => {
    render(<QuotaBanner quota={q(850 * 1024, 1000 * 1024, { level: "near", warning: true })} />)
    const banner = screen.getByRole("status")
    expect(banner).toHaveAttribute("data-level", "near")
    expect(banner).toHaveTextContent("History is getting full")
    expect(banner).toHaveTextContent("850 KB of 1000 KB (85%)")
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "85")
  })

  it("alerts when critical and full, never claiming anything was deleted", () => {
    const { rerender } = render(<QuotaBanner quota={q(960, 1000, { level: "critical", warning: true })} />)
    expect(screen.getByRole("alert")).toHaveTextContent("History is almost full")
    rerender(<QuotaBanner quota={q(1000, 1000, { level: "full", warning: true })} />)
    const alert = screen.getByRole("alert")
    expect(alert).toHaveTextContent("History is full")
    expect(alert).toHaveTextContent("Nothing in the history is deleted without you.")
  })

  it("shows the retention dry run only as a preview and offers review", () => {
    const review = vi.fn()
    render(<QuotaBanner quota={q(900, 1000, { level: "near" })} onReviewVersions={review}
      retentionPreview={{ policy: "recent-14d-then-daily", recent_days: 14, dry_run: true, would_thin: [1, 4, 6] }} />)
    expect(screen.getByRole("status")).toHaveTextContent("would free 3 versions. This is only a preview.")
    fireEvent.click(screen.getByRole("button", { name: "Review versions" }))
    expect(review).toHaveBeenCalledOnce()
  })

  it("ignores a preview that is not a dry run", () => {
    render(<QuotaBanner quota={q(900, 1000, { level: "near" })} retentionPreview={{ policy: "x", recent_days: 14, dry_run: false, would_thin: [1] }} />)
    expect(screen.getByRole("status")).not.toHaveTextContent("preview")
  })
})
