"use client"

import { useState } from "react"

const sameElements = <T,>(a: T[], b: T[]) => a.length === b.length && a.every((item, i) => item === b[i])

/**
 * Keeps a list's rows where they are while `hold` is true (pointer or focus is
 * inside it). Live updates still reach rows already shown (new unread count,
 * new title), but rows are neither added, removed nor reordered under the
 * pointer; `pending` says the real order differs and `refresh` applies it.
 * Releasing the hold applies it on its own.
 */
export function useStableOrder<T>(items: T[], key: (item: T) => string, hold: boolean) {
  const [snap, setSnap] = useState(items)
  if (!hold && !sameElements(snap, items)) setSnap(items)
  if (!hold) return { items, pending: false, refresh: () => {} }
  const latest = new Map(items.map((item) => [key(item), item]))
  const shown = snap.map((old) => latest.get(key(old)) ?? old)
  const pending = snap.length !== items.length || snap.some((old, i) => key(old) !== key(items[i]))
  return { items: shown, pending, refresh: () => setSnap(items) }
}
