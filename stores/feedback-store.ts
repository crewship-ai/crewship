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
   *  immediately and the POST happens in the background. If the POST
   *  comes back !res.ok (validation, auth, server error), the optimistic
   *  flip is rolled back so the UI doesn't lie about a signal the server
   *  never recorded. Network rejections (offline, DNS) keep the flip
   *  because a retry could still succeed and the server-side UPSERT
   *  will reconcile. */
  submit: (turnId: string, signal: FeedbackSignal, opts?: {
    chatId?: string
    traceId?: string
    reason?: string
  }) => Promise<void>

  /** Toggle off a previously-submitted signal. Calls DELETE on the
   *  server first so the eval pipeline doesn't keep counting a
   *  retracted signal, then clears local state on success. A failed
   *  DELETE keeps the local state pointing at "submitted" so a refresh
   *  reconciles back to truth; the user can retry. */
  reset: (turnId: string, signal: FeedbackSignal) => Promise<void>
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

        const rollback = () =>
          set((s) => {
            const cur = { ...(s.byTurn[turnId] ?? {}) }
            delete cur[signal]
            return { byTurn: { ...s.byTurn, [turnId]: cur } }
          })

        try {
          const res = await fetch("/api/v1/feedback", {
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
          if (!res.ok) {
            // 4xx/5xx — the server REJECTED the signal. The optimistic
            // flip is now a lie; reverse it so the UI matches truth.
            // The user can click again to retry once the issue clears
            // (e.g. session restored, validation fixed).
            if (process.env.NODE_ENV !== "production") {
              console.warn(`[feedback] submit returned ${res.status}; rolling back`)
            }
            rollback()
          }
        } catch (err) {
          // Network rejection — keep the optimistic state so the user
          // doesn't see a flicker on transient offline blips. A
          // subsequent submit on the same (turn, signal) UPSERTs
          // server-side.
          if (process.env.NODE_ENV !== "production") {
            console.warn("[feedback] submit network error:", err)
          }
        }
      },

      reset: async (turnId, signal) => {
        // DELETE on the server FIRST so a failure keeps the local
        // state pointing at "submitted" — better UX than a phantom
        // un-submitted thumb that re-appears on refresh.
        try {
          const res = await fetch(
            `/api/v1/feedback?message_id=${encodeURIComponent(turnId)}&signal=${encodeURIComponent(signal)}`,
            { method: "DELETE", credentials: "include" },
          )
          if (!res.ok) {
            if (process.env.NODE_ENV !== "production") {
              console.warn(`[feedback] reset returned ${res.status}; keeping local state`)
            }
            return
          }
        } catch (err) {
          if (process.env.NODE_ENV !== "production") {
            console.warn("[feedback] reset network error:", err)
          }
          return
        }
        set((s) => {
          const cur = { ...(s.byTurn[turnId] ?? {}) }
          delete cur[signal]
          return { byTurn: { ...s.byTurn, [turnId]: cur } }
        })
      },
    }),
    {
      name: "crewship.message_feedback",
      storage: createJSONStorage(() => localStorage),
    },
  ),
)
