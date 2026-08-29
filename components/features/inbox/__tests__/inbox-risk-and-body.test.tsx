import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"
import { riskLevelOf } from "../inbox-derive"

// #2160 — the two things the row and the pane had no way to say:
// what this approval would do, and whether it can be taken back.
//
// The title half needs no test here: the row already renders item.title, so
// a distinct title flows through the moment the server writes one. That
// contract is pinned server-side in internal/pipeline/waitpoints_title_test.go.
// What is new on this side is the risk badge and the body clamp.

vi.mock("../waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))
vi.mock("../kind-actions", () => ({ KindActions: () => null }))

import { InboxDetail } from "../inbox-detail"

function item(over: Partial<InboxItem> & Pick<InboxItem, "id" | "kind" | "title">): InboxItem {
  const ts = new Date("2026-08-29T16:52:00Z").toISOString()
  return {
    workspace_id: "ws",
    source_id: `s-${over.id}`,
    state: "unread",
    priority: "high",
    blocking: true,
    created_at: ts,
    updated_at: ts,
    ...over,
  } as InboxItem
}

function renderDetail(it: InboxItem) {
  return render(
    <InboxDetail item={it} role="OWNER" onResolve={vi.fn()} onRefresh={vi.fn()} onMarkUnread={vi.fn()} />,
  )
}

describe("riskLevelOf", () => {
  it("marks only an explicitly destructive item", () => {
    expect(riskLevelOf(item({ id: "a", kind: "waitpoint", title: "t", payload: { risk_level: "destructive" } })))
      .toBe("destructive")
    // "normal" returns null, not "normal": the row has room to mark the
    // exception, not to label the rule.
    expect(riskLevelOf(item({ id: "b", kind: "waitpoint", title: "t", payload: { risk_level: "normal" } })))
      .toBeNull()
    expect(riskLevelOf(item({ id: "c", kind: "waitpoint", title: "t", payload: {} }))).toBeNull()
    expect(riskLevelOf(item({ id: "d", kind: "waitpoint", title: "t" }))).toBeNull()
  })

  it("does not guess from an unrecognised value", () => {
    // The server validates the vocabulary at save time, so anything else
    // here is a value this build does not know. Treating it as risky would
    // be a guess; treating it as safe is what the server already decided.
    expect(riskLevelOf(item({ id: "e", kind: "waitpoint", title: "t", payload: { risk_level: "DESTRUCTIVE" } })))
      .toBeNull()
    expect(riskLevelOf(item({ id: "f", kind: "waitpoint", title: "t", payload: { risk_level: "catastrophic" } })))
      .toBeNull()
  })

  it("ignores a non-string payload value", () => {
    expect(riskLevelOf(item({ id: "g", kind: "waitpoint", title: "t", payload: { risk_level: 1 } }))).toBeNull()
  })
})

describe("InboxDetail — the destructive badge", () => {
  afterEach(cleanup)

  it("warns beside the decision heading, not in the context dump", () => {
    renderDetail(
      item({
        id: "i1",
        kind: "waitpoint",
        title: "Delete staging bucket crewship-stage-artifacts",
        payload: { risk_level: "destructive", pipeline_run_id: "r1" },
      }),
    )

    const card = screen.getByTestId("decision-card")
    expect(within(card).getByText(/destructive/i)).toBeTruthy()
    // The raw key must not also appear as a Context row — it is a warning,
    // and rendering it twice, once as debug output, weakens it.
    expect(screen.queryByText("Risk level")).toBeNull()
  })

  it("says nothing for an ordinary approval", () => {
    renderDetail(
      item({
        id: "i2",
        kind: "waitpoint",
        title: "Scale payments-api to 12 replicas",
        payload: { risk_level: "normal", pipeline_run_id: "r1" },
      }),
    )
    expect(screen.queryByText(/destructive/i)).toBeNull()
  })
})

describe("InboxDetail — the message body", () => {
  afterEach(cleanup)

  const short = "Approve this?"
  // A model-drafted change plan, which is what a waitpoint body actually is.
  const long = `Approve this production action?\n\n${"Verify pre-flight conditions and monitor SLOs for five minutes. ".repeat(20)}`

  it("renders a short body whole, with no control that does nothing", () => {
    renderDetail(item({ id: "b1", kind: "message", title: "t", body_md: short }))
    expect(screen.getByText(short)).toBeTruthy()
    expect(screen.queryByRole("button", { name: /show the whole message/i })).toBeNull()
  })

  it("clamps a long body and offers the rest", () => {
    renderDetail(item({ id: "b2", kind: "message", title: "t", body_md: long }))
    const toggle = screen.getByRole("button", { name: /show the whole message/i })
    expect(toggle).toBeTruthy()

    fireEvent.click(toggle)
    expect(screen.getByRole("button", { name: /show less/i })).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: /show less/i }))
    expect(screen.getByRole("button", { name: /show the whole message/i })).toBeTruthy()
  })

  it("keeps the whole body in the DOM while clamped", () => {
    // The clamp is presentational. Removing the text would break find-in-page
    // and copy, and would make the collapsed state a different document than
    // the expanded one.
    renderDetail(item({ id: "b3", kind: "message", title: "t", body_md: long }))
    expect(screen.getByText(/Verify pre-flight conditions/)).toBeTruthy()
  })
})
