import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"

import { AttentionTiles, buildAttentionItems } from "../dashboard-overview"
import type { InboxItem } from "@/hooks/use-inbox"

afterEach(cleanup)

const all = () =>
  buildAttentionItems({
    inbox: [{ id: "f1", kind: "failed_run", state: "unread", title: "Nightly backlog triage" }] as InboxItem[],
    heldCrews: [{ crew_id: "c1", reason: "memory", detail: "Host memory low", since: "", waited_ms: 0 }],
    reviewCount: 2,
    reviewIdentifiers: ["CRW-148", "CRW-151", "CRW-160"],
    activeByKind: { failed_run: 1, schedule_missed: 3 },
    decisionCount: 2,
  })

describe("attention tiles", () => {
  it("priority order and max four", () => {
    const items = all()
    expect(items.map((i) => i.id)).toEqual(["approvals", "failures", "reviews", "capacity", "schedules"])
    render(<AttentionTiles items={items} />)
    const tiles = screen.getAllByRole("link").filter((a) => a.getAttribute("data-slot") === "attention-tile")
    expect(tiles).toHaveLength(4)
    expect(tiles.map((t) => t.querySelector("b")?.textContent)).toEqual(["Decisions waiting", "Failed run", "Issues for review", "Waiting for capacity"])
    // The number sits in its own tile, in the tone of the state.
    expect(within(tiles[1]).getByText("1")).toBeInTheDocument()
    expect(within(tiles[1]).getByText("Nightly backlog triage")).toBeInTheDocument()
    expect(within(tiles[2]).getByText("CRW-148, CRW-151")).toBeInTheDocument()
    expect(tiles[2]).toHaveAttribute("href", "/issues?status=REVIEW")
    expect(tiles[0]).toHaveAttribute("href", "/inbox?attention=approvals")
  })

  it("attention overflow line names the rest", () => {
    render(<AttentionTiles items={all()} />)
    expect(screen.getByText("+1 more:")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "3 schedule alerts" })).toHaveAttribute("href", "/inbox?attention=schedule-alerts")
    expect(screen.getByRole("link", { name: "Open Inbox" })).toHaveAttribute("href", "/inbox")
  })

  it("unknown inbox renders Could not check, not All clear", () => {
    const retry = vi.fn()
    render(<AttentionTiles items={[]} inboxError="403" onRetry={retry} />)
    expect(screen.getByText("Could not check the inbox")).toBeInTheDocument()
    expect(screen.queryByText(/Nothing needs you/)).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    expect(retry).toHaveBeenCalledOnce()
  })

  it("says nothing needs you only when the inbox answered", () => {
    const { rerender } = render(<AttentionTiles items={[]} inboxLoading />)
    expect(screen.getByText("Checking what needs you…")).toBeInTheDocument()
    rerender(<AttentionTiles items={[]} />)
    expect(screen.getByText("Nothing needs you right now")).toBeInTheDocument()
  })

  it("marks shown tiles as incomplete when the inbox could not be read", () => {
    const capacityOnly = buildAttentionItems({ inbox: [], heldCrews: [{ crew_id: "c1", reason: "memory", since: "", waited_ms: 0 }] })
    render(<AttentionTiles items={capacityOnly} inboxError="403" />)
    expect(screen.getByText(/the inbox could not be read/)).toBeInTheDocument()
  })
})
