"use client"

import { useCallback, useEffect, useRef } from "react"
import type { JournalEntry } from "@/lib/types/journal"

/**
 * Flush window for buffered live entries. Long enough that a burst collapses
 * into one render, short enough that the tail still reads as live.
 */
export const PREPEND_FLUSH_MS = 250

/**
 * Wrap a `useJournalList` prependLive in a coalescing buffer: entries that
 * arrive within one flush window are handed over as a single array, so a
 * burst costs one pass over the buffer and one render instead of one of each
 * per event.
 *
 * Every SSE consumer wants this. The journal page grew its own copy;
 * ResourcesStrip did not, and it subscribes to `container.metrics` — the
 * highest-volume entry type in the product — so it re-rendered a recharts
 * area chart per sample. Shared here so the next subscriber inherits the
 * behaviour rather than the bug.
 *
 * Returns a stable callback safe to pass straight to `useJournalStream`'s
 * `onEntry`; `prependLive` is read through a ref so a caller that rebuilds
 * it every render does not reset the buffer.
 */
export function useBatchedPrepend(
  prependLive: (entries: JournalEntry | JournalEntry[]) => void,
  flushMs: number = PREPEND_FLUSH_MS,
): (entry: JournalEntry) => void {
  const pendingRef = useRef<JournalEntry[]>([])
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const prependRef = useRef(prependLive)

  useEffect(() => {
    prependRef.current = prependLive
  }, [prependLive])

  useEffect(() => {
    return () => {
      if (timerRef.current) {
        clearTimeout(timerRef.current)
        timerRef.current = null
      }
      // Whatever is still buffered belongs to a list that is going away.
      pendingRef.current = []
    }
  }, [])

  return useCallback(
    (entry: JournalEntry) => {
      pendingRef.current.push(entry)
      if (timerRef.current) return
      timerRef.current = setTimeout(() => {
        timerRef.current = null
        const batch = pendingRef.current
        if (batch.length === 0) return
        pendingRef.current = []
        prependRef.current(batch)
      }, flushMs)
    },
    [flushMs],
  )
}
