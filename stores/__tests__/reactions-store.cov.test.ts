import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { useReactionsStore } from "@/stores/reactions-store"

// Coverage companion for reactions-store.test.ts — that file covers
// toggle/clear; this one drives the add/remove counting semantics.
//
// Counting is per-user now that the server owns the rows: one user
// contributes at most one row per (message, emoji), so a repeated add
// does NOT stack. A count above 1 only comes from other people, which
// the tests below set up by seeding a teammate's tally.

const CHAT = "chat_1"
const noContent = () => ({ ok: true, status: 204 })

let mockFetch: ReturnType<typeof vi.fn>

beforeEach(() => {
  mockFetch = vi.fn().mockResolvedValue(noContent())
  vi.stubGlobal("fetch", mockFetch)
  useReactionsStore.setState({ byTurn: {} })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("useReactionsStore.add", () => {
  it("adds an emoji with count 1 on a fresh turn", async () => {
    await useReactionsStore.getState().add(CHAT, "turn_1", "🚀")
    expect(useReactionsStore.getState().byTurn.turn_1["🚀"]).toEqual({
      count: 1,
      mine: true,
    })
  })

  it("does not stack on repeated adds by the same user", async () => {
    await useReactionsStore.getState().add(CHAT, "turn_1", "🚀")
    await useReactionsStore.getState().add(CHAT, "turn_1", "🚀")
    await useReactionsStore.getState().add(CHAT, "turn_1", "🚀")
    expect(useReactionsStore.getState().byTurn.turn_1["🚀"]).toEqual({
      count: 1,
      mine: true,
    })
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  it("tracks different emojis independently on the same turn", async () => {
    await useReactionsStore.getState().add(CHAT, "turn_1", "🚀")
    useReactionsStore.setState({
      byTurn: {
        turn_1: {
          ...useReactionsStore.getState().byTurn.turn_1,
          "👍": { count: 1, mine: false },
        },
      },
    })
    await useReactionsStore.getState().add(CHAT, "turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1).toEqual({
      "🚀": { count: 1, mine: true },
      "👍": { count: 2, mine: true },
    })
  })
})

describe("useReactionsStore.remove", () => {
  it("decrements a count above 1 and clears the mine flag", async () => {
    useReactionsStore.setState({
      byTurn: { turn_1: { "👍": { count: 2, mine: true } } },
    })
    await useReactionsStore.getState().remove(CHAT, "turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toEqual({
      count: 1,
      mine: false,
    })
  })

  it("deletes the emoji entry when the count drops to zero", async () => {
    await useReactionsStore.getState().add(CHAT, "turn_1", "👍")
    await useReactionsStore.getState().remove(CHAT, "turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toBeUndefined()
  })

  it("is a safe no-op for an emoji that was never added", async () => {
    await useReactionsStore.getState().add(CHAT, "turn_1", "🎉")
    mockFetch.mockClear()
    await useReactionsStore.getState().remove(CHAT, "turn_1", "👻")
    expect(useReactionsStore.getState().byTurn.turn_1).toEqual({
      "🎉": { count: 1, mine: true },
    })
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("is a safe no-op on a turn with no reactions at all", async () => {
    await useReactionsStore.getState().remove(CHAT, "turn_unknown", "👍")
    expect(useReactionsStore.getState().byTurn.turn_unknown).toBeUndefined()
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("does not leak counts across turns", async () => {
    await useReactionsStore.getState().add(CHAT, "turn_1", "👍")
    await useReactionsStore.getState().add(CHAT, "turn_2", "👍")
    await useReactionsStore.getState().remove(CHAT, "turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toBeUndefined()
    expect(useReactionsStore.getState().byTurn.turn_2["👍"]).toEqual({
      count: 1,
      mine: true,
    })
  })
})
