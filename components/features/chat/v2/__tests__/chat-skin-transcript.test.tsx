import { describe, it, expect, vi } from "vitest"
import type { ReactNode } from "react"
import { render, screen, cleanup } from "@testing-library/react"

import type { ChatTurn, TurnPart } from "@/hooks/use-chat"

// Record what ThinkingBlock asks Reasoning for. `defaultOpen` is the whole
// point of these tests, so it has to be observable rather than inferred from
// whether some text happens to be in the DOM.
const reasoningCalls: { isStreaming?: boolean; defaultOpen?: boolean }[] = []
vi.mock("@/components/ai-elements/reasoning", () => ({
  Reasoning: ({ children, isStreaming, defaultOpen }: {
    children: ReactNode
    isStreaming?: boolean
    defaultOpen?: boolean
  }) => {
    reasoningCalls.push({ isStreaming, defaultOpen })
    return (
      <div data-testid="reasoning" data-default-open={String(!!defaultOpen)}>
        {children}
      </div>
    )
  },
  ReasoningTrigger: ({ children }: { children?: ReactNode }) => (
    <span data-testid="reasoning-trigger">{children ?? <span data-testid="default-trigger" />}</span>
  ),
  ReasoningContent: ({ children }: { children: ReactNode }) => (
    <div data-testid="reasoning-content">{children}</div>
  ),
  useReasoning: () => ({ isStreaming: true, isOpen: false, duration: undefined, elapsed: 4 }),
  thinkingLiveLabel: (elapsed: number) => `Thinking… ${elapsed}s`,
  thoughtForLabel: (d?: number) => `Thought for ${d ?? 0} seconds`,
}))

vi.mock("@/hooks/use-smooth-text", () => ({ useSmoothText: (t: string) => t }))

vi.mock("@/components/ui/hover-card", () => ({
  HoverCard: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
  HoverCardTrigger: ({ children }: { children?: ReactNode }) => <>{children}</>,
  HoverCardContent: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
}))

// The avatar components reach for DiceBear collections and the auth session;
// neither is what these tests are about. Probes keep the assertions on "is
// there a face in this gutter" rather than on which PNG it resolved to.
vi.mock("../thinking-avatar", () => ({
  ThinkingAvatar: ({ active }: { active: boolean }) => (
    <div data-testid="thinking-avatar" data-active={String(active)} />
  ),
}))
vi.mock("../turn-user-avatar", () => ({
  ChatTurnUserAvatar: () => <div data-testid="user-avatar" />,
}))

import { ChatSkinProvider } from "../chat-skin"
import { AssistantTurn } from "../../assistant-turn"
import { TurnRenderer } from "../../turn-renderer"

function part(p: Partial<TurnPart> = {}): TurnPart {
  return {
    id: p.id ?? "p1",
    type: p.type ?? "text",
    content: p.content ?? "",
    metadata: p.metadata,
    isStreaming: p.isStreaming,
    timestamp: p.timestamp ?? new Date("2026-08-26T12:00:00Z"),
  }
}

function thinkingTurn(streaming: boolean): ChatTurn {
  return {
    id: "a1",
    role: "assistant",
    parts: [part({ id: "th", type: "thinking", content: "weighing options", isStreaming: streaming })],
    isStreaming: streaming,
    timestamp: new Date("2026-08-26T12:00:00Z"),
  }
}

function userTurn(): ChatTurn {
  return {
    id: "u1",
    role: "user",
    parts: [part({ type: "text", content: "ahoj" })],
    isStreaming: false,
    timestamp: new Date("2026-08-26T12:00:00Z"),
  }
}

const AGENT = { id: "ag1", name: "Morgan" }

function renderSkinned(node: ReactNode, variant: "classic" | "v2") {
  return render(
    <ChatSkinProvider variant={variant} agent={variant === "v2" ? AGENT : null}>
      {node}
    </ChatSkinProvider>,
  )
}

const noop = () => {}

describe("reasoning is never auto-opened in v2", () => {
  it("classic still opens the block while it streams", () => {
    reasoningCalls.length = 0
    renderSkinned(
      <AssistantTurn turn={thinkingTurn(true)} onCopy={noop} onFileClick={noop} />,
      "classic",
    )
    expect(reasoningCalls.at(-1)).toMatchObject({ isStreaming: true, defaultOpen: true })
    cleanup()
  })

  it("v2 leaves it closed even while it streams", () => {
    // The defect this whole skin starts from: a streaming reasoning block
    // dumps the model's private deliberation into the transcript and then
    // takes it away a second later. The chevron still opens it on request.
    reasoningCalls.length = 0
    renderSkinned(
      <AssistantTurn turn={thinkingTurn(true)} onCopy={noop} onFileClick={noop} />,
      "v2",
    )
    expect(reasoningCalls.at(-1)).toMatchObject({ isStreaming: true, defaultOpen: false })
    expect(screen.getByTestId("reasoning")).toHaveAttribute("data-default-open", "false")
    cleanup()
  })

  it("v2 keeps it closed once the stream has ended too", () => {
    reasoningCalls.length = 0
    renderSkinned(
      <AssistantTurn turn={thinkingTurn(false)} onCopy={noop} onFileClick={noop} />,
      "v2",
    )
    expect(reasoningCalls.at(-1)).toMatchObject({ isStreaming: false, defaultOpen: false })
    cleanup()
  })

  it("names the agent in the v2 header, where classic says only 'Thinking'", () => {
    renderSkinned(
      <AssistantTurn turn={thinkingTurn(true)} onCopy={noop} onFileClick={noop} />,
      "v2",
    )
    expect(screen.getByTestId("reasoning-trigger").textContent).toContain("Morgan is thinking")
    // The default brain-glyph trigger must NOT render: the turn already has a
    // face in the gutter and two animated marks on one turn is one too many.
    expect(screen.queryByTestId("default-trigger")).toBeNull()
    cleanup()
  })

  it("falls back to the classic trigger when no skin is present", () => {
    render(<AssistantTurn turn={thinkingTurn(true)} onCopy={noop} onFileClick={noop} />)
    expect(screen.getByTestId("default-trigger")).toBeTruthy()
    cleanup()
  })
})

describe("the transcript gutters", () => {
  it("gives an assistant turn a face and drives it from isStreaming", () => {
    renderSkinned(
      <TurnRenderer turn={thinkingTurn(true)} onCopy={noop} onFileClick={noop} />,
      "v2",
    )
    expect(screen.getByTestId("thinking-avatar")).toHaveAttribute("data-active", "true")
    cleanup()
  })

  it("stops the animation the moment the turn settles", () => {
    // Same flag the Regenerate affordance waits on, so the ring cannot still
    // be turning on a turn the reader is already allowed to act on.
    renderSkinned(
      <TurnRenderer turn={thinkingTurn(false)} onCopy={noop} onFileClick={noop} />,
      "v2",
    )
    expect(screen.getByTestId("thinking-avatar")).toHaveAttribute("data-active", "false")
    cleanup()
  })

  it("gives a user turn a face in the opposite gutter", () => {
    renderSkinned(<TurnRenderer turn={userTurn()} onCopy={noop} onFileClick={noop} />, "v2")
    expect(screen.getByTestId("user-avatar")).toBeTruthy()
    expect(screen.queryByTestId("thinking-avatar")).toBeNull()
    cleanup()
  })

  it("draws no gutters at all on the classic route", () => {
    renderSkinned(<TurnRenderer turn={userTurn()} onCopy={noop} onFileClick={noop} />, "classic")
    expect(screen.queryByTestId("user-avatar")).toBeNull()
    cleanup()
    renderSkinned(
      <TurnRenderer turn={thinkingTurn(true)} onCopy={noop} onFileClick={noop} />,
      "classic",
    )
    expect(screen.queryByTestId("thinking-avatar")).toBeNull()
    cleanup()
  })
})
