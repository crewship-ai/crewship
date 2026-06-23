import { describe, it, expect, beforeEach } from "vitest"
import { useReactionsStore } from "@/stores/reactions-store"

// Coverage companion for reactions-store.test.ts — that file covers
// toggle/clear; this one drives the add/remove counting semantics.

beforeEach(() => {
  useReactionsStore.setState({ byTurn: {} })
})

describe("useReactionsStore.add", () => {
  it("adds an emoji with count 1 on a fresh turn", () => {
    useReactionsStore.getState().add("turn_1", "🚀")
    expect(useReactionsStore.getState().byTurn.turn_1["🚀"]).toBe(1)
  })

  it("increments the count on repeated adds", () => {
    useReactionsStore.getState().add("turn_1", "🚀")
    useReactionsStore.getState().add("turn_1", "🚀")
    useReactionsStore.getState().add("turn_1", "🚀")
    expect(useReactionsStore.getState().byTurn.turn_1["🚀"]).toBe(3)
  })

  it("tracks different emojis independently on the same turn", () => {
    useReactionsStore.getState().add("turn_1", "🚀")
    useReactionsStore.getState().add("turn_1", "👍")
    useReactionsStore.getState().add("turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1).toEqual({ "🚀": 1, "👍": 2 })
  })
})

describe("useReactionsStore.remove", () => {
  it("decrements a count above 1", () => {
    useReactionsStore.getState().add("turn_1", "👍")
    useReactionsStore.getState().add("turn_1", "👍")
    useReactionsStore.getState().remove("turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toBe(1)
  })

  it("deletes the emoji entry when the count drops to zero", () => {
    useReactionsStore.getState().add("turn_1", "👍")
    useReactionsStore.getState().remove("turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toBeUndefined()
  })

  it("is a safe no-op for an emoji that was never added", () => {
    useReactionsStore.getState().add("turn_1", "🎉")
    useReactionsStore.getState().remove("turn_1", "👻")
    expect(useReactionsStore.getState().byTurn.turn_1).toEqual({ "🎉": 1 })
  })

  it("is a safe no-op on a turn with no reactions at all", () => {
    useReactionsStore.getState().remove("turn_unknown", "👍")
    expect(useReactionsStore.getState().byTurn.turn_unknown).toEqual({})
  })

  it("does not leak counts across turns", () => {
    useReactionsStore.getState().add("turn_1", "👍")
    useReactionsStore.getState().add("turn_2", "👍")
    useReactionsStore.getState().remove("turn_1", "👍")
    expect(useReactionsStore.getState().byTurn.turn_1["👍"]).toBeUndefined()
    expect(useReactionsStore.getState().byTurn.turn_2["👍"]).toBe(1)
  })
})
