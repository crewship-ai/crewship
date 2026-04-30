import { describe, it, expect, beforeEach } from "vitest"
import { useReactionsStore } from "@/stores/reactions-store"

beforeEach(() => {
  useReactionsStore.setState({ byTurn: {} })
})

describe("useReactionsStore", () => {
  it("toggle adds an emoji on first call", () => {
    useReactionsStore.getState().toggle("turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toBe(1)
  })

  it("toggle removes the emoji on second call", () => {
    useReactionsStore.getState().toggle("turn_1", "👍")
    useReactionsStore.getState().toggle("turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toBeUndefined()
  })

  it("toggle is per-turn — same emoji on different turns is independent", () => {
    useReactionsStore.getState().toggle("turn_1", "🎉")
    useReactionsStore.getState().toggle("turn_2", "🎉")
    expect(useReactionsStore.getState().byTurn.turn_1["🎉"]).toBe(1)
    expect(useReactionsStore.getState().byTurn.turn_2["🎉"]).toBe(1)
  })

  it("clear drops all reactions on a turn", () => {
    useReactionsStore.getState().toggle("turn_1", "👍")
    useReactionsStore.getState().toggle("turn_1", "🎉")
    useReactionsStore.getState().clear("turn_1")
    const turn = useReactionsStore.getState().byTurn.turn_1
    expect(turn === undefined || Object.keys(turn).length === 0).toBe(true)
  })
})
