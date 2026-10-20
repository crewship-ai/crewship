import { describe, it, expect, vi } from "vitest"
import React from "react"
import { act, render, screen, fireEvent, cleanup } from "@testing-library/react"
import type { ChatTurn, TurnPart } from "@/hooks/use-chat"
import { feedbackTurnKey, useFeedbackStore } from "@/stores/feedback-store"

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
  ToolHeader: ({ children, title, state, statusLabel }: { children: React.ReactNode; title?: string; state?: string; statusLabel?: string }) => <div data-testid="tool-header" data-state={state} data-label={statusLabel ?? ""} title={title}>{children}</div>,
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

function part(partial: Partial<TurnPart> & { type: TurnPart["type"] }): TurnPart {
  return {
    ...partial,
    id: partial.id ?? `p-${Math.random().toString(36).slice(2, 8)}`,
    type: partial.type,
    content: partial.content ?? "",
    metadata: partial.metadata,
    isStreaming: partial.isStreaming,
    timestamp: partial.timestamp ?? new Date("2026-04-17T00:00:00Z"),
  }
}

function turn(parts: TurnPart[], overrides: Partial<ChatTurn> = {}): ChatTurn {
  return {
    ...overrides,
    id: overrides.id ?? "t-1",
    runId: overrides.runId,
    role: "assistant",
    parts,
    isStreaming: overrides.isStreaming ?? false,
    timestamp: overrides.timestamp ?? new Date("2026-04-17T00:00:00Z"),
  }
}

describe("AssistantTurn dispatch", () => {
  const onCopy = vi.fn()
  const onFileClick = vi.fn()

  beforeEach(() => {
    onCopy.mockReset()
    onFileClick.mockReset()
    cleanup()
  })

  it("hides previously saved Codex startup progress without changing the reply or copied text", () => {
    render(
      <AssistantTurn
        turn={turn([part({ type: "text", content: "Reading additional input from stdin...\nAhoj! Co pro tebe můžu udělat?" })])}
        onCopy={onCopy}
        onFileClick={onFileClick}
      />,
    )
    expect(screen.queryByText(/Reading additional input from stdin/)).toBeNull()
    expect(screen.getByText("Ahoj! Co pro tebe můžu udělat?")).toBeTruthy()
    fireEvent.click(screen.getByTestId("action-copy"))
    expect(onCopy).toHaveBeenCalledWith("Ahoj! Co pro tebe můžu udělat?")
  })

  // The result footer's model badge. It used to read Object.keys(model_usage)[0],
  // and because Go's encoding/json emits map keys sorted, the CLI's small
  // housekeeping model ("claude-haiku-4-5-…") always arrived ahead of the model
  // the agent actually reasons with ("claude-opus-5"). An operator asking
  // "which model is this agent on?" got the wrong answer, reliably.
  describe("result footer model badge", () => {
    // Key order here is the order the server really sends: sorted, so haiku first.
    const bothModels = {
      "claude-haiku-4-5-20251001": { inputTokens: 40, outputTokens: 12 },
      "claude-opus-5": { inputTokens: 9000, outputTokens: 2400 },
    }

    it("prefers the server-stamped scalar model over the model_usage map", () => {
      render(
        <AssistantTurn
          turn={turn([
            part({
              type: "result",
              metadata: { model: "claude-opus-5", model_usage: bothModels, num_turns: 1 },
            }),
          ])}
          onCopy={onCopy}
          onFileClick={onFileClick}
        />,
      )
      expect(screen.getAllByText(/claude-opus-5/).length).toBeGreaterThan(0)
      expect(screen.queryAllByText(/claude-haiku/)).toHaveLength(0)
    })

    it("falls back to the highest-token model, not the alphabetically first", () => {
      // No scalar `model` — an older server or a replayed transcript. The
      // housekeeping model must still not win just because it sorts first.
      render(
        <AssistantTurn
          turn={turn([part({ type: "result", metadata: { model_usage: bothModels, num_turns: 1 } })])}
          onCopy={onCopy}
          onFileClick={onFileClick}
        />,
      )
      expect(screen.getAllByText(/claude-opus-5/).length).toBeGreaterThan(0)
      expect(screen.queryAllByText(/claude-haiku/)).toHaveLength(0)
    })

    it("still reports a genuinely cheap agent's own model", () => {
      // Guard against 'fixing' this with a name blacklist: an agent really
      // configured on the cheap model must report it, not fall through to
      // nothing.
      render(
        <AssistantTurn
          turn={turn([
            part({
              type: "result",
              metadata: {
                model_usage: { "claude-haiku-4-5-20251001": { inputTokens: 5000, outputTokens: 900 } },
                num_turns: 1,
              },
            }),
          ])}
          onCopy={onCopy}
          onFileClick={onFileClick}
        />,
      )
      expect(screen.getAllByText(/claude-haiku-4-5/).length).toBeGreaterThan(0)
    })
  })

  it("says Stopping while a stop waits for the server, without hiding the partial reply", () => {
    render(<AssistantTurn turn={turn([part({ type: "text", content: "Half an answer" })], { isStreaming: true, cancelRequested: true, turnState: "running" })} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.getByRole("status")).toHaveTextContent("Stopping…")
    expect(screen.getByTestId("message-response")).toHaveTextContent("Half an answer")
  })
  it.each([
    ["cancelled", "Stopped. The reply above is partial."],
    ["interrupted", "Interrupted. The reply above is partial."],
    ["failed", "The run failed. The reply above may be incomplete."],
  ] as const)("names a %s turn in words", (turnState, text) => {
    render(<AssistantTurn turn={turn([part({ type: "text", content: "Half" })], { turnState })} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.getByRole("status")).toHaveTextContent(text)
  })
  it("does not call a reply partial when the turn ended before any output", () => {
    render(<AssistantTurn turn={turn([], { turnState: "cancelled" })} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.getByRole("status")).toHaveTextContent("Stopped before any reply.")
  })
  it("adds no note to a completed turn", () => {
    render(<AssistantTurn turn={turn([part({ type: "text", content: "Done" })], { turnState: "completed" })} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.queryByRole("status")).toBeNull()
  })
  it("says Copied only after the clipboard confirmed the copy", async () => {
    let confirm!: (ok: boolean) => void
    const copy = vi.fn(() => new Promise<boolean>(resolve => { confirm = resolve }))
    render(<AssistantTurn turn={turn([part({ type: "text", content: "Answer" })])} onCopy={copy} onFileClick={onFileClick} />)
    fireEvent.click(screen.getByTestId("action-copy"))
    expect(copy).toHaveBeenCalledWith("Answer")
    expect(screen.queryByText("Copied to clipboard")).toBeNull()
    await act(async () => confirm(true))
    expect(screen.getByRole("status")).toHaveTextContent("Copied to clipboard")
    expect(screen.getByTestId("action-copied")).toBeInTheDocument()
  })
  it("says the copy failed instead of pretending it worked", async () => {
    render(<AssistantTurn turn={turn([part({ type: "text", content: "Answer" })])} onCopy={() => Promise.resolve(false)} onFileClick={onFileClick} />)
    await act(async () => { fireEvent.click(screen.getByTestId("action-copy")) })
    expect(screen.getByRole("status")).toHaveTextContent("Copy failed. Select the text and copy it with your keyboard.")
    expect(screen.queryByText("Copied to clipboard")).toBeNull()
  })

  it("renders status part via StatusIndicator", () => {
    render(<AssistantTurn turn={turn([part({ type: "status", content: "Thinking..." })])} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.getByTestId("status-indicator").textContent).toBe("Thinking...")
  })

  it("renders thinking part via Reasoning with isStreaming passed through", () => {
    render(<AssistantTurn turn={turn([part({ type: "thinking", content: "legacy ignored", isStreaming: true, thinking: { visibility: "summary", state: "running", summary: "Pondering" } })])} onCopy={onCopy} onFileClick={onFileClick} />)
    const reasoning = screen.getByTestId("reasoning")
    expect(reasoning.getAttribute("data-streaming")).toBe("true")
    // ThinkingBlock appends a trailing space while streaming on purpose
    // (Streamdown reflow workaround, see assistant-turn.tsx) — compare trimmed.
    expect(screen.getByTestId("reasoning-content").textContent?.trim()).toBe("Pondering")
  })

  it("renders text part via MessageResponse", () => {
    render(<AssistantTurn turn={turn([part({ type: "text", content: "hello" })])} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.getByTestId("message-response").textContent?.trim()).toBe("hello")
  })

  it("does not render AskUserQuestion metadata as clickable-looking choices", () => {
    const { container } = render(
      <AssistantTurn
        turn={turn([part({
          type: "tool_call",
          content: "AskUserQuestion",
          metadata: {
            tool_name: "AskUserQuestion",
            input: {
              questions: [{
                header: "Choose",
                question: "Which environment?",
                options: [{ label: "Production", description: "Use live data" }],
              }],
            },
          },
        })])}
        onCopy={onCopy}
        onFileClick={onFileClick}
      />,
    )

    expect(screen.getByTitle(/AskUserQuestion/)).toBeTruthy()
    expect(container.querySelector(".cursor-default")).toBeNull()
    expect(container.querySelector("button")).toBeNull()
  })

  it("delegates [DELEGATED ...] text to the DelegationContent branch (no MessageResponse)", () => {
    render(<AssistantTurn turn={turn([part({ type: "text", content: "[DELEGATED to Viktor] run tests" })])} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.queryByTestId("message-response")).toBeNull()
  })

  it("renders a streaming text part with a trailing space (caret hint)", () => {
    render(<AssistantTurn turn={turn([part({ type: "text", content: "streaming", isStreaming: true })], { isStreaming: true })} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.getByTestId("message-response").textContent).toBe("streaming ")
  })

  describe("error dispatch", () => {
    it("uses the amber rate-limit banner for 'rate limit' content", () => {
      render(<AssistantTurn turn={turn([part({ type: "error", content: "Hit a rate limit, slow down" })])} onCopy={onCopy} onFileClick={onFileClick} />)
      // Rate-limit branch emits a plain div (no MessageContent wrapper) with
      // the warn token class as the visual marker.
      const banner = screen.getByText("Hit a rate limit, slow down").parentElement
      expect(banner?.className).toMatch(/warn/)
      expect(screen.queryByTestId("message-content")).toBeNull()
    })

    it("uses the amber banner for '429' content", () => {
      render(<AssistantTurn turn={turn([part({ type: "error", content: "HTTP 429 Too Many Requests" })])} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.queryByTestId("message-content")).toBeNull()
    })

    it("uses the destructive red error for any other error content", () => {
      render(<AssistantTurn turn={turn([part({ type: "error", content: "agent crashed" })])} onCopy={onCopy} onFileClick={onFileClick} />)
      const box = screen.getByTestId("message-content")
      expect(box.className).toMatch(/destructive/)
      expect(box.textContent).toContain("agent crashed")
    })
  })

  describe("image media_type validation (XSS guard)", () => {
    it("accepts an allowed PNG media_type verbatim", () => {
      const { container } = render(<AssistantTurn turn={turn([part({ type: "image", content: "AAAA", metadata: { media_type: "image/png" } })])} onCopy={onCopy} onFileClick={onFileClick} />)
      const img = container.querySelector("img")
      expect(img?.getAttribute("src")).toBe("data:image/png;base64,AAAA")
    })

    it("accepts an allowed JPEG media_type", () => {
      const { container } = render(<AssistantTurn turn={turn([part({ type: "image", content: "BBBB", metadata: { media_type: "image/jpeg" } })])} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(container.querySelector("img")?.getAttribute("src")).toBe("data:image/jpeg;base64,BBBB")
    })

    it("falls back to image/png for a disallowed media_type (blocks data:image/svg+xml XSS)", () => {
      const { container } = render(<AssistantTurn turn={turn([part({ type: "image", content: "CCCC", metadata: { media_type: "image/svg+xml" } })])} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(container.querySelector("img")?.getAttribute("src")).toBe("data:image/png;base64,CCCC")
    })

    it("falls back to image/png when media_type is non-string garbage", () => {
      const { container } = render(<AssistantTurn turn={turn([part({ type: "image", content: "DDDD", metadata: { media_type: 123 as unknown as string } })])} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(container.querySelector("img")?.getAttribute("src")).toBe("data:image/png;base64,DDDD")
    })
  })

  describe("file creation notification (path sanitization)", () => {
    it("shows a Preview button for a clean relative filename", () => {
      render(<AssistantTurn turn={turn([part({ type: "text", content: "File created: `src/app.ts`" })])} onCopy={onCopy} onFileClick={onFileClick} />)
      // The text "File created:" also appears in the echoed MessageResponse,
      // so scope the click to the Preview button instead.
      const previewSpan = screen.getByText(/Preview/)
      const btn = previewSpan.closest("button")!
      fireEvent.click(btn)
      expect(onFileClick).toHaveBeenCalledWith("src/app.ts")
    })

    it("rejects path traversal (..)", () => {
      render(<AssistantTurn turn={turn([part({ type: "text", content: "File created: `../etc/passwd`" })])} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.queryByText(/Preview/)).toBeNull()
    })

    it("rejects absolute paths (/)", () => {
      render(<AssistantTurn turn={turn([part({ type: "text", content: "File created: `/etc/shadow`" })])} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.queryByText(/Preview/)).toBeNull()
    })

    it("does not render the notification while still streaming", () => {
      render(<AssistantTurn turn={turn([part({ type: "text", content: "File created: `src/app.ts`" })], { isStreaming: true })} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.queryByText(/Preview/)).toBeNull()
    })
  })

  describe("actions toolbar", () => {
    it("does not reuse a source workspace vote for a forked turn with the same ID", () => {
      useFeedbackStore.setState({ userId: "test-user", byTurn: {
        [feedbackTurnKey("t-1", "ws-source")]: { helpful: true },
      } })
      const reply = turn([part({ type: "text", content: "answer" })])
      const view = render(<AssistantTurn turn={reply} workspaceId="ws-source" onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.getByTestId("action-marked-good-—-click-to-undo")).toBeTruthy()

      view.rerender(<AssistantTurn turn={reply} workspaceId="ws-fork" onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.getByTestId("action-good-response")).toBeTruthy()
      expect(useFeedbackStore.getState().byTurn[feedbackTurnKey("t-1", "ws-source")]).toEqual({ helpful: true })
    })

    it("renders Copy/Up/Down when not streaming and has text", () => {
      render(<AssistantTurn turn={turn([part({ type: "text", content: "answer" })])} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.getByTestId("action-copy")).toBeTruthy()
      expect(screen.getByTestId("action-good-response")).toBeTruthy()
      expect(screen.getByTestId("action-bad-response")).toBeTruthy()
    })

    it("Copy button invokes onCopy with joined text content", () => {
      render(
        <AssistantTurn
          turn={turn([
            part({ type: "text", content: "hello " }),
            part({ type: "text", content: "world" }),
          ])}
          onCopy={onCopy}
          onFileClick={onFileClick}
        />,
      )
      fireEvent.click(screen.getByTestId("action-copy"))
      expect(onCopy).toHaveBeenCalledWith("hello world")
    })

    it("hides actions while the turn is still streaming", () => {
      render(<AssistantTurn turn={turn([part({ type: "text", content: "still going" })], { isStreaming: true })} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.queryByTestId("message-actions")).toBeNull()
    })

    it("hides actions for a delegation-only turn", () => {
      render(<AssistantTurn turn={turn([part({ type: "text", content: "[DELEGATED to Viktor] x" })])} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.queryByTestId("message-actions")).toBeNull()
    })

    it("hides actions when there is no text content at all", () => {
      render(<AssistantTurn turn={turn([part({ type: "thinking", content: "ponder" })])} onCopy={onCopy} onFileClick={onFileClick} />)
      expect(screen.queryByTestId("message-actions")).toBeNull()
    })
  })
})


describe("authorized run image mount", () => {
  it("mounts the image viewer from persisted run identity without model image URLs", () => {
    render(<AssistantTurn turn={turn([part({ type: "text", content: "Here is your chart." })], { runId: "run-chart" })} agentId="agent-chart" workspaceId="ws-chart" chatId="chat-chart" onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.getByTestId("run-images")).toHaveTextContent("agent-chart:ws-chart:run-chart")
  })
  it("does not discover image paths from model text or unfinished replies", () => {
    const view = render(<AssistantTurn turn={turn([part({ type: "text", content: "https://untrusted.invalid/chart.png" })])} agentId="agent-chart" workspaceId="ws-chart" chatId="chat-chart" onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.queryByTestId("run-images")).toBeNull()
    view.rerender(<AssistantTurn turn={turn([], { runId: "run-chart", isStreaming: true })} agentId="agent-chart" workspaceId="ws-chart" chatId="chat-chart" onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.queryByTestId("run-images")).toBeNull()
  })
})

describe("safe canonical activity and reasoning", () => {
  beforeEach(() => cleanup())
  it("never renders legacy raw thinking", () => {
    render(<AssistantTurn turn={turn([part({ type: "thinking", content: "private legacy reasoning" })])} onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.queryByText("private legacy reasoning")).toBeNull()
  })
  it("renders only the typed safe summary, ignoring the legacy content field", () => {
    render(<AssistantTurn turn={turn([part({ type: "thinking", content: "private legacy reasoning", thinking: { visibility: "summary", state: "completed", summary: "Checked the public sources" } })])} onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.getByText("Checked the public sources")).toBeTruthy()
    expect(screen.queryByText("private legacy reasoning")).toBeNull()
  })
  it.each(["failed", "denied", "interrupted"] as const)("does not label %s activity Completed", state => {
    render(<AssistantTurn turn={turn([part({ type: "tool_call", content: "Read", activity: { kind: "read", state }, metadata: { completed: true } })])} onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.getByTestId("tool-header").getAttribute("data-state")).not.toBe("output-available")
    expect(screen.getByText(state === "failed" ? "Failed" : state === "denied" ? "Denied" : "Interrupted")).toBeTruthy()
  })
})

describe("honest activity completion", () => {
  beforeEach(() => cleanup())
  it("does not invent running progress for an unknown group", () => {
    const view = render(<AssistantTurn turn={turn([
      part({ id: "a", type: "activity", tool_call_id: "a", activity: { kind: "read", state: "unknown" } }),
      part({ id: "b", type: "activity", tool_call_id: "b", activity: { kind: "read", state: "succeeded" } }),
    ])} onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.getAllByTestId("tool-header").some((h) => h.getAttribute("data-label") === "Outcome unknown")).toBe(true)
    expect(screen.queryByText("Working")).toBeNull()
    expect(view.container.querySelector(".animate-spin")).toBeNull()
  })
  it("marks a legacy single failed result's call as failed", () => {
    render(<AssistantTurn turn={turn([
      part({ id: "a", type: "tool_call", metadata: { tool_id: "a", tool_name: "Read" } }),
      part({ id: "r", type: "tool_result", content: "Failed to read", metadata: { tool_id: "a", is_error: true } }),
    ])} onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.getAllByTestId("tool-header").map(header => header.getAttribute("data-state"))).toEqual(["output-error", "output-error"])
  })
})

describe("typed safe activity details", () => {
  beforeEach(() => cleanup())
  it("shows only typed output/diff and an explicit truncation marker", () => {
    render(<AssistantTurn turn={turn([part({ type: "activity", tool_call_id: "safe-call", content: "raw legacy detail", metadata: { input: { command: "raw legacy detail" } }, activity: { kind: "read", state: "succeeded", output: "Safe bounded output", diff: "-before\n+after", truncated: true } })])} onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.getByText("Safe bounded output")).toBeTruthy()
    expect(screen.getAllByTestId("code-block").map(block => block.textContent)).toContain("-before\n+after")
    expect(screen.getByText("Details truncated")).toBeTruthy()
    expect(screen.queryByText(/raw legacy detail/)).toBeNull()
  })
  it("never falls back to a legacy result body for a typed activity", () => {
    render(<AssistantTurn turn={turn([
      part({ type: "activity", tool_call_id: "safe-call", activity: { kind: "read", state: "succeeded" } }),
      part({ type: "tool_result", content: "raw legacy detail", metadata: { tool_id: "safe-call" } }),
    ])} onCopy={() => {}} onFileClick={() => {}} />)
    expect(screen.queryByText("raw legacy detail")).toBeNull()
  })
})

it("does not revive legacy Task input or running status inside a typed failed activity", () => {
  cleanup()
  render(<AssistantTurn turn={turn([part({ type: "activity", tool_call_id: "task", metadata: { tool_name: "Task", input: { description: "raw legacy task detail" } }, activity: { kind: "task", name: "Task", state: "failed" } })])} onCopy={() => {}} onFileClick={() => {}} />)
  expect(screen.getByText("Failed")).toBeTruthy()
  expect(screen.queryByText("raw legacy task detail")).toBeNull()
  expect(screen.queryByText("Working")).toBeNull()
})

it("labels an incomplete canonical stream without claiming recovery or completion", () => {
  render(<AssistantTurn turn={turn([part({ type: "text", content: "partial answer" })], { streamIncomplete: true, isStreaming: true })} onCopy={vi.fn()} onFileClick={vi.fn()} />)
  expect(screen.getByRole("status")).toHaveTextContent("Partial response — stream recovery pending")
})
