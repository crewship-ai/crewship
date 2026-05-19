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
  /** The user whose votes this state represents. Stored explicitly so we
   *  can detect a user switch (sign out + sign in as someone else on the
   *  same browser) and avoid rehydrating the previous user's votes into
   *  the new session. Null until setUser is called. */
  userId: string | null

  /** Per-turn map of signals the current user has submitted. Cleared on
   *  user switch. */
  byTurn: Record<string, Partial<Record<FeedbackSignal, true>>>

  /** Bind the store to an authenticated user. Called from a component
   *  with access to useSession(). When the userId changes (sign-in,
   *  account switch, sign-out → sign-in), the existing byTurn map is
   *  cleared — the persisted localStorage state is per-browser but
   *  votes are per-user, so a stale rehydrate would preselect the wrong
   *  thumbs for the new account. */
  setUser: (userId: string | null) => void

  /** Submit a feedback signal. Optimistic: the local map updates
   *  immediately and the POST happens in the background. The flip is
   *  rolled back on ANY failure — HTTP non-2xx OR network/transport
   *  rejection — so a persisted-localStorage flag never claims a row
   *  exists on the server when it doesn't.
   *
   *  Per-(turn, signal) sequencing: if a previous submit or reset for
   *  the same (turn, signal) is still in flight, this call waits for
   *  it before issuing its own HTTP request. Without this, a fast
   *  toggle (click → click again) could race: a slow POST landing
   *  AFTER a fast DELETE creates a row the user thinks they cleared. */
  submit: (turnId: string, signal: FeedbackSignal, opts?: {
    chatId?: string
    traceId?: string
    reason?: string
  }) => Promise<void>

  /** Toggle off a previously-submitted signal. DELETE on the server
   *  first so the eval pipeline doesn't keep counting a retracted
   *  signal, then clear local state on success. A failed DELETE keeps
   *  local state pointing at "submitted" so a refresh reconciles.
   *  Shares the per-(turn, signal) sequencing with submit. */
  reset: (turnId: string, signal: FeedbackSignal) => Promise<void>
}

// Per-(turn, signal) in-flight serialization. Lives in module scope —
// NOT persisted to localStorage and not part of the zustand state —
// because the in-flight promise itself cannot survive a page reload and
// because keeping Promises in zustand state would force every consumer
// to re-render on every chain link.
const inflight = new Map<string, Promise<void>>()

function inflightKey(userId: string, turnId: string, signal: FeedbackSignal): string {
  // Pipe-separated; userId/turnId/signal are all CUID-shaped + enum so
  // none of them carry the separator naturally.
  return `${userId}|${turnId}|${signal}`
}

/** chain registers `op` after any prior promise for the same key, then
 *  installs the new tail on the map. On resolution it cleans up only
 *  when it was the LAST scheduled op so the map doesn't grow without
 *  bound but doesn't accidentally drop a still-pending follow-up.
 *
 *  Implementation note: we attach the cleanup via the SAME `.then`
 *  call we return so there's no orphaned promise produced by a
 *  separate `.finally`. The cleanup ignores its input (the resolved
 *  value of `op`) and never throws, so the inner promise carries no
 *  rejection that would land as an unhandled-promise warning in
 *  Node/Vitest. */
function chain(key: string, op: () => Promise<void>): Promise<void> {
  const prev = inflight.get(key) ?? Promise.resolve()
  let self: Promise<void>
  const cleanup = () => {
    if (inflight.get(key) === self) inflight.delete(key)
  }
  self = prev.then(op, op).then(cleanup, cleanup)
  inflight.set(key, self)
  return self
}

export const useFeedbackStore = create<FeedbackState>()(
  persist(
    (set, get) => ({
      userId: null,
      byTurn: {},

      setUser: (userId) => {
        const current = get().userId
        if (current === userId) return
        // Sign out → sign in as someone else on the same browser must
        // not rehydrate the prior account's votes. Clear byTurn on any
        // user switch (including from null → real user, since that
        // would otherwise inherit pre-auth state).
        set({ userId, byTurn: {} })
      },

      submit: async (turnId, signal, opts = {}) => {
        const userId = get().userId
        if (!userId) {
          // No bound user means a misconfigured caller — the chat UI
          // wraps setUser around useSession, so reaching here means
          // someone forgot to bind the store. Refuse rather than
          // pollute the persisted state with unscoped writes.
          if (process.env.NODE_ENV !== "production") {
            console.warn("[feedback] submit called before setUser; ignoring")
          }
          return
        }

        return chain(inflightKey(userId, turnId, signal), async () => {
          // Re-check that the bound user hasn't changed between
          // scheduling and execution — a click-then-sign-out race
          // should not write the previous user's signal under the
          // new user's session.
          if (get().userId !== userId) return

          // Optimistic flip happens INSIDE the chained op, not at the
          // submit() call site, so a same-key DELETE that landed
          // first doesn't see its clear stomped by a stale POST flip.
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
              if (process.env.NODE_ENV !== "production") {
                console.warn(`[feedback] submit returned ${res.status}; rolling back`)
              }
              rollback()
            }
          } catch (err) {
            // Network rejection — the signal was NOT delivered. We
            // must roll back: persisted localStorage state would
            // otherwise claim a signal that never reached the server,
            // including after the user goes back online days later
            // without re-clicking.
            if (process.env.NODE_ENV !== "production") {
              console.warn("[feedback] submit network error; rolling back:", err)
            }
            rollback()
          }
        })
      },

      reset: async (turnId, signal) => {
        const userId = get().userId
        if (!userId) return

        return chain(inflightKey(userId, turnId, signal), async () => {
          if (get().userId !== userId) return

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
              console.warn("[feedback] reset network error; keeping local state:", err)
            }
            return
          }
          set((s) => {
            const cur = { ...(s.byTurn[turnId] ?? {}) }
            delete cur[signal]
            return { byTurn: { ...s.byTurn, [turnId]: cur } }
          })
        })
      },
    }),
    {
      name: "crewship.message_feedback",
      storage: createJSONStorage(() => localStorage),
      // Persist ONLY userId — not byTurn. The previous version
      // persisted both, which meant a sign-out followed by sign-in
      // as another account on the same browser would render the
      // previous user's votes for one frame before the bound
      // useEffect could fire setUser and clear them. Dropping byTurn
      // from persistence closes that window entirely: each tab/session
      // starts with an empty optimistic map and rebuilds it as the
      // user interacts. The trade-off is losing the cross-refresh
      // optimistic state — acceptable because the server is the
      // source of truth (a future enhancement could hydrate byTurn
      // via GET /api/v1/feedback?message_id=… per visible turn).
      partialize: (state) => ({ userId: state.userId }),
    },
  ),
)
