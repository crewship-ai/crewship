import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { useReactionsStore } from "@/stores/reactions-store"

// Local-state semantics of the store. The wire contract (URLs, methods,
// rollback) lives in reactions-store.server-sync.test.ts; here every
// request succeeds so the assertions are about the resulting map.

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

describe("useReactionsStore", () => {
  it("toggle adds an emoji on first call", async () => {
    await useReactionsStore.getState().toggle(CHAT, "turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toEqual({
      count: 1,
      mine: true,
    })
  })

  it("toggle removes the emoji on second call", async () => {
    await useReactionsStore.getState().toggle(CHAT, "turn_1", "👍")
    await useReactionsStore.getState().toggle(CHAT, "turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toBeUndefined()
  })

  it("toggle on a teammate's reaction adds mine on top instead of removing theirs", async () => {
    useReactionsStore.setState({
      byTurn: { turn_1: { "👍": { count: 1, mine: false } } },
    })
    await useReactionsStore.getState().toggle(CHAT, "turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toEqual({
      count: 2,
      mine: true,
    })
    expect(mockFetch.mock.calls[0][1].method).toBe("POST")
  })

  it("toggle is per-turn — same emoji on different turns is independent", async () => {
    await useReactionsStore.getState().toggle(CHAT, "turn_1", "🎉")
    await useReactionsStore.getState().toggle(CHAT, "turn_2", "🎉")
    expect(useReactionsStore.getState().byTurn.turn_1["🎉"].count).toBe(1)
    expect(useReactionsStore.getState().byTurn.turn_2["🎉"].count).toBe(1)
  })

  it("clear drops all reactions on a turn without calling the server", async () => {
    await useReactionsStore.getState().toggle(CHAT, "turn_1", "👍")
    await useReactionsStore.getState().toggle(CHAT, "turn_1", "🎉")
    mockFetch.mockClear()

    useReactionsStore.getState().clear("turn_1")

    const turn = useReactionsStore.getState().byTurn.turn_1
    expect(turn === undefined || Object.keys(turn).length === 0).toBe(true)
    expect(mockFetch).not.toHaveBeenCalled()
  })
})
