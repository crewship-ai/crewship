import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import React from "react"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

// The emoji picker is a frimousse popover with its own data loading; here it
// only needs to hand an emoji back, so replace it with a button that picks 👍.
vi.mock("../reactions/reaction-picker", () => ({
  ReactionPicker: ({ onPick }: { onPick: (emoji: string) => void }) => (
    <button data-testid="pick" onClick={() => onPick("👍")}>
      react
    </button>
  ),
}))

// motion spreads animation props into the DOM under happy-dom; strip them.
vi.mock("motion/react", () => ({
  AnimatePresence: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  motion: {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    button: ({ children, initial: _i, animate: _a, exit: _e, transition: _t, layout: _l, ...rest }: any) => (
      <button {...rest}>{children}</button>
    ),
  },
}))

import { TurnReactions, TurnReactionPicker } from "../reactions/turn-reactions"
import { useReactionsStore } from "@/stores/reactions-store"

const CHAT = "chat_1"
const MSG = "msg_1"
const BASE = `/api/v1/chats/${CHAT}/messages/${MSG}/reactions`

const noContent = () => ({ ok: true, status: 204 })
const list = (reactions: unknown[]) => ({
  ok: true,
  status: 200,
  json: async () => ({ reactions }),
})

let mockFetch: ReturnType<typeof vi.fn>
let warnSpy: ReturnType<typeof vi.spyOn>

/** Renders the two halves of the reactions UI the way AssistantTurn does.
 *  `chatId` is passed explicitly — no default — so the "no chat id" case
 *  really renders without one. */
function Turn({ chatId }: { chatId?: string }) {
  return (
    <>
      <TurnReactions chatId={chatId} messageId={MSG} streaming={false} />
      <TurnReactionPicker chatId={chatId} messageId={MSG} />
    </>
  )
}

/** Answers the mount-time GET with an empty list, then defers to `then`. */
function routeFetch(then: (url: string, init?: RequestInit) => unknown) {
  return (url: string, init?: RequestInit) => {
    if (!init?.method || init.method === "GET") return Promise.resolve(list([]))
    return Promise.resolve(then(url, init))
  }
}

/** A response the test releases by hand, so the optimistic state can be
 *  observed while the request is still in flight. */
function deferred() {
  let settle!: (value: unknown) => void
  const promise = new Promise<unknown>((resolve) => {
    settle = resolve
  })
  return { promise, settle }
}

beforeEach(() => {
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
  warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {})
  useReactionsStore.setState({ byTurn: {} })
})

afterEach(() => {
  cleanup()
  warnSpy.mockRestore()
  vi.unstubAllGlobals()
})

describe("TurnReactions", () => {
  it("hydrates from the server on mount so a teammate's reaction is visible", async () => {
    mockFetch.mockResolvedValue(list([{ emoji: "🎉", count: 2, mine: false }]))

    render(<Turn chatId={CHAT} />)

    expect(await screen.findByLabelText("🎉 2")).toBeTruthy()
    expect(mockFetch).toHaveBeenCalledWith(BASE, expect.anything())
  })

  it("picking an emoji POSTs it and shows it while the POST is in flight", async () => {
    const post = deferred()
    mockFetch.mockImplementation(routeFetch(() => post.promise))

    render(<Turn chatId={CHAT} />)
    fireEvent.click(screen.getByTestId("pick"))

    // Optimistic: on screen before the POST answers.
    expect(await screen.findByLabelText("👍 1")).toBeTruthy()
    const call = mockFetch.mock.calls.find((c) => c[1]?.method === "POST")
    expect(call).toBeTruthy()
    expect(call![0]).toBe(BASE)
    expect(JSON.parse(call![1].body)).toEqual({ emoji: "👍" })

    post.settle(noContent())
    await waitFor(() => expect(screen.getByLabelText("👍 1")).toBeTruthy())
  })

  it("clicking my own reaction DELETEs it", async () => {
    mockFetch.mockImplementation(routeFetch(() => noContent()))
    render(<Turn chatId={CHAT} />)
    fireEvent.click(screen.getByTestId("pick"))
    const chip = await screen.findByLabelText("👍 1")

    fireEvent.click(chip)

    await waitFor(() => {
      const del = mockFetch.mock.calls.find((c) => c[1]?.method === "DELETE")
      expect(del).toBeTruthy()
      expect(del![0]).toBe(`${BASE}/${encodeURIComponent("👍")}`)
    })
    await waitFor(() => expect(screen.queryByLabelText(/👍/)).toBeNull())
  })

  it("does not leave a reaction on screen that the server rejected", async () => {
    const post = deferred()
    mockFetch.mockImplementation(routeFetch(() => post.promise))

    render(<Turn chatId={CHAT} />)
    fireEvent.click(screen.getByTestId("pick"))
    expect(await screen.findByLabelText("👍 1")).toBeTruthy()

    post.settle({ ok: false, status: 400 })

    await waitFor(() => expect(screen.queryByLabelText(/👍/)).toBeNull())
  })

  it("does not leave a reaction on screen when the request never lands", async () => {
    mockFetch.mockImplementation(
      routeFetch(() => {
        throw new Error("offline")
      }),
    )

    render(<Turn chatId={CHAT} />)
    fireEvent.click(screen.getByTestId("pick"))

    await waitFor(() => expect(screen.queryByLabelText(/👍/)).toBeNull())
  })

  it("renders nothing and makes no request without a chat id", () => {
    render(<Turn />)

    expect(screen.queryByTestId("pick")).toBeNull()
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("makes no request while the turn is still streaming", () => {
    render(<TurnReactions chatId={CHAT} messageId={MSG} streaming />)

    expect(mockFetch).not.toHaveBeenCalled()
  })
})
