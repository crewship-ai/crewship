"use client"

import { useState } from "react"

/**
 * Keeps a list's rows where they are while `hold` is true (pointer or keyboard
 * focus is inside it). Live updates still reach rows already shown (new unread
 * count, new title), but rows are neither added nor reordered under the
 * pointer; `pending` says the real list differs and `refresh` applies it.
 * Releasing the hold applies it on its own. Holding freezes ORDER only: a row
 * missing from the new data (deleted, access revoked) disappears at once, so
 * stale content is never shown.
 *
 * The hold is for BACKGROUND change only. `resetKey` must change whenever the
 * user caused the list to change (search text, filters, favourites, the
 * selected row); the snapshot is then replaced immediately, hold or not.
 * `same` decides whether a row is unchanged (default identity).
 */
export function useStableOrder<T>(items: T[], key: (item: T) => string, hold: boolean, resetKey = "", same: (a: T, b: T) => boolean = Object.is) {
  const sameList = (a: T[], b: T[]) => a.length === b.length && a.every((item, i) => same(item, b[i]))
  const [snap, setSnap] = useState(items)
  const [snapKey, setSnapKey] = useState(resetKey)
  if (resetKey !== snapKey) {
    setSnapKey(resetKey)
    setSnap(items)
    return { items, pending: false, refresh: () => {} }
  }
  if (!hold) {
    if (!sameList(snap, items)) setSnap(items)
    return { items, pending: false, refresh: () => {} }
  }
  const latest = new Map(items.map((item) => [key(item), item]))
  const shown = snap.flatMap((old) => { const now = latest.get(key(old)); return now === undefined ? [] : [now] })
  const pending = shown.length !== items.length || shown.some((row, i) => key(row) !== key(items[i]))
  return { items: shown, pending, refresh: () => setSnap(items) }
}
