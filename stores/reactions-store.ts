"use client"

import { create } from "zustand"
import { apiFetch } from "@/lib/api-fetch"

// Emoji reactions on a chat message. The server owns them —
// `message_reactions` (migration v57), one row per
// (chat, message, emoji, user), served by
// internal/api/message_reactions.go:
//
//   GET    /api/v1/chats/{chatId}/messages/{messageId}/reactions
//            → 200 {"reactions":[{emoji,count,mine}]}
//   POST   /api/v1/chats/{chatId}/messages/{messageId}/reactions
//            body {"emoji":"👍"} → 204, idempotent (INSERT OR IGNORE)
//   DELETE /api/v1/chats/{chatId}/messages/{messageId}/reactions/{emoji}
//            → 204, no-op when the row isn't there
//
// This store is the optimistic layer in front of those three calls, in
// the same shape as the sibling feedback store: flip locally so the
// picker stays instant, then reconcile — a non-2xx or a transport
// rejection restores the pre-click entry, so the row never claims a
// reaction the server refused.
//
// NOT persisted. Until this file called the API at all, reactions lived
// only in localStorage under `crewship-reactions`, which is why two
// people on the same conversation saw different rows forever. That
// state cannot be replayed onto the server — it recorded a turn id and
// a count, never the chat id the endpoint needs nor which user reacted,
// and most of its keys are per-session UUIDs from a live stream that no
// longer name anything. So it is deleted on load rather than
// half-migrated, and the server list is re-read on mount instead.

/** One emoji's tally on one message, mirroring the handler's row shape.
 *  `mine` is what makes a click decide POST vs DELETE: a teammate's 👍
 *  is not mine to retract, and clicking it must add my own. */
export interface ReactionEntry {
  count: number
  mine: boolean
}

export type ReactionMap = Record<string, ReactionEntry>

interface ReactionsState {
  /** Per-message map: { messageId: { emoji: {count, mine} } }. Message
   *  ids are CUIDs (globally unique), so the chat id is a call argument
   *  rather than part of the key. */
  byTurn: Record<string, ReactionMap>

  /** Re-read one message's reactions from the server. Safe to call on
   *  mount; skips its write if a mutation for the same message is
   *  in flight so a slow GET can't undo a just-clicked reaction. */
  hydrate: (chatId: string, messageId: string) => Promise<void>

  /** Add the current user's reaction. No-op (and no request) when the
   *  user already reacted with this emoji — the server's INSERT OR
   *  IGNORE would accept it, but there is nothing to change. */
  add: (chatId: string, messageId: string, emoji: string) => Promise<void>

  /** Retract the current user's reaction. No-op when it isn't mine. */
  remove: (chatId: string, messageId: string, emoji: string) => Promise<void>

  /** Add or retract depending on whether this user already reacted. */
  toggle: (chatId: string, messageId: string, emoji: string) => Promise<void>

  /** Forget one message's local tallies. Local only — this is cache
   *  eviction, not a retraction; it sends nothing. */
  clear: (messageId: string) => void
}

const LEGACY_STORAGE_KEY = "crewship-reactions"

// One-shot drop of the pre-server-sync localStorage state. See the note
// at the top of the file for why it is discarded rather than migrated.
if (typeof window !== "undefined") {
  try {
    localStorage.removeItem(LEGACY_STORAGE_KEY)
  } catch {
    // Private mode / disabled storage — nothing to drop.
  }
}

function reactionsURL(chatId: string, messageId: string): string {
  return `/api/v1/chats/${encodeURIComponent(chatId)}/messages/${encodeURIComponent(messageId)}/reactions`
}

// Per-(chat, message, emoji) in-flight serialization, module scope for
// the same reasons as the feedback store: a Promise cannot be persisted
// and keeping it in zustand state would re-render every consumer on
// every link. A fast add → remove on one emoji must not let the POST
// land after the DELETE and resurrect the row.
const inflight = new Map<string, Promise<void>>()

function inflightKey(chatId: string, messageId: string, emoji: string): string {
  return `${chatId}|${messageId}|${emoji}`
}

/** True while any emoji on this message has a mutation in flight. */
function messageBusy(chatId: string, messageId: string): boolean {
  const prefix = `${chatId}|${messageId}|`
  for (const key of inflight.keys()) {
    if (key.startsWith(prefix)) return true
  }
  return false
}

/**
 * How many mutations a message has seen, ever.
 *
 * `messageBusy` alone cannot make hydrate safe: it answers "is one in flight
 * RIGHT NOW", and the losing case is a mutation that starts AND finishes
 * inside the GET's window. Click 👍 the moment a turn renders, the POST
 * returns before the list does, `inflight` is empty again by the time hydrate
 * checks — and the pre-click snapshot overwrites the reaction the server has
 * already accepted. The chip disappears until the next mount.
 *
 * So hydrate reads this counter before its request and again before its
 * write, and stands down if it moved. Module scope, not zustand state, for
 * the same reason `inflight` is: bumping it must not re-render every
 * consumer.
 */
const mutations = new Map<string, number>()

function messageEpoch(chatId: string, messageId: string): number {
  return mutations.get(`${chatId}|${messageId}`) ?? 0
}

function bumpEpoch(chatId: string, messageId: string): void {
  const k = `${chatId}|${messageId}`
  mutations.set(k, (mutations.get(k) ?? 0) + 1)
}

/**
 * Serialise one emoji's mutations, and mark the message as moved on.
 *
 * The epoch is bumped HERE rather than at each call site so no future
 * mutation can be added without it — a mutation that forgets to bump is a
 * mutation hydrate is allowed to overwrite.
 */
function chain(key: string, op: () => Promise<void>): Promise<void> {
  const [chatId, messageId] = key.split("|")
  bumpEpoch(chatId, messageId)
  const prev = inflight.get(key) ?? Promise.resolve()
  let self: Promise<void>
  const cleanup = () => {
    if (inflight.get(key) === self) inflight.delete(key)
  }
  self = prev.then(op, op).then(cleanup, cleanup)
  inflight.set(key, self)
  return self
}

function warn(msg: string, ...rest: unknown[]): void {
  if (process.env.NODE_ENV !== "production") {
    console.warn(`[reactions] ${msg}`, ...rest)
  }
}

export const useReactionsStore = create<ReactionsState>()((set, get) => {
  /** Writes (or deletes, when the entry is gone or empty) one emoji's
   *  tally. Used for both the optimistic flip and the rollback, so a
   *  failed request restores exactly the entry that was there. */
  const writeEntry = (
    messageId: string,
    emoji: string,
    entry: ReactionEntry | undefined,
  ) =>
    set((s) => {
      const cur = { ...(s.byTurn[messageId] ?? {}) }
      if (!entry || entry.count <= 0) delete cur[emoji]
      else cur[emoji] = entry
      return { byTurn: { ...s.byTurn, [messageId]: cur } }
    })

  return {
    byTurn: {},

    hydrate: async (chatId, messageId) => {
      if (!chatId || !messageId) return
      // Captured BEFORE the request. Anything that mutates this message from
      // here on makes the response below stale, whether or not it is still in
      // flight when it arrives.
      const epoch = messageEpoch(chatId, messageId)
      let payload: { reactions?: unknown }
      try {
        const res = await apiFetch(reactionsURL(chatId, messageId))
        if (!res.ok) {
          warn(`list returned ${res.status}; keeping local state`)
          return
        }
        payload = (await res.json()) as { reactions?: unknown }
      } catch (err) {
        warn("list failed; keeping local state:", err)
        return
      }
      // A mutation that touched this message while the GET was in flight is
      // newer than this snapshot — let it stand and re-read on the next
      // mount. The epoch catches the ones that already finished; messageBusy
      // catches an optimistic write whose request has not returned yet and so
      // has not bumped anything the server agrees with.
      if (messageEpoch(chatId, messageId) !== epoch) return
      if (messageBusy(chatId, messageId)) return
      const next: ReactionMap = {}
      for (const row of Array.isArray(payload.reactions) ? payload.reactions : []) {
        const r = row as Partial<ReactionEntry> & { emoji?: unknown }
        if (typeof r.emoji !== "string" || typeof r.count !== "number") continue
        if (r.count <= 0) continue
        next[r.emoji] = { count: r.count, mine: r.mine === true }
      }
      set((s) => ({ byTurn: { ...s.byTurn, [messageId]: next } }))
    },

    add: (chatId, messageId, emoji) =>
      chain(inflightKey(chatId, messageId, emoji), async () => {
        const before = get().byTurn[messageId]?.[emoji]
        if (before?.mine) return
        writeEntry(messageId, emoji, { count: (before?.count ?? 0) + 1, mine: true })
        try {
          const res = await apiFetch(reactionsURL(chatId, messageId), {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ emoji }),
          })
          if (!res.ok) {
            // 400 = the emoji failed the server's composition check,
            // 404 = not our chat, 5xx = it never stored. All three mean
            // the row the user is now looking at does not exist.
            warn(`add returned ${res.status}; rolling back`)
            writeEntry(messageId, emoji, before)
          }
        } catch (err) {
          warn("add failed; rolling back:", err)
          writeEntry(messageId, emoji, before)
        }
      }),

    remove: (chatId, messageId, emoji) =>
      chain(inflightKey(chatId, messageId, emoji), async () => {
        const before = get().byTurn[messageId]?.[emoji]
        // Only the caller's own row is deletable server-side; without
        // one there is nothing to send.
        if (!before?.mine) return
        writeEntry(messageId, emoji, { count: before.count - 1, mine: false })
        try {
          const res = await apiFetch(
            `${reactionsURL(chatId, messageId)}/${encodeURIComponent(emoji)}`,
            { method: "DELETE" },
          )
          if (!res.ok) {
            warn(`remove returned ${res.status}; restoring`)
            writeEntry(messageId, emoji, before)
          }
        } catch (err) {
          warn("remove failed; restoring:", err)
          writeEntry(messageId, emoji, before)
        }
      }),

    toggle: (chatId, messageId, emoji) => {
      const mine = get().byTurn[messageId]?.[emoji]?.mine === true
      return mine
        ? get().remove(chatId, messageId, emoji)
        : get().add(chatId, messageId, emoji)
    },

    clear: (messageId) =>
      set((s) => {
        const next = { ...s.byTurn }
        delete next[messageId]
        return { byTurn: next }
      }),
  }
})
