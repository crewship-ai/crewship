/**
 * §9: "if the payload has passed retention, replay is unavailable and the UI
 * must say why rather than offering a dead button."
 *
 * The failure this pins is a specific one, and it is the reason the check
 * cannot live in the click handler: the server DOES answer 409 with the same
 * sentence, but a 409 arrives after the click. A button that looks live,
 * accepts a press and then explains that it never could have worked is the
 * dead button §9 forbids — it just takes a round trip to find out.
 */
import { describe, it, expect, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

import { ReplayButton, ReplayUnavailableReason, WorkReplayDialog } from "../work-replay-dialog"
import type { ReplayAvailability, WorkItemDetail } from "@/hooks/use-work-items"

afterEach(cleanup)

const AVAILABLE: ReplayAvailability = { state: "available", reason: "" }
const DROPPED: ReplayAvailability = {
  state: "unavailable",
  reason:
    "The raw payload for this delivery has passed its retention window and was dropped, so a replay cannot reproduce the original input. It expired at 2026-09-01T09:00:00Z. Ask the sender to deliver it again.",
}

function item(over: Partial<WorkItemDetail> = {}): WorkItemDetail {
  return {
    id: "cwork0000000000000001",
    workspace_id: "ws-1",
    source: "webhook",
    source_ref: "cdlv1",
    domain_kind: "pipeline",
    domain_id: "p-1",
    agent_id: "agent-jamie",
    crew_id: "crew-1",
    session_id: "",
    class: "background",
    authorized_by_user_id: "user-1",
    input_sha256: "abcdef0123456789".repeat(4),
    target_revision: "rev-7",
    state: "failed",
    state_reason: "",
    generation: 2,
    priority: 0,
    eligible_at: "2026-09-10T09:00:00Z",
    deadline_at: null,
    replay_of: null,
    replay_reason: "",
    created_at: "2026-09-10T09:00:00Z",
    updated_at: "2026-09-10T09:05:00Z",
    terminal_at: "2026-09-10T09:05:00Z",
    attempts: [],
    events: [],
    ...over,
  }
}

describe("the replay button", () => {
  it("is disabled, and the reason is on the page — not only in a tooltip", () => {
    render(
      <>
        <ReplayButton availability={DROPPED} onClick={() => {}} />
        <ReplayUnavailableReason availability={DROPPED} />
      </>,
    )
    expect(screen.getByTestId("replay-button").hasAttribute("disabled")).toBe(true)
    expect(screen.getByTestId("replay-unavailable-reason").textContent).toContain("passed its retention window")
  })

  it("carries no reason when replay is genuinely available", () => {
    render(
      <>
        <ReplayButton availability={AVAILABLE} onClick={() => {}} />
        <ReplayUnavailableReason availability={AVAILABLE} />
      </>,
    )
    expect(screen.getByTestId("replay-button").hasAttribute("disabled")).toBe(false)
    expect(screen.queryByTestId("replay-unavailable-reason")).toBeNull()
  })
})

describe("the replay dialog", () => {
  it("cannot be confirmed when the payload is gone, and states why", () => {
    let confirmed = false
    render(
      <WorkReplayDialog
        open
        onOpenChange={() => {}}
        item={item()}
        availability={DROPPED}
        pending={false}
        onConfirm={() => { confirmed = true }}
      />,
    )
    const confirm = screen.getByTestId("replay-confirm")
    expect(confirm.hasAttribute("disabled")).toBe(true)
    expect(screen.getByTestId("replay-blocked-reason").textContent).toContain("passed its retention window")
    fireEvent.click(confirm)
    expect(confirmed).toBe(false)
  })

  it("shows the original input's fingerprint, never the input", () => {
    render(
      <WorkReplayDialog open onOpenChange={() => {}} item={item()} availability={AVAILABLE} pending={false} onConfirm={() => {}} />,
    )
    expect(screen.getByTestId("replay-input-fingerprint").textContent).toContain("sha256:")
  })

  it("says it creates NEW work rather than re-running the original", () => {
    const { baseElement } = render(
      <WorkReplayDialog open onOpenChange={() => {}} item={item()} availability={AVAILABLE} pending={false} onConfirm={() => {}} />,
    )
    expect(baseElement.textContent).toContain("This creates NEW work")
    expect(baseElement.textContent).toContain("replay_of")
    expect(screen.getByTestId("replay-confirm").textContent).toContain("Create new work")
  })

  it("inherits the original target revision rather than resolving 'latest' for you", () => {
    render(
      <WorkReplayDialog open onOpenChange={() => {}} item={item({ target_revision: "rev-7" })} availability={AVAILABLE} pending={false} onConfirm={() => {}} />,
    )
    const field = screen.getByDisplayValue("rev-7") as HTMLInputElement
    expect(field).toBeTruthy()
  })

  it("passes the reason and the revision through on confirm", () => {
    const calls: Array<{ reason: string; targetRevision: string }> = []
    render(
      <WorkReplayDialog open onOpenChange={() => {}} item={item()} availability={AVAILABLE} pending={false} onConfirm={(v) => calls.push(v)} />,
    )
    fireEvent.change(screen.getByDisplayValue("rev-7"), { target: { value: "rev-9" } })
    fireEvent.change(screen.getByPlaceholderText("Why is this being run again?"), { target: { value: "sender re-sent" } })
    fireEvent.click(screen.getByTestId("replay-confirm"))
    expect(calls).toEqual([{ reason: "sender re-sent", targetRevision: "rev-9" }])
  })
})
