"use client"

import { create } from "zustand"
import { persist, createJSONStorage } from "zustand/middleware"

// Signal vocabulary mirrors the v95 message_feedback CHECK constraint.
// The eval pipeline + drift detector query against these strings, so
// adding a new kind here requires a matching migration that widens the
// CHECK clause — otherwise a POST will 400 on a value the UI can produce.
export type FeedbackSignal =
  | "helpful"
  | "not_helpful"
  | "inaccurate"
  | "unsafe"
  | "edit"
  | "regenerate"

interface FeedbackState {
  /** Per-turn map of signals the current user already submitted, so the
   *  UI can render the active state on the button without refetching. */
  byTurn: Record<string, Partial<Record<FeedbackSignal, true>>>

  /** Submit a feedback signal. Optimistic: the local map updates
   *  immediately and the POST happens in the background. On HTTP failure
   *  we leave the optimistic state intact — the user shouldn't see a
   *  flickering thumb because the network blipped, and the server-side
   *  UPSERT will reconcile on the next attempt. */
  submit: (turnId: string, signal: FeedbackSignal, opts?: {
    chatId?: string
    traceId?: string
    reason?: string
  }) => Promise<void>

  /** Local-only un-submit. Used when the user clicks the same thumb
   *  twice to toggle off. The backend doesn't currently expose a delete
   *  endpoint — the row stays but the UI state goes back to neutral.
   *  Acceptable for v1 because the eval pipeline reads on a rolling
   *  window where stale rows are harmless. */
  reset: (turnId: string, signal: FeedbackSignal) => void
}

export const useFeedbackStore = create<FeedbackState>()(
  persist(
    (set, get) => ({
      byTurn: {},

      submit: async (turnId, signal, opts = {}) => {
        // Optimistic update first — UI reflects the click without
        // waiting on the round-trip.
        set((s) => ({
          byTurn: {
            ...s.byTurn,
            [turnId]: { ...(s.byTurn[turnId] ?? {}), [signal]: true },
          },
        }))

        try {
          await fetch("/api/v1/feedback", {
            method: "POST",
            credentials: "include",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              message_id: turnId,
              chat_id: opts.chatId,
              trace_id: opts.traceId,
              signal,
              reason: opts.reason,
            }),
          })
        } catch (err) {
          // Log but don't roll back — the user gave the signal, we
          // just couldn't deliver it. The next submit on the same
          // turn+signal tuple will UPSERT and the server reconciles.
          if (process.env.NODE_ENV !== "production") {
            console.warn("[feedback] submit failed:", err)
          }
        }
      },

      reset: (turnId, signal) =>
        set((s) => {
          const cur = { ...(s.byTurn[turnId] ?? {}) }
          delete cur[signal]
          return { byTurn: { ...s.byTurn, [turnId]: cur } }
        }),
    }),
    {
      name: "crewship.message_feedback",
      storage: createJSONStorage(() => localStorage),
    },
  ),
)
