"use client"

import { create } from "zustand"
import { persist, createJSONStorage } from "zustand/middleware"

interface ReactionsState {
  /** Per-turn map: { turnId: { emoji: count } }. */
  byTurn: Record<string, Record<string, number>>
  toggle: (turnId: string, emoji: string) => void
  add: (turnId: string, emoji: string) => void
  remove: (turnId: string, emoji: string) => void
  clear: (turnId: string) => void
}

export const useReactionsStore = create<ReactionsState>()(
  persist(
    (set) => ({
      byTurn: {},
      toggle: (turnId, emoji) =>
        set((s) => {
          const cur = s.byTurn[turnId] ?? {}
          const exists = (cur[emoji] ?? 0) > 0
          const next = { ...cur }
          if (exists) {
            delete next[emoji]
          } else {
            next[emoji] = 1
          }
          return { byTurn: { ...s.byTurn, [turnId]: next } }
        }),
      add: (turnId, emoji) =>
        set((s) => {
          const cur = s.byTurn[turnId] ?? {}
          return {
            byTurn: {
              ...s.byTurn,
              [turnId]: { ...cur, [emoji]: (cur[emoji] ?? 0) + 1 },
            },
          }
        }),
      remove: (turnId, emoji) =>
        set((s) => {
          const cur = s.byTurn[turnId] ?? {}
          const next = { ...cur }
          if ((next[emoji] ?? 0) <= 1) delete next[emoji]
          else next[emoji] = next[emoji] - 1
          return { byTurn: { ...s.byTurn, [turnId]: next } }
        }),
      clear: (turnId) =>
        set((s) => {
          const next = { ...s.byTurn }
          delete next[turnId]
          return { byTurn: next }
        }),
    }),
    {
      name: "crewship-reactions",
      storage: createJSONStorage(() => localStorage),
    },
  ),
)
