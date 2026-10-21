import { describe, it, expect, vi } from "vitest"
import React from "react"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"
import type { ChatTurn, TurnPart } from "@/hooks/use-chat"

// AssistantTurn leans on heavy ai-elements components (motion/react inside
// Reasoning, portal logic in Tool/CodeBlock). Replace them with thin probes
// so the tests focus on the dispatch logic this file actually owns, rather
// than the primitives those libraries already cover.
vi.mock("@/components/ai-elements/message", () => ({
  Message: ({ children, from }: { children: React.ReactNode; from: string }) => (
    <div data-testid="message" data-from={from}>{children}</div>
  ),
  MessageContent: ({ children, className }: { children: React.ReactNode; className?: string }) => (
    <div data-testid="message-content" className={className}>{children}</div>
  ),
  MessageResponse: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="message-response">{children}</div>
  ),
  MessageActions: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="message-actions">{children}</div>
  ),
  MessageAction: ({ children, tooltip, onClick }: {
    children: React.ReactNode
    tooltip: string
    onClick?: () => void
  }) => (
    <button data-testid={`action-${tooltip.toLowerCase().replace(/ /g, "-")}`} onClick={onClick}>
      {children}
    </button>
  ),
}))

vi.mock("@/components/ai-elements/reasoning", () => ({
  Reasoning: ({ children, isStreaming }: { children: React.ReactNode; isStreaming?: boolean }) => (
    <div data-testid="reasoning" data-streaming={String(!!isStreaming)}>{children}</div>
  ),
  ReasoningTrigger: () => <span data-testid="reasoning-trigger" />,
  ReasoningContent: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="reasoning-content">{children}</div>
  ),
}))

// The smooth-reveal animation is covered by hooks/__tests__/use-smooth-text;
// here it would make streaming parts render progressively across frames,
// which is noise for dispatch assertions. Identity-mock it.
vi.mock("@/hooks/use-smooth-text", () => ({
  useSmoothText: (text: string) => text,
}))

vi.mock("@/components/ai-elements/tool", () => ({
  Tool: ({ children }: { children: React.ReactNode }) => <div data-testid="tool">{children}</div>,
  ToolContent: ({ children }: { children: React.ReactNode }) => <div data-testid="tool-content">{children}</div>,
  ToolHeader: ({ title, state, statusLabel }: { title?: string; state?: string; statusLabel?: string }) => <div data-testid="tool-header" data-state={state} data-label={statusLabel ?? ""}>{title}</div>,
}))

vi.mock("@/components/ai-elements/code-block", () => ({
  CodeBlock: ({ code }: { code: string }) => <pre data-testid="code-block">{code}</pre>,
}))

vi.mock("@/components/features/chat/status-indicator", () => ({
  StatusIndicator: ({ content }: { content: string }) => (
    <div data-testid="status-indicator">{content}</div>
  ),
}))

// TurnFeedbackActions calls useSession() to namespace the feedback
// store by user (privacy fix for the per-user persisted state). The
// real useAuth/useSession require an AuthProvider; tests don't have
// one and shouldn't — the feedback wiring is an implementation detail
// for these dispatch-logic tests. Stub useSession to a fixed identity
// so the component renders without throwing.
vi.mock("@/hooks/use-auth", () => ({
  useSession: () => ({
    data: { user: { id: "test-user", name: "Test", email: "t@e.com" }, expires: "" },
    status: "authenticated" as const,
  }),
}))

vi.mock("../reactions/turn-reactions", () => ({ TurnReactions: () => null }))

vi.mock("../artifact/inline-run-images", () => ({ InlineRunImages: ({ agentId, workspaceId, runId }: { agentId: string; workspaceId: string; runId: string }) => <div data-testid="run-images">{`${agentId}:${workspaceId}:${runId}`}</div> }))

import { AssistantTurn } from "../assistant-turn"

let n = 0
const part = (type: TurnPart["type"], metadata: Record<string, unknown> = {}, content = ""): TurnPart => ({
  id: `p${++n}`, type, content, metadata, timestamp: new Date("2026-10-19T10:00:00Z"),
})
const turn = (parts: TurnPart[], isStreaming = false): ChatTurn => ({
  id: "t", role: "assistant", parts, isStreaming, timestamp: new Date("2026-10-19T10:00:00Z"),
})
const call = (id: string, extra: Record<string, unknown> = {}) =>
  part("tool_call", { tool_id: id, tool_name: "Bash", input: { command: "ls" }, ...extra }, "Bash")
const result = (id: string, extra: Record<string, unknown> = {}) => part("tool_result", { tool_id: id, ...extra }, "")
const renderTurn = (t: ChatTurn) =>
  render(<AssistantTurn turn={t} onCopy={async () => true} onFileClick={() => {}} />)

describe("CH-04 grouped activity", () => {
  beforeEach(() => cleanup())

  it("a failed step never produces a green group summary", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b"), result("b", { is_error: true })]))
    const group = container.querySelector("details[data-activity-tone]")!
    expect(group.getAttribute("data-activity-tone")).toBe("attention")
    expect(group.textContent).toContain("Activity incomplete")
    expect(group.textContent).toContain("1 failed")
    expect(group.hasAttribute("open")).toBe(true)
    const states = screen.getAllByTestId("tool-header").map((h) => h.getAttribute("data-state"))
    expect(states).toEqual(["output-available", "output-error"])
  })

  it("all-success group reads as worked", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b"), result("b")]))
    const group = container.querySelector("details[data-activity-tone]")!
    expect(group.getAttribute("data-activity-tone")).toBe("ok")
    expect(group.textContent).toContain("Worked")
  })

  it("shows running while the turn streams and unknown once it ended without a result", () => {
    const parts = [call("a"), result("a"), call("b")]
    const live = renderTurn(turn(parts, true))
    expect(live.container.querySelector("details")!.getAttribute("data-activity-tone")).toBe("running")
    cleanup()
    const done = renderTurn(turn(parts, false))
    expect(done.container.querySelector("details")!.getAttribute("data-activity-tone")).toBe("neutral")
    expect(screen.getAllByTestId("tool-header").some((h) => h.getAttribute("data-label") === "Outcome unknown")).toBe(true)
  })

  it("labels denied and interrupted steps", () => {
    renderTurn(turn([
      call("a"), result("a", { denied: true, is_error: true }),
      ({ ...part("activity", { tool_name: "Bash" }, "Bash"), tool_call_id: "b", activity: { kind: "shell", name: "Bash", state: "interrupted" } }) as TurnPart,
    ]))
    expect(screen.getByText("Denied")).toBeTruthy()
    expect(screen.getByText("Interrupted")).toBeTruthy()
  })

  it("nests subagent steps under their parent by correlation", () => {
    renderTurn(turn([
      call("task"), call("c1", { parent_tool_call_id: "task" }), result("c1"), result("task"),
    ]))
    expect(screen.getAllByTestId("nested-activity")).toHaveLength(1)
  })

  it("keeps an approval request outside the collapsed group", () => {
    const { container } = renderTurn(turn([
      call("a"), result("a"), call("b"), result("b"),
      ({ ...part("activity", { tool_name: "Bash" }, "Bash"), tool_call_id: "c", activity: { kind: "shell", name: "Bash", state: "awaiting_approval" } }) as TurnPart,
    ], true))
    const group = container.querySelector("details[data-activity-tone]")!
    const approval = screen.getAllByTestId("tool-header").find((h) => h.getAttribute("data-state") === "approval-requested")!
    expect(approval).toBeTruthy()
    expect(group.contains(approval)).toBe(false)
  })
})

describe("review blockers: parallel calls, wire keys, history", () => {
  beforeEach(() => cleanup())
  const rUse = (id: string, extra: Record<string, unknown> = {}) => part("tool_result", { tool_use_id: id, ...extra }, "")

  it("pairs parallel calls by tool_use_id and puts the failure on the right call", () => {
    const { container } = renderTurn(turn([
      call("A"), call("B"), rUse("A", { is_error: true }), rUse("B"),
    ]))
    const headers = screen.getAllByTestId("tool-header")
    expect(headers).toHaveLength(2)
    expect(headers.map((h) => h.getAttribute("data-state"))).toEqual(["output-error", "output-available"])
    const group = container.querySelector("details[data-activity-tone]")!
    expect(group.textContent).toContain("1 failed")
    expect(group.textContent).not.toContain("unknown")
  })

  it("pairs parallel calls by tool_id too, and an all-success parallel turn reads Worked", () => {
    const { container } = renderTurn(turn([call("A"), call("B"), result("A"), result("B")]))
    const group = container.querySelector("details[data-activity-tone]")!
    expect(group.getAttribute("data-activity-tone")).toBe("ok")
    expect(group.textContent).toContain("2 steps")
  })

  it("an orphan result classifies itself by its own is_error", () => {
    renderTurn(turn([rUse("zzz", { is_error: true })]))
    expect(screen.getAllByTestId("tool-header")[0].getAttribute("data-state")).toBe("output-error")
  })

  it("nests under parent_tool_use_id, the key the server emits", () => {
    renderTurn(turn([
      call("task"), call("c1", { parent_tool_use_id: "task" }), result("c1"), result("task"),
    ]))
    expect(screen.getAllByTestId("nested-activity")).toHaveLength(1)
  })

  it("a finished turn with no recorded result is neutral, not an alarm", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b")], false))
    const group = container.querySelector("details[data-activity-tone]")!
    expect(group.getAttribute("data-activity-tone")).toBe("neutral")
    expect(group.hasAttribute("open")).toBe(false)
    expect(group.textContent).not.toContain("incomplete")
  })
})

describe("typed events from the stream consumer", () => {
  beforeEach(() => cleanup())
  const typed = (id: string, state: string, parent?: string) => ({
    ...part("activity", { tool_name: "Bash" }, "Bash"),
    tool_call_id: id,
    ...(parent ? { parent_tool_call_id: parent } : {}),
    activity: { kind: "shell", name: "Bash", state },
  }) as TurnPart

  it("a failed typed step is never green and nests children by parent_tool_call_id", () => {
    const { container } = renderTurn(turn([typed("t", "succeeded"), typed("c", "failed", "t")]))
    expect(container.querySelector("details[data-activity-tone]")!.getAttribute("data-activity-tone")).toBe("attention")
    expect(screen.getAllByTestId("nested-activity")).toHaveLength(1)
    expect(screen.getAllByTestId("tool-header").map((h) => h.getAttribute("data-state"))).toEqual(["output-available", "output-error"])
  })

  it("the failed step shows the exact visible text Failed with no click, and the group is open and not Worked", () => {
    // Same shape as the persisted-activity browser fixture.
    const { container } = renderTurn(turn([typed("a", "succeeded"), typed("b", "failed")]))
    expect(container.querySelector("details[data-activity-tone]")!.hasAttribute("open")).toBe(true)
    expect(screen.getByText("Activity incomplete")).toBeTruthy()
    expect(screen.getAllByText("Failed")).toHaveLength(1)
    expect(screen.queryByText("Worked")).toBeNull()
  })

  it("a typed unknown step reads Outcome unknown, not success", () => {
    renderTurn(turn([typed("a", "unknown")]))
    // Once: in the header badge only, not repeated as a second label.
    expect(screen.getByTestId("tool-header").getAttribute("data-label")).toBe("Outcome unknown")
    expect(screen.queryByText("Outcome unknown")).toBeNull()
  })
  it("an input_streaming step reads as in progress, never Worked", () => {
    const { container } = renderTurn(turn([typed("a", "succeeded"), typed("b", "input_streaming")], true))
    expect(container.querySelector("details[data-activity-tone]")!.getAttribute("data-activity-tone")).toBe("running")
    expect(screen.queryByText("Worked")).toBeNull()
  })

  it("an unrecognised state token degrades to Outcome unknown, never Worked", () => {
    renderTurn(turn([typed("a", "succeeded"), typed("b", "paused")]))
    expect(screen.queryByText("Worked")).toBeNull()
    expect(screen.getAllByTestId("tool-header").map((h) => h.getAttribute("data-label"))).toContain("Outcome unknown")
  })

  it("a typed running step on a finished turn reads Outcome unknown instead of spinning", () => {
    const { container } = renderTurn(turn([typed("a", "succeeded"), typed("b", "running")], false))
    expect(container.querySelector("details[data-activity-tone]")!.getAttribute("data-activity-tone")).toBe("neutral")
    expect(screen.getAllByTestId("tool-header").map((h) => h.getAttribute("data-label"))).toContain("Outcome unknown")
  })

  it("a group the reader opened stays open when its tone changes", () => {
    // Same part ids across renders, as in a live turn (the key is the first id).
    const a = typed("a", "succeeded"), b = typed("b", "running")
    const withB = (state: string) => ({ ...b, activity: { ...b.activity!, state } }) as TurnPart
    const { container, rerender } = renderTurn(turn([a, b], true))
    const details = () => container.querySelector("details[data-activity-tone]") as HTMLDetailsElement
    expect(details().open).toBe(false)
    details().open = true
    fireEvent(details(), new Event("toggle"))
    rerender(<AssistantTurn turn={turn([a, withB("failed")], true)} onCopy={async () => true} onFileClick={() => {}} />)
    expect(details().getAttribute("data-activity-tone")).toBe("attention")
    expect(details().open).toBe(true)
    rerender(<AssistantTurn turn={turn([a, withB("succeeded")], false)} onCopy={async () => true} onFileClick={() => {}} />)
    expect(details().getAttribute("data-activity-tone")).toBe("ok")
    expect(details().open).toBe(true)
  })

  it("every thinking state renders without claiming a finished summary", () => {
    for (const state of ["running", "completed", "cut_off", "unavailable", "nonsense"]) {
      cleanup()
      const p = { ...part("thinking", {}, "private"), thinking: { visibility: "summary", state, summary: "Checked sources" } } as TurnPart
      const { container } = renderTurn(turn([p]))
      expect(container.textContent).not.toContain("private")
      if (state === "cut_off") expect(container.textContent).toContain("interrupted")
    }
  })
})
