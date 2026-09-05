import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

import { InboxActError, type InboxActResult, type InboxItem } from "@/hooks/use-inbox"

// #2398 — a run_needs_human card renders its §12 actions[] and acts on them.
//
// Before this, KindActions had no branch for the kind: the card fell through
// to the generic Dismiss, which PATCHes the inbox row and never reaches the
// session that asked. The three server-side actions (B15, #2389) existed for
// the CLI only. Rendered through KindActions on purpose — the switch is the
// thing that was missing, and a test of the branch component alone would pass
// without it being wired.

const toastError = vi.fn()
const toastSuccess = vi.fn()
const toastInfo = vi.fn()

vi.mock("sonner", () => ({
  toast: {
    error: (...a: unknown[]) => toastError(...a),
    success: (...a: unknown[]) => toastSuccess(...a),
    info: (...a: unknown[]) => toastInfo(...a),
  },
}))

import { KindActions } from "../kind-actions"

const ACTIONS = [
  { id: "answer", label: "Answer", effect: "Delivers your input to the agent's session and resumes the run from its checkpoint", irreversible: false },
  { id: "take_over", label: "Take over", effect: "Opens the issue for you to continue; the agent's session goes idle", irreversible: false },
  { id: "dismiss", label: "Dismiss", effect: "No further work now; the agent's session goes idle", irreversible: false },
]

function card(over: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "ibx_1", workspace_id: "ws", kind: "run_needs_human", source_id: "asg_1",
    title: "Casey needs your input on ENG-7",
    state: "unread", priority: "high", blocking: true,
    attention_class: "input",
    thread_key: "issue:ws:m_1",
    actions: ACTIONS,
    payload: { who_can_act: ["role:MANAGER"], context: { issue: "ENG-7", run: "asg_1" } },
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
    ...over,
  } as InboxItem
}

const receipt = (action: string): InboxActResult => ({
  id: "ibx_1",
  state: "resolved",
  action: action as InboxActResult["action"],
  receipt: {
    action, acted_by: "usr_1", acted_at: "2026-09-05T10:05:00Z", inbox_item_id: "ibx_1",
    session_id: "ses_1", agent_version: 3, source_run_id: "asg_1",
    ...(action === "answer" ? { comment_id: "cmt_1", delivery_id: "mcm_1", run_id: "asg_2", dispatch_state: "dispatched" } : {}),
    event_id: "act_1", seq: 14,
  },
})

const onResolve = vi.fn()
const onRefresh = vi.fn()
const onAct = vi.fn()

function mount(i: InboxItem, opts: { disabled?: boolean; withAct?: boolean } = {}) {
  return render(
    <KindActions
      item={i}
      onResolve={onResolve}
      onRefresh={onRefresh}
      disabled={opts.disabled ?? false}
      onAct={opts.withAct === false ? undefined : onAct}
    />,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
})
afterEach(cleanup)

describe("run_needs_human renders its actions[]", () => {
  it("shows one button per server-declared action, and nothing PATCH-shaped", () => {
    mount(card())
    expect(screen.getByRole("button", { name: "Answer" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Take over" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeInTheDocument()
    // The generic Dismiss the kind used to fall through to resolves the row
    // by PATCH; the one here is a server-side action. Same word, different
    // door — the test that tells them apart is what onResolve is NOT called.
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }))
    expect(onResolve).not.toHaveBeenCalled()
    expect(onAct).toHaveBeenCalledWith("dismiss", undefined)
  })

  it("renders only the actions the card carries — a pre-B15 card offers take_over alone", () => {
    mount(card({ actions: [ACTIONS[1]] }))
    expect(screen.getByRole("button", { name: "Take over" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Answer" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Dismiss" })).not.toBeInTheDocument()
  })

  it("does not render §12 actions for another kind", () => {
    mount(card({ kind: "message", actions: ACTIONS, blocking: false }))
    expect(screen.queryByRole("button", { name: "Answer" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Take over" })).not.toBeInTheDocument()
    expect(onAct).not.toHaveBeenCalled()
  })

  it("disables every action when the caller says so (role gate)", () => {
    mount(card(), { disabled: true })
    for (const name of ["Answer", "Take over", "Dismiss"]) {
      expect(screen.getByRole("button", { name })).toBeDisabled()
    }
  })
})

describe("answer", () => {
  it("opens an input and refuses to send until there is text", async () => {
    mount(card())
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Answer" }))

    const box = screen.getByRole("textbox", { name: /your answer/i })
    const send = screen.getByRole("button", { name: "Send" })
    expect(send).toBeDisabled()
    fireEvent.change(box, { target: { value: "   " } })
    expect(send).toBeDisabled()
    expect(onAct).not.toHaveBeenCalled()

    fireEvent.change(box, { target: { value: "Use the staging bucket." } })
    expect(send).toBeEnabled()
  })

  it("sends the text through onAct and flips to the receipt without a reload", async () => {
    onAct.mockResolvedValueOnce(receipt("answer"))
    mount(card())
    fireEvent.click(screen.getByRole("button", { name: "Answer" }))
    fireEvent.change(screen.getByRole("textbox", { name: /your answer/i }), { target: { value: "Use the staging bucket." } })
    fireEvent.click(screen.getByRole("button", { name: "Send" }))

    await waitFor(() => expect(onAct).toHaveBeenCalledWith("answer", "Use the staging bucket."))
    const rec = await screen.findByTestId("act-receipt")
    expect(rec).toHaveTextContent(/answered/i)
    expect(rec).toHaveTextContent("asg_2")
    expect(rec).toHaveTextContent("#14")
    // The buttons are gone — the card is resolved, not re-actionable.
    expect(screen.queryByRole("button", { name: "Answer" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Send" })).not.toBeInTheDocument()
    expect(toastSuccess).toHaveBeenCalledWith(expect.stringMatching(/asg_2/))
    // Not a refresh-and-advance: the person is still looking at the card.
    expect(onRefresh).not.toHaveBeenCalled()
  })
})

describe("take_over / dismiss", () => {
  it("acts immediately and shows the receipt", async () => {
    onAct.mockResolvedValueOnce(receipt("take_over"))
    mount(card())
    fireEvent.click(screen.getByRole("button", { name: "Take over" }))
    await waitFor(() => expect(onAct).toHaveBeenCalledWith("take_over", undefined))
    const rec = await screen.findByTestId("act-receipt")
    expect(rec).toHaveTextContent(/taken over/i)
    expect(rec).toHaveTextContent("#14")
  })

  it("confirms first when the action is irreversible, and does nothing on cancel", async () => {
    // The DOM shim here ships no window.confirm at all, so this is an
    // assignment rather than a spy.
    const original = window.confirm
    const confirm = vi.fn().mockReturnValue(false)
    window.confirm = confirm
    try {
      mount(card({ actions: [{ ...ACTIONS[2], irreversible: true }] }))
      fireEvent.click(screen.getByRole("button", { name: "Dismiss" }))
      expect(confirm).toHaveBeenCalledTimes(1)
      expect(confirm.mock.calls[0][0]).toMatch(/cannot be undone/i)
      expect(onAct).not.toHaveBeenCalled()

      confirm.mockReturnValue(true)
      onAct.mockResolvedValueOnce(receipt("dismiss"))
      fireEvent.click(screen.getByRole("button", { name: "Dismiss" }))
      await waitFor(() => expect(onAct).toHaveBeenCalledWith("dismiss", undefined))
    } finally {
      window.confirm = original
    }
  })
})

describe("the 409s", () => {
  it("already acted: refreshes the card instead of erroring", async () => {
    onAct.mockRejectedValueOnce(
      new InboxActError("already acted on", { status: 409, code: "already_acted", resolvedAction: "dismiss" }),
    )
    mount(card())
    fireEvent.click(screen.getByRole("button", { name: "Take over" }))

    await waitFor(() => expect(onRefresh).toHaveBeenCalledWith("resolved"))
    expect(toastError).not.toHaveBeenCalled()
    expect(toastInfo).toHaveBeenCalledWith(expect.stringMatching(/already/i))
  })

  it("undeliverable: shows the server's detail and keeps the card open", async () => {
    onAct.mockRejectedValueOnce(
      new InboxActError("the answer was recorded as a comment but could not be delivered to the agent's session", {
        status: 409, code: "undeliverable", detail: "agent casey is held", dispatchState: "held",
      }),
    )
    mount(card())
    fireEvent.click(screen.getByRole("button", { name: "Answer" }))
    fireEvent.change(screen.getByRole("textbox", { name: /your answer/i }), { target: { value: "hello" } })
    fireEvent.click(screen.getByRole("button", { name: "Send" }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/agent casey is held/)))
    expect(onRefresh).not.toHaveBeenCalled()
    expect(screen.queryByTestId("act-receipt")).not.toBeInTheDocument()
    // Still open for a retry once the cause is fixed.
    expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument()
  })
})

describe("a resolved card", () => {
  it("renders the receipt the server merged into payload, not buttons", () => {
    mount(card({
      state: "resolved", resolved_action: "answer", resolved_at: "2026-09-05T10:05:00Z",
      payload: { who_can_act: ["role:MANAGER"], receipt: receipt("answer").receipt },
    }), { disabled: true })
    const rec = screen.getByTestId("act-receipt")
    expect(rec).toHaveTextContent("asg_2")
    expect(rec).toHaveTextContent("#14")
    expect(screen.queryByRole("button", { name: "Answer" })).not.toBeInTheDocument()
  })
})
