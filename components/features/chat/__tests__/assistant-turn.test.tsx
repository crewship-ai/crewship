import { describe, it, expect, vi } from "vitest"
import React from "react"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"
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

vi.mock("@/components/ai-elements/tool", () => ({
  Tool: ({ children }: { children: React.ReactNode }) => <div data-testid="tool">{children}</div>,
  ToolContent: ({ children }: { children: React.ReactNode }) => <div data-testid="tool-content">{children}</div>,
  ToolHeader: ({ children }: { children: React.ReactNode }) => <div data-testid="tool-header">{children}</div>,
}))

vi.mock("@/components/ai-elements/code-block", () => ({
  CodeBlock: ({ code }: { code: string }) => <pre data-testid="code-block">{code}</pre>,
}))

vi.mock("@/components/features/chat/status-indicator", () => ({
  StatusIndicator: ({ content }: { content: string }) => (
    <div data-testid="status-indicator">{content}</div>
  ),
}))

import { AssistantTurn } from "../assistant-turn"

function part(partial: Partial<TurnPart> & { type: TurnPart["type"] }): TurnPart {
  return {
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
    id: overrides.id ?? "t-1",
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

  it("renders status part via StatusIndicator", () => {
    render(<AssistantTurn turn={turn([part({ type: "status", content: "Thinking..." })])} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.getByTestId("status-indicator").textContent).toBe("Thinking...")
  })

  it("renders thinking part via Reasoning with isStreaming passed through", () => {
    render(<AssistantTurn turn={turn([part({ type: "thinking", content: "Pondering", isStreaming: true })])} onCopy={onCopy} onFileClick={onFileClick} />)
    const reasoning = screen.getByTestId("reasoning")
    expect(reasoning.getAttribute("data-streaming")).toBe("true")
    expect(screen.getByTestId("reasoning-content").textContent).toBe("Pondering")
  })

  it("renders text part via MessageResponse", () => {
    render(<AssistantTurn turn={turn([part({ type: "text", content: "hello" })])} onCopy={onCopy} onFileClick={onFileClick} />)
    expect(screen.getByTestId("message-response").textContent?.trim()).toBe("hello")
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
      // the amber-50 class as the visual marker.
      const banner = screen.getByText("Hit a rate limit, slow down").parentElement
      expect(banner?.className).toMatch(/amber-/)
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
