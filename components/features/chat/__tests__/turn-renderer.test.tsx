import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import type { ChatTurn, TurnPart } from "@/hooks/use-chat"

// AssistantTurn is a 493-line component with its own deep tree; the renderer
// simply delegates to it for the "assistant" role. Replace it with a probe so
// we can assert delegation without exercising its internals.
vi.mock("../assistant-turn", () => ({
  AssistantTurn: vi.fn(({ turn }: { turn: ChatTurn }) => (
    <div data-testid="assistant-turn-mock" data-turn-id={turn.id} />
  )),
}))

import { TurnRenderer } from "../turn-renderer"

function makePart(partial: Partial<TurnPart> = {}): TurnPart {
  return {
    id: partial.id ?? "p1",
    type: partial.type ?? "text",
    content: partial.content ?? "",
    metadata: partial.metadata,
    isStreaming: partial.isStreaming,
    timestamp: partial.timestamp ?? new Date("2026-04-17T12:00:00Z"),
  }
}

function userTurn(text: string): ChatTurn {
  return {
    id: "u1",
    role: "user",
    parts: [makePart({ type: "text", content: text })],
    isStreaming: false,
    timestamp: new Date("2026-04-17T12:34:00Z"),
  }
}

function systemTurn(
  type: TurnPart["type"],
  content = "",
  metadata?: Record<string, unknown>,
): ChatTurn {
  return {
    id: "s1",
    role: "system",
    parts: [makePart({ type, content, metadata })],
    isStreaming: false,
    timestamp: new Date(),
  }
}

function assistantTurn(id = "a1", isStreaming = false): ChatTurn {
  return {
    id,
    role: "assistant",
    parts: [makePart({ content: "hello" })],
    isStreaming,
    timestamp: new Date(),
  }
}

const noop = () => {}

describe("TurnRenderer", () => {
  describe("user role", () => {
    it("renders text content", () => {
      render(<TurnRenderer turn={userTurn("hello world")} onCopy={noop} onFileClick={noop} />)
      expect(screen.getByText("hello world")).toBeTruthy()
    })

    it("falls back to empty string when no text part is present", () => {
      const turn: ChatTurn = {
        ...userTurn("ignored"),
        parts: [makePart({ type: "thinking", content: "internal" })],
      }
      // Render must not crash; the user bubble is still emitted.
      const { container } = render(
        <TurnRenderer turn={turn} onCopy={noop} onFileClick={noop} />,
      )
      expect(container.textContent).not.toContain("internal")
    })
  })

  describe("system role — system_init", () => {
    it("renders the Session started pill", () => {
      render(<TurnRenderer turn={systemTurn("system_init")} onCopy={noop} onFileClick={noop} />)
      expect(screen.getByText(/Session started/)).toBeTruthy()
    })

    it("shows the model chip when metadata.model is present", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { model: "claude-opus-4-7" })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText("claude-opus-4-7")).toBeTruthy()
    })

    it("shows tool count when tools list is non-empty", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { tools: ["a", "b", "c"] })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText(/3 tools/)).toBeTruthy()
    })

    it("omits tool count when tools list is empty", () => {
      render(
        <TurnRenderer
          turn={systemTurn("system_init", "", { tools: [] })}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.queryByText(/tools/)).toBeNull()
    })
  })

  describe("system role — error/info", () => {
    it("renders the content for an error part", () => {
      render(
        <TurnRenderer
          turn={systemTurn("error", "something blew up")}
          onCopy={noop}
          onFileClick={noop}
        />,
      )
      expect(screen.getByText("something blew up")).toBeTruthy()
    })
  })

  describe("assistant role", () => {
    it("delegates rendering to AssistantTurn", () => {
      render(
        <TurnRenderer turn={assistantTurn("a-42")} onCopy={noop} onFileClick={noop} />,
      )
      const probe = screen.getByTestId("assistant-turn-mock")
      expect(probe.getAttribute("data-turn-id")).toBe("a-42")
    })

    it("shows Regenerate when isLastAssistant + onRegenerate + not streaming", () => {
      render(
        <TurnRenderer
          turn={assistantTurn("a1", false)}
          onCopy={noop}
          onFileClick={noop}
          isLastAssistant
          onRegenerate={vi.fn()}
        />,
      )
      expect(screen.getByText("Regenerate")).toBeTruthy()
    })

    it("hides Regenerate while streaming", () => {
      render(
        <TurnRenderer
          turn={assistantTurn("a1", true)}
          onCopy={noop}
          onFileClick={noop}
          isLastAssistant
          onRegenerate={vi.fn()}
        />,
      )
      expect(screen.queryByText("Regenerate")).toBeNull()
    })

    it("hides Regenerate when onRegenerate is not provided", () => {
      render(
        <TurnRenderer
          turn={assistantTurn("a1", false)}
          onCopy={noop}
          onFileClick={noop}
          isLastAssistant
        />,
      )
      expect(screen.queryByText("Regenerate")).toBeNull()
    })

    it("hides Regenerate when isLastAssistant is false", () => {
      render(
        <TurnRenderer
          turn={assistantTurn("a1", false)}
          onCopy={noop}
          onFileClick={noop}
          onRegenerate={vi.fn()}
        />,
      )
      expect(screen.queryByText("Regenerate")).toBeNull()
    })
  })
})
