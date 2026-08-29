import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// #2157 — the jump control on the detail pane must be a real link.
//
// jumpFor returned a label and an icon and no href, and the pane rendered it
// as a plain <Button>: it computed which destination to name, named it, and
// did nothing when clicked. It was the only control on the pane for reaching
// the run an approval describes.
//
// The unit test in inbox-derive.test.ts covers what jumpFor RETURNS. It passed
// throughout, because the derivation was never the broken half — the wiring
// was. So these assert the rendered DOM: an <a> carrying an href. Anything
// that checks the label alone would go green against the original bug.

vi.mock("../waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))
vi.mock("../kind-actions", () => ({ KindActions: () => null }))

import { InboxDetail } from "../inbox-detail"

function item(over: Partial<InboxItem> & Pick<InboxItem, "id" | "kind" | "title">): InboxItem {
  const ts = new Date("2026-08-29T16:52:00Z").toISOString()
  return {
    workspace_id: "ws",
    source_id: `s-${over.id}`,
    state: "unread",
    priority: "medium",
    blocking: false,
    created_at: ts,
    updated_at: ts,
    ...over,
  } as InboxItem
}

function renderDetail(it: InboxItem) {
  return render(
    <InboxDetail
      item={it}
      role="OWNER"
      onResolve={vi.fn()}
      onRefresh={vi.fn()}
      onMarkUnread={vi.fn()}
    />,
  )
}

/** The rendered jump control, whatever element it turned out to be. */
function jumpLink(name: RegExp): HTMLElement {
  return screen.getByRole("link", { name })
}

describe("InboxDetail — the jump control", () => {
  afterEach(cleanup)

  it("renders Open run as an anchor pointing at the run's activity page", () => {
    renderDetail(
      item({
        id: "i1",
        kind: "waitpoint",
        title: "Approve this production action?",
        payload: { pipeline_run_id: "run_cmtembsm10025c68f8f1b" },
      }),
    )

    const link = jumpLink(/open run/i)
    expect(link.tagName).toBe("A")
    expect(link).toHaveAttribute("href", "/activity?run=run_cmtembsm10025c68f8f1b")
  })

  it("renders Open <issue> as an anchor pointing at the issue", () => {
    renderDetail(
      item({ id: "i2", kind: "message", title: "ENG-6 ready", payload: { issue_identifier: "ENG-6" } }),
    )

    const link = jumpLink(/open eng-6/i)
    expect(link.tagName).toBe("A")
    expect(link).toHaveAttribute("href", "/issues/ENG-6")
  })

  it("renders Open chat as an anchor pointing at the session", () => {
    renderDetail(
      item({ id: "i3", kind: "message", title: "casey replied", payload: { chat_url: "/chat/casey" } }),
    )

    const link = jumpLink(/open chat/i)
    expect(link.tagName).toBe("A")
    expect(link).toHaveAttribute("href", "/chat/casey")
  })

  // The guard is shared with kind-actions now, but the pane is where an
  // off-origin href would actually be handed to a manager to click.
  it("renders no jump at all for an off-origin chat_url", () => {
    renderDetail(
      item({ id: "i4", kind: "message", title: "casey replied", payload: { chat_url: "//evil.example/x" } }),
    )

    expect(screen.queryByRole("link", { name: /open chat/i })).toBeNull()
  })

  it("falls through to the run when chat_url is unsafe but the item has one", () => {
    renderDetail(
      item({
        id: "i5",
        kind: "waitpoint",
        title: "Approve this production action?",
        payload: { chat_url: "https://evil.example", pipeline_run_id: "r1" },
      }),
    )

    expect(screen.queryByRole("link", { name: /open chat/i })).toBeNull()
    expect(jumpLink(/open run/i)).toHaveAttribute("href", "/activity?run=r1")
  })

  it("renders no jump for an item with nowhere to go", () => {
    renderDetail(item({ id: "i6", kind: "message", title: "Workspace digest" }))

    expect(screen.queryByRole("link", { name: /^open /i })).toBeNull()
  })
})
