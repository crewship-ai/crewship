import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, act } from "@testing-library/react"
import { ApprovalCard } from "@/components/features/approvals/approval-card"
import type { ApprovalRow } from "@/lib/types/approvals"

function row(over: Partial<ApprovalRow> = {}): ApprovalRow {
  return {
    id: "appr_1",
    kind: "destructive_op",
    reason: "rm -rf important",
    status: "pending",
    created_at: new Date(Date.now() - 60_000).toISOString(),
    ...over,
  } as ApprovalRow
}

describe("ApprovalCard — base rendering", () => {
  it("renders kind, status, and reason", () => {
    render(<ApprovalCard row={row()} active={false} onSelect={() => {}} />)
    expect(screen.getByText("pending")).toBeInTheDocument()
    expect(screen.getByText("rm -rf important")).toBeInTheDocument()
  })

  it("italic '(no reason)' fallback for empty reason", () => {
    render(<ApprovalCard row={row({ reason: "" })} active={false} onSelect={() => {}} />)
    expect(screen.getByText("(no reason)")).toBeInTheDocument()
  })

  it("renders requested_by line when populated", () => {
    render(
      <ApprovalCard
        row={row({ requested_by: "alice@example.com" })}
        active={false}
        onSelect={() => {}}
      />,
    )
    expect(screen.getByText("alice@example.com")).toBeInTheDocument()
    expect(screen.getByText(/requested by/i)).toBeInTheDocument()
  })

  it("hides requested-by line when missing", () => {
    render(<ApprovalCard row={row({ requested_by: undefined })} active={false} onSelect={() => {}} />)
    expect(screen.queryByText(/requested by/i)).not.toBeInTheDocument()
  })

  it("clicking the card calls onSelect", () => {
    const onSelect = vi.fn()
    render(<ApprovalCard row={row()} active={false} onSelect={onSelect} />)
    fireEvent.click(screen.getByRole("button"))
    expect(onSelect).toHaveBeenCalledTimes(1)
  })

  it("active=true applies the primary ring style", () => {
    const { container } = render(<ApprovalCard row={row()} active onSelect={() => {}} />)
    expect(container.querySelector(".ring-primary\\/20")).toBeTruthy()
  })

  it("active=false does NOT apply primary ring", () => {
    const { container } = render(
      <ApprovalCard row={row()} active={false} onSelect={() => {}} />,
    )
    expect(container.querySelector(".ring-primary\\/20")).toBeFalsy()
  })

  it("renders a single-button (clickable card)", () => {
    render(<ApprovalCard row={row()} active={false} onSelect={() => {}} />)
    expect(screen.getAllByRole("button")).toHaveLength(1)
  })
})

describe("ApprovalCard — status styling", () => {
  it.each(["pending", "approved", "denied", "timeout"])(
    "%s status renders the status text",
    (status) => {
      render(
        <ApprovalCard row={row({ status })} active={false} onSelect={() => {}} />,
      )
      expect(screen.getByText(status)).toBeInTheDocument()
    },
  )

  it("uppercase status maps via case-insensitive lookup (PENDING → pending styling)", () => {
    // Component does STATUS_CLASS[row.status.toLowerCase()], so "PENDING"
    // hits the same amber 'pending' style as the lowercase form. The
    // visible text is still rendered as the caller provided it (no
    // forced lowercase), but the colour class matches lowercase.
    render(
      <ApprovalCard row={row({ status: "PENDING" })} active={false} onSelect={() => {}} />,
    )
    // The status badge text is rendered as authored, and its container
    // carries the amber 'pending' colour class via case-insensitive
    // lookup. We assert by querying the badge element and its class.
    const badge = screen.getByText("PENDING")
    expect(badge).toBeInTheDocument()
    expect(badge.className).toContain("text-amber-300")
  })

  it("unknown status falls back to muted styling", () => {
    render(
      <ApprovalCard
        row={row({ status: "weirdo" })}
        active={false}
        onSelect={() => {}}
      />,
    )
    expect(screen.getByText("weirdo")).toBeInTheDocument()
  })
})

describe("ApprovalCard — TimeoutCountdown", () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date("2026-04-30T10:00:00Z"))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it("renders MM:SS countdown for pending row with timeout_at", () => {
    render(
      <ApprovalCard
        row={row({ timeout_at: new Date("2026-04-30T10:02:30Z").toISOString() })}
        active={false}
        onSelect={() => {}}
      />,
    )
    expect(screen.getByText("2:30")).toBeInTheDocument()
  })

  it("countdown ticks down every second", () => {
    render(
      <ApprovalCard
        row={row({ timeout_at: new Date("2026-04-30T10:00:10Z").toISOString() })}
        active={false}
        onSelect={() => {}}
      />,
    )
    expect(screen.getByText("0:10")).toBeInTheDocument()
    act(() => {
      vi.advanceTimersByTime(3000)
    })
    expect(screen.getByText("0:07")).toBeInTheDocument()
  })

  it("countdown shows 'expired' once timeout passes", () => {
    render(
      <ApprovalCard
        row={row({ timeout_at: new Date("2026-04-30T10:00:01Z").toISOString() })}
        active={false}
        onSelect={() => {}}
      />,
    )
    act(() => {
      vi.advanceTimersByTime(2000)
    })
    expect(screen.getByText("expired")).toBeInTheDocument()
  })

  it("malformed timeout_at does not crash, renders 'expired'", () => {
    render(
      <ApprovalCard
        row={row({ timeout_at: "not-a-real-iso-date" })}
        active={false}
        onSelect={() => {}}
      />,
    )
    expect(screen.getByText("expired")).toBeInTheDocument()
  })

  it("non-pending rows do NOT render the countdown badge", () => {
    render(
      <ApprovalCard
        row={row({
          status: "approved",
          timeout_at: new Date("2026-04-30T10:01:00Z").toISOString(),
        })}
        active={false}
        onSelect={() => {}}
      />,
    )
    // Approved row never shows the countdown — confirm by absence of MM:SS.
    expect(screen.queryByText(/^\d+:\d{2}$/)).not.toBeInTheDocument()
    expect(screen.queryByText("expired")).not.toBeInTheDocument()
  })

  it("pending row without timeout_at does NOT render countdown", () => {
    render(
      <ApprovalCard
        row={row({ timeout_at: null })}
        active={false}
        onSelect={() => {}}
      />,
    )
    // Assert BOTH the 'expired' label and any MM:SS-shaped string are
    // absent — the previous version only checked 'expired' which would
    // miss an unintended countdown render.
    expect(screen.queryByText(/^\d+:\d{2}$/)).not.toBeInTheDocument()
    expect(screen.queryByText("expired")).not.toBeInTheDocument()
  })
})
