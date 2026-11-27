import { describe, it, expect, vi } from "vitest"
import React from "react"
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react"
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
const renderTurn = (t: ChatTurn, onCopy: (s: string) => Promise<boolean> = async () => true) =>
  render(<AssistantTurn turn={t} onCopy={onCopy} onFileClick={() => {}} />)

// The turn's one activity line, and the steps behind it.
const activity = (c: HTMLElement) => c.querySelector("[data-testid='turn-activity']") as HTMLElement
const activityButton = (c: HTMLElement) => activity(c).querySelector("button[data-activity-tone]") as HTMLButtonElement
const tone = (c: HTMLElement) => activityButton(c).getAttribute("data-activity-tone")
const openSteps = (c: HTMLElement) => { fireEvent.click(activityButton(c)); return screen.getByRole("dialog") }
const stepStates = () => screen.getAllByTestId("activity-step").map((s) => s.getAttribute("data-state"))


describe("one activity line per turn", () => {
  beforeEach(() => cleanup())

  it("a failed step never produces a success line, and no step is drawn in the transcript", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b"), result("b", { is_error: true })]))
    expect(tone(container)).toBe("attention")
    const line = activityButton(container)
    // The turn ran to its end but is not summarised as success; the failure is
    // named in the line, with a labelled marker, not only a colour.
    expect(line.textContent).toContain("Finished")
    expect(line.textContent).toContain("2 steps")
    expect(line.textContent).toContain("1 failed")
    expect(line.textContent).not.toContain("Done")
    expect(line.textContent).not.toContain("Worked")
    expect(line.querySelector("[aria-label='1 failed']")).not.toBeNull()
    expect(screen.queryAllByTestId("tool-header")).toHaveLength(0)
    expect(container.textContent).not.toContain("ls")
    openSteps(container)
    expect(stepStates()).toEqual(["succeeded", "failed"])
  })

  it("a failed step the same tool later did successfully reads as done, retried, in words", () => {
    const { container } = renderTurn(turn([call("a"), result("a", { is_error: true }), call("b"), result("b")]))
    expect(tone(container)).toBe("ok")
    const line = activityButton(container).textContent!
    expect(line).toContain("Done")
    expect(line).toContain("1 retried")
    expect(line).not.toContain("Bash")
  })

  it("a failed step that was retried keeps Failed in the steps and says the retry succeeded", () => {
    const { container } = renderTurn(turn([call("a"), result("a", { is_error: true }), call("b"), result("b")]))
    expect(activityButton(container).textContent).toBe("Done · 2 steps · 1 retried")
    const dialog = openSteps(container)
    expect(stepStates()).toEqual(["failed", "succeeded"])
    const [failed, retry] = within(dialog).getAllByTestId("activity-step")
    expect(within(failed).getByTestId("step-state").textContent).toBe("Failed · retried, then succeeded")
    expect(within(retry).queryByTestId("step-state")).toBeNull()
  })

  it("a failed step nothing retried says only Failed", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b"), result("b", { is_error: true })]))
    const dialog = openSteps(container)
    expect(within(within(dialog).getAllByTestId("activity-step")[1]).getByTestId("step-state").textContent).toBe("Failed")
  })

  it("while the agent works the line is one sentence of what it does now, with the time", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b")], true))
    expect(tone(container)).toBe("running")
    const line = activityButton(container).textContent!
    expect(line).toContain("Running a command…")
    expect(line).toMatch(/\d+ (s|min)/)
    expect(screen.queryAllByTestId("tool-header")).toHaveLength(0)
  })

  it("while the agent works the line moves nothing itself (the face in the gutter is the one indicator) and reads in full contrast", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b")], true))
    const button = activityButton(container)
    expect(button.querySelector("[data-testid='working-mark']")).toBeNull()
    expect(button.querySelector("[class*='animate-']")).toBeNull()
    const sentence = button.querySelector("[data-testid='activity-sentence']") as HTMLElement
    expect(sentence.textContent).toBe("Running a command…")
    expect(sentence.className).toContain("text-foreground")
    expect(button.querySelector("[data-testid='activity-elapsed']")!.textContent).toMatch(/^\d+ (s|min)/)
  })

  it("names a Sheet change by its range while it runs", () => {
    const apply = part("tool_call", { tool_id: "s", tool_name: "mcp__crewship__craft_sheet_apply", input: { commands: [{ command: "range.setValues", params: { sheet: "L", range: "B8:B577", values: [] } }] } }, "mcp__crewship__craft_sheet_apply")
    const { container } = renderTurn(turn([call("a"), result("a"), apply], true))
    expect(activityButton(container).textContent).toContain("Changing 'L'!B8:B577 (570 cells)…")
  })

  it("a real agent's Sheet steps read as words in the line and in the steps, never as an MCP name or [object Object]", () => {
    const read = part("tool_call", { tool_id: "r", tool_name: "mcp__crewship-routines__craft_sheet_read", input: { query: { command: "sheet.read", params: { sheet: "L" } } } }, "mcp__crewship-routines__craft_sheet_read")
    const acquire = part("tool_call", { tool_id: "q", tool_name: "mcp__crewship-routines__craft_sheet_acquire", input: {} }, "mcp__crewship-routines__craft_sheet_acquire")
    const running = renderTurn(turn([read, result("r"), acquire], true))
    expect(activityButton(running.container).textContent).toContain("Taking over editing of the Sheet…")
    cleanup()
    const apply = part("tool_call", { tool_id: "a", tool_name: "mcp__crewship-routines__craft_sheet_apply", input: { generation: "4", base_version: "12", request_id: "n-1", commands: [{ command: "range.setValues", params: { range: "B8:B577", values: [["LN-0001"]], sheet: "L" } }] } }, "mcp__crewship-routines__craft_sheet_apply")
    const release = part("tool_call", { tool_id: "z", tool_name: "mcp__crewship-routines__craft_sheet_release", input: { generation: "4" } }, "mcp__crewship-routines__craft_sheet_release")
    const { container } = renderTurn(turn([read, result("r"), acquire, result("q"), apply, result("a"), release, result("z")]))
    const dialog = openSteps(container)
    expect(within(dialog).getAllByTestId("activity-step").map(s => s.querySelector("span.text-foreground")!.textContent)).toEqual([
      "Read sheet L", "Took over editing of the Sheet", "Changed 'L'!B8:B577 (570 cells)", "Handed the Sheet back",
    ])
    expect(document.body.textContent).not.toMatch(/\[object Object\]/)
    for (const row of within(dialog).getAllByTestId("activity-step")) expect(row.firstElementChild!.textContent).not.toMatch(/mcp__|crewship-routines|craft_sheet/)
  })

  it("a Sheet step waiting for approval keeps a human name in the transcript", () => {
    const pending = { ...part("activity", { tool_name: "mcp__crewship-routines__craft_sheet_read" }, "mcp__crewship-routines__craft_sheet_read"), tool_call_id: "w", activity: { kind: "tool", name: "mcp__crewship-routines__craft_sheet_read", state: "awaiting_approval", parameters: { query: JSON.stringify({ command: "sheet.read", params: { sheet: "L" } }) } } } as TurnPart
    renderTurn(turn([call("a"), result("a"), pending], true))
    const header = screen.getAllByTestId("tool-header").find((h) => h.getAttribute("data-state") === "approval-requested")!
    expect(header.textContent).toBe("Reading sheet L")
  })

  it("between tools the line says the agent is writing, and the agent's text stays the answer", () => {
    const text = { ...part("text", {}, "Here is the answer."), isStreaming: true }
    const { container } = renderTurn(turn([call("a"), result("a"), text], true))
    expect(activityButton(container).textContent).toContain("Writing the answer…")
    expect(screen.getByText("Here is the answer.")).toBeTruthy()
  })

  it("two runs of tools around text are one line, and the text keeps its place as prose", () => {
    const { container } = renderTurn(turn([
      call("a"), result("a"), part("text", {}, "Let me check the second sheet."),
      call("b"), result("b"), call("c"), result("c"), part("text", {}, "All loans are numbered."),
    ]))
    expect(container.querySelectorAll("[data-testid='turn-activity']")).toHaveLength(1)
    expect(activityButton(container).textContent).toContain("3 steps")
    expect(screen.getByText("Let me check the second sheet.")).toBeTruthy()
    expect(screen.getByText("All loans are numbered.")).toBeTruthy()
    expect(screen.queryAllByTestId("tool-header")).toHaveLength(0)
  })

  it("a finished line names how long the run took and how many steps it had", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b"), result("b"), part("result", { duration_ms: 41_000 })]))
    expect(activityButton(container).textContent).toBe("Done · 41 s · 2 steps")
  })

  it("a run that failed reads Failed, never Done", () => {
    const t = { ...turn([call("a"), result("a")]), turnState: "failed" } as ChatTurn
    const { container } = renderTurn(t)
    expect(tone(container)).toBe("attention")
    expect(activityButton(container).textContent).toContain("Failed")
    expect(activityButton(container).textContent).not.toContain("Done")
  })

  it("a long run renders its steps only once the detail is opened", () => {
    const parts: TurnPart[] = []
    for (let i = 0; i < 200; i++) parts.push(call(`s${i}`), result(`s${i}`))
    const { container } = renderTurn(turn(parts))
    expect(activityButton(container).textContent).toContain("200 steps")
    expect(screen.queryAllByTestId("activity-step")).toHaveLength(0)
    openSteps(container)
    expect(screen.getAllByTestId("activity-step")).toHaveLength(200)
  })

  it("all-success run reads Done", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b"), result("b")]))
    expect(tone(container)).toBe("ok")
    expect(activityButton(container).textContent).toContain("Done")
  })

  it("shows running while the turn streams and unknown once it ended without a result", () => {
    const parts = [call("a"), result("a"), call("b")]
    const live = renderTurn(turn(parts, true))
    expect(tone(live.container)).toBe("running")
    cleanup()
    const done = renderTurn(turn(parts, false))
    expect(tone(done.container)).toBe("neutral")
    expect(activityButton(done.container).textContent).not.toContain("Done")
    openSteps(done.container)
    expect(stepStates()).toEqual(["succeeded", "unknown"])
    expect(within(screen.getByRole("dialog")).getByText("Outcome unknown")).toBeTruthy()
  })

  it("labels denied and interrupted steps in the detail", () => {
    const { container } = renderTurn(turn([
      call("a"), result("a", { denied: true, is_error: true }),
      ({ ...part("activity", { tool_name: "Bash" }, "Bash"), tool_call_id: "b", activity: { kind: "shell", name: "Bash", state: "interrupted" } }) as TurnPart,
    ]))
    expect(activityButton(container).textContent).not.toContain("Done")
    const dialog = openSteps(container)
    expect(within(dialog).getByText("Denied")).toBeTruthy()
    expect(within(dialog).getByText("Interrupted")).toBeTruthy()
  })

  it("nests subagent steps under their parent by correlation, in the detail", () => {
    const { container } = renderTurn(turn([
      call("task"), call("c1", { parent_tool_call_id: "task" }), result("c1"), result("task"),
    ]))
    expect(activityButton(container).textContent).toContain("2 steps")
    openSteps(container)
    expect(screen.getAllByTestId("nested-activity")).toHaveLength(1)
  })

  it("keeps an approval request in the transcript, outside the line, and actionable", () => {
    const { container } = renderTurn(turn([
      call("a"), result("a"), call("b"), result("b"),
      ({ ...part("activity", { tool_name: "Bash" }, "Bash"), tool_call_id: "c", activity: { kind: "shell", name: "Bash", state: "awaiting_approval" } }) as TurnPart,
    ], true))
    const approval = screen.getAllByTestId("tool-header").find((h) => h.getAttribute("data-state") === "approval-requested")!
    expect(approval).toBeTruthy()
    expect(activity(container).contains(approval)).toBe(false)
  })

  it.each([
    ["as a tool call", part("tool_call", { tool_name: "AskUserQuestion", tool_id: "q" }, "AskUserQuestion")],
    ["as a typed activity", { ...part("activity", { tool_name: "AskUserQuestion" }, "AskUserQuestion"), tool_call_id: "q", activity: { kind: "tool", name: "AskUserQuestion", state: "running" } } as TurnPart],
  ] as const)("a question %s after a run of tools stays in the transcript", (_form, question) => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b"), result("b"), question], true))
    expect(activity(container)).toBeTruthy()
    const header = screen.getAllByTestId("tool-header").find((h) => h.textContent === "AskUserQuestion")
    expect(header).toBeTruthy()
    expect(activity(container).contains(header!)).toBe(false)
  })
})

describe("the steps popup", () => {
  beforeEach(() => cleanup())

  it("opens on click as a labelled dialog, closes with Escape and gives focus back", async () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b"), result("b")]))
    const trigger = activityButton(container)
    expect(trigger.getAttribute("aria-haspopup")).toBe("dialog")
    trigger.focus()
    const dialog = openSteps(container)
    const labelledBy = dialog.getAttribute("aria-labelledby")
    expect(labelledBy && document.getElementById(labelledBy)?.textContent).toBe("Steps")
    expect(within(dialog).getAllByText("Ran a command")).toHaveLength(2)
    // Quiet: no "Completed" badge on a step that simply succeeded.
    expect(within(dialog).queryByText("Completed")).toBeNull()
    fireEvent.keyDown(dialog, { key: "Escape" })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it("keeps the raw command and output folded, with a Copy button", async () => {
    const onCopy = vi.fn(async () => true)
    const { container } = renderTurn(turn([
      part("tool_call", { tool_id: "a", tool_name: "Bash", input: { command: "wc -l loans.csv" } }, "Bash"),
      part("tool_result", { tool_id: "a" }, "577 loans.csv"),
    ]), onCopy)
    const dialog = openSteps(container)
    const raw = dialog.querySelector("details[data-testid='step-raw']") as HTMLDetailsElement
    expect(raw).not.toBeNull()
    expect(raw.open).toBe(false)
    expect(raw.textContent).toContain("wc -l loans.csv")
    expect(raw.textContent).toContain("577 loans.csv")
    fireEvent.click(within(dialog).getByLabelText("Copy command and output"))
    await waitFor(() => expect(onCopy).toHaveBeenCalledWith("$ wc -l loans.csv\n\n577 loans.csv"))
  })

  it("never shows a secret from a step's input", () => {
    const { container } = renderTurn(turn([
      part("tool_call", { tool_id: "a", tool_name: "mcp__x__login", input: { api_key: "sk-synthetic-123", user: "demo" } }, "mcp__x__login"),
      part("tool_result", { tool_id: "a" }, "ok"),
    ]))
    const dialog = openSteps(container)
    expect(dialog.textContent).not.toContain("sk-synthetic-123")
    expect(dialog.textContent).toContain("[REDACTED]")
  })

  it("stays open as the turn it describes changes", () => {
    const a = { ...part("activity", { tool_name: "Bash" }, "Bash"), tool_call_id: "a", activity: { kind: "shell", name: "Bash", state: "succeeded" } } as TurnPart
    const b = { ...part("activity", { tool_name: "Bash" }, "Bash"), tool_call_id: "b", activity: { kind: "shell", name: "Bash", state: "running" } } as TurnPart
    const withB = (state: string) => ({ ...b, activity: { ...b.activity!, state } }) as TurnPart
    const { container, rerender } = renderTurn(turn([a, b], true))
    openSteps(container)
    rerender(<AssistantTurn turn={turn([a, withB("failed")], true)} onCopy={async () => true} onFileClick={() => {}} />)
    expect(screen.getByRole("dialog")).toBeTruthy()
    expect(stepStates()).toEqual(["succeeded", "failed"])
  })
})

describe("review blockers: parallel calls, wire keys, history", () => {
  beforeEach(() => cleanup())
  const rUse = (id: string, extra: Record<string, unknown> = {}) => part("tool_result", { tool_use_id: id, ...extra }, "")

  it("pairs parallel calls by tool_use_id and puts the failure on the right call", () => {
    const { container } = renderTurn(turn([
      call("A"), call("B"), rUse("A", { is_error: true }), rUse("B"),
    ]))
    expect(activityButton(container).textContent).toContain("1 failed")
    expect(activityButton(container).textContent).not.toContain("unknown")
    openSteps(container)
    expect(stepStates()).toEqual(["failed", "succeeded"])
  })

  it("pairs parallel calls by tool_id too, and an all-success parallel turn reads Done", () => {
    const { container } = renderTurn(turn([call("A"), call("B"), result("A"), result("B")]))
    expect(tone(container)).toBe("ok")
    expect(activityButton(container).textContent).toContain("2 steps")
  })

  it("an orphan result classifies itself by its own is_error", () => {
    const { container } = renderTurn(turn([rUse("zzz", { is_error: true })]))
    openSteps(container)
    expect(stepStates()).toEqual(["failed"])
  })

  it("nests under parent_tool_use_id, the key the server emits", () => {
    const { container } = renderTurn(turn([
      call("task"), call("c1", { parent_tool_use_id: "task" }), result("c1"), result("task"),
    ]))
    openSteps(container)
    expect(screen.getAllByTestId("nested-activity")).toHaveLength(1)
  })

  it("a finished turn with no recorded result is neutral, not an alarm", () => {
    const { container } = renderTurn(turn([call("a"), result("a"), call("b")], false))
    expect(tone(container)).toBe("neutral")
    expect(activityButton(container).textContent).not.toContain("incomplete")
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

  it("a failed typed step is never a success and nests children by parent_tool_call_id", () => {
    const { container } = renderTurn(turn([typed("t", "succeeded"), typed("c", "failed", "t")]))
    expect(tone(container)).toBe("attention")
    openSteps(container)
    expect(screen.getAllByTestId("nested-activity")).toHaveLength(1)
    expect(stepStates()).toEqual(["succeeded", "failed"])
  })

  it("a typed unknown step reads Outcome unknown in the detail, once", () => {
    const { container } = renderTurn(turn([typed("a", "unknown")]))
    expect(screen.queryByText("Outcome unknown")).toBeNull()
    const dialog = openSteps(container)
    expect(within(dialog).getAllByText("Outcome unknown")).toHaveLength(1)
  })

  it("an input_streaming step reads as in progress, never Done", () => {
    const { container } = renderTurn(turn([typed("a", "succeeded"), typed("b", "input_streaming")], true))
    expect(tone(container)).toBe("running")
    expect(container.textContent).not.toContain("Done")
  })

  it("an unrecognised state token degrades to Outcome unknown, never Done", () => {
    const { container } = renderTurn(turn([typed("a", "succeeded"), typed("b", "paused")]))
    expect(activityButton(container).textContent).not.toContain("Done")
    openSteps(container)
    expect(stepStates()).toContain("unknown")
  })

  it("a typed running step on a finished turn reads Outcome unknown instead of spinning", () => {
    const { container } = renderTurn(turn([typed("a", "succeeded"), typed("b", "running")], false))
    expect(tone(container)).toBe("neutral")
    expect(activity(container).querySelector("[data-testid='working-mark'][data-active='true']")).toBeNull()
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
