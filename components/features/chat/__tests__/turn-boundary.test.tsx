/**
 * The chat tree's own error boundary (issue #1957).
 *
 * Every value a turn renders is adapter-supplied: init metadata is forwarded
 * verbatim, and a key that holds a string today can hold an object tomorrow.
 * When one of those reads threw, nothing between it and the route replaced the
 * page — so the whole dashboard segment was torn down, taking `useChat`'s turn
 * state and the live WebSocket of the very session that had degraded.
 *
 * What is asserted here is the blast radius, not the pretty card:
 *
 *  1. One unrenderable turn costs its own slot. The sibling turns and the
 *     composer — which stands in for the live session state around the list —
 *     keep their DOM nodes, which is the test-visible form of "not remounted".
 *  2. A turn that threw RECOVERS when good content arrives. This is the trap
 *     documented in `components/features/pages/panels/registry.tsx`: a boundary
 *     keyed on stable identity alone stays broken forever, and a streaming turn
 *     mutates under a constant `turn.id`, so one bad token shape would wedge
 *     that turn until a full reload.
 */
import { describe, it, expect, vi, beforeEach } from "vitest"
import * as React from "react"
import { render, screen, fireEvent } from "@testing-library/react"
import type { ReactNode } from "react"
import type { ChatTurn, TurnPart } from "@/hooks/use-chat"

const { captureException } = vi.hoisted(() => ({ captureException: vi.fn() }))
vi.mock("@sentry/nextjs", () => ({ captureException }))

// A poisoned token stands in for any assistant payload this build cannot
// render. The real one is whatever the next adapter change ships; what the test
// needs is a throw that happens during render of ONE turn, under a turn id that
// does not move while the content does.
const POISON = "<<bad-token>>"

vi.mock("../assistant-turn", () => ({
  AssistantTurn: ({ turn }: { turn: ChatTurn }) => {
    const text = turn.parts.map((p) => p.content).join("")
    if (text.includes("<<bad-token>>")) {
      throw new TypeError("Cannot read properties of undefined (reading 'map')")
    }
    return <div data-testid="assistant-turn">{text}</div>
  },
}))

// Radix mounts HoverCardContent only while the card is open, and no pointer in
// this environment opens it — same substitution the sibling suite makes.
vi.mock("@/components/ui/hover-card", () => ({
  HoverCard: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
  HoverCardTrigger: ({ children }: { children?: ReactNode }) => <>{children}</>,
  HoverCardContent: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
}))

import { TurnRenderer, turnContentKey } from "../turn-renderer"

const noop = () => {}

function part(partial: Partial<TurnPart> = {}): TurnPart {
  return {
    id: partial.id ?? "p1",
    type: partial.type ?? "text",
    content: partial.content ?? "",
    metadata: partial.metadata,
    isStreaming: partial.isStreaming,
    timestamp: partial.timestamp ?? new Date("2026-08-20T12:00:00Z"),
  }
}

function userTurn(id: string, text: string): ChatTurn {
  return {
    id,
    role: "user",
    parts: [part({ id: `${id}-p`, type: "text", content: text })],
    isStreaming: false,
    timestamp: new Date("2026-08-20T12:00:00Z"),
  }
}

/** A `system_init` turn whose `model` field throws the moment it is read.
 *
 *  Not a contrivance: init metadata is passed through unparsed, `as` says
 *  nothing about what is actually there, and the shipped instance of this bug
 *  was `.trim()` on a value that had become an object. A throwing getter is the
 *  smallest way to reproduce "reading this field during render throws" without
 *  pinning the test to one field's current coercion. */
function hostileInitTurn(id = "sys-init"): ChatTurn {
  return {
    id,
    role: "system",
    parts: [
      part({
        id: `${id}-p`,
        type: "system_init",
        metadata: {
          get model(): string {
            throw new TypeError("value.trim is not a function")
          },
        } as Record<string, unknown>,
      }),
    ],
    isStreaming: false,
    timestamp: new Date("2026-08-20T12:00:00Z"),
  }
}

function assistantTurn(id: string, text: string, isStreaming = true): ChatTurn {
  return {
    id,
    role: "assistant",
    parts: [part({ id: `${id}-p`, type: "text", content: text, isStreaming })],
    isStreaming,
    timestamp: new Date("2026-08-20T12:00:00Z"),
  }
}

/** The list plus one piece of live session state beside it. The composer's
 *  value is what a user loses when the route boundary replaces the page: it
 *  lives above the turns and outside anything a turn can throw into. */
function ChatHarness({ turns }: { turns: ChatTurn[] }) {
  const [draft, setDraft] = React.useState("")
  return (
    <div>
      <div data-testid="turn-list">
        {turns.map((turn) => (
          <TurnRenderer key={turn.id} turn={turn} onCopy={noop} onFileClick={noop} />
        ))}
      </div>
      <input
        data-testid="composer"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
      />
    </div>
  )
}

beforeEach(() => {
  captureException.mockClear()
})

describe("one unrenderable turn costs its own slot", () => {
  it("keeps the sibling turns, the composer node and the composer's draft", () => {
    const { rerender } = render(
      <ChatHarness turns={[userTurn("u1", "first message"), userTurn("u2", "second message")]} />,
    )

    const composer = screen.getByTestId("composer") as HTMLInputElement
    fireEvent.change(composer, { target: { value: "half-typed reply" } })
    expect(composer.value).toBe("half-typed reply")

    // A degraded session's init event arrives mid-conversation.
    rerender(
      <ChatHarness
        turns={[userTurn("u1", "first message"), hostileInitTurn(), userTurn("u2", "second message")]}
      />,
    )

    // The bad turn renders a card in its own slot...
    expect(screen.getByTestId("turn-error")).toBeTruthy()
    expect(screen.getByText(/could not be rendered/i)).toBeTruthy()

    // ...and nothing else moved: same composer element, same draft, both
    // siblings still on screen.
    expect(screen.getByTestId("composer")).toBe(composer)
    expect((screen.getByTestId("composer") as HTMLInputElement).value).toBe("half-typed reply")
    expect(screen.getByText("first message")).toBeTruthy()
    expect(screen.getByText("second message")).toBeTruthy()
  })

  it("reports the throw to Sentry tagged with the boundary and the turn", () => {
    render(<ChatHarness turns={[hostileInitTurn("sys-42")]} />)

    expect(captureException).toHaveBeenCalledTimes(1)
    const [error, context] = captureException.mock.calls[0]
    expect(error).toBeInstanceOf(TypeError)
    expect(context.tags).toMatchObject({ boundary: "chat-turn", turnId: "sys-42" })
  })

  it("retries the turn on demand without touching the rest of the session", () => {
    render(<ChatHarness turns={[userTurn("u1", "first message"), hostileInitTurn()]} />)

    const composer = screen.getByTestId("composer")
    fireEvent.click(screen.getByRole("button", { name: /try again/i }))

    // The payload is still bad, so the card comes back — but the retry is not
    // a page reload: the session around it is the same DOM.
    expect(screen.getByTestId("turn-error")).toBeTruthy()
    expect(screen.getByTestId("composer")).toBe(composer)
    expect(screen.getByText("first message")).toBeTruthy()
  })
})

describe("the resetKeys trap — a turn that threw must recover on new content", () => {
  it("recovers when good content replaces the bad payload under the same turn id", () => {
    const { rerender } = render(
      <ChatHarness turns={[userTurn("u1", "first message"), assistantTurn("a1", `Thinking${POISON}`)]} />,
    )

    const composer = screen.getByTestId("composer") as HTMLInputElement
    fireEvent.change(composer, { target: { value: "keep me" } })
    expect(screen.getByTestId("turn-error")).toBeTruthy()

    // Same turn, same id, more tokens — exactly what a streaming turn does.
    // A boundary keyed on `turn.id` alone never sees this and stays broken
    // until a full reload; that is the bug registry.tsx documents.
    rerender(
      <ChatHarness turns={[userTurn("u1", "first message"), assistantTurn("a1", "Thinking about it")]} />,
    )

    expect(screen.queryByTestId("turn-error")).toBeNull()
    expect(screen.getByTestId("assistant-turn").textContent).toBe("Thinking about it")

    // Recovery is not a remount of the page around it.
    expect(screen.getByTestId("composer")).toBe(composer)
    expect((screen.getByTestId("composer") as HTMLInputElement).value).toBe("keep me")
  })

  it("keeps the card while the payload is unchanged — no reset loop", () => {
    const bad = assistantTurn("a1", `Thinking${POISON}`)
    const { rerender } = render(<ChatHarness turns={[bad]} />)
    expect(screen.getByTestId("turn-error")).toBeTruthy()

    rerender(<ChatHarness turns={[bad]} />)
    expect(screen.getByTestId("turn-error")).toBeTruthy()
    // One error, not one per render.
    expect(captureException).toHaveBeenCalledTimes(1)
  })

  it("moves the content key when a streaming turn grows under a constant id", () => {
    const before = turnContentKey(assistantTurn("a1", "Think"))
    const after = turnContentKey(assistantTurn("a1", "Thinking about it"))
    expect(after).not.toBe(before)
  })

  it("moves the content key when only the metadata shape changes", () => {
    const before = turnContentKey(hostileInitTurn())
    const fixed: ChatTurn = {
      ...hostileInitTurn(),
      parts: [
        part({
          id: "sys-init-p",
          type: "system_init",
          metadata: { model: "claude-opus-4", claude_code_version: "2.1.0" },
        }),
      ],
    }
    expect(turnContentKey(fixed)).not.toBe(before)
  })

  it("never throws on the payload that made the turn throw", () => {
    // The key is computed OUTSIDE the boundary. A throw here would escape to
    // the route boundary — the failure this whole change exists to prevent.
    const proxied = {
      ...assistantTurn("a1", "hi"),
      parts: new Proxy([] as TurnPart[], {
        get() {
          throw new Error("hostile parts")
        },
      }),
    } as ChatTurn
    expect(() => turnContentKey(proxied)).not.toThrow()
  })
})
