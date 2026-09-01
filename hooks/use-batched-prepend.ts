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
 *
 * `scopeKey` identifies the list the buffered entries belong to — pass the
 * same thing the list is keyed on (its query params, plus whatever pauses
 * the tail). When it changes, anything still queued is discarded rather than
 * flushed into a list it was not fetched for. Callers with a single fixed
 * scope can omit it.
 */
export function useBatchedPrepend(
  prependLive: (entries: JournalEntry | JournalEntry[]) => void,
  flushMs: number = PREPEND_FLUSH_MS,
  scopeKey?: unknown,
): (entry: JournalEntry) => void {
  const pendingRef = useRef<JournalEntry[]>([])
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const prependRef = useRef(prependLive)

  useEffect(() => {
    prependRef.current = prependLive
  }, [prependLive])

  // Drop the buffer whenever the list these entries belong to goes away —
  // on unmount, and on every change of `scopeKey`.
  //
  // The scope arm is not housekeeping. `onEntry` checks the caller's own
  // conditions (a paused live tail, a disabled tab) when an event ARRIVES,
  // and up to `flushMs` passes before the batch is handed over. In that
  // window the user can pause the tail or change a filter, and a queued
  // entry belonging to the previous query would be prepended to the list
  // that replaced it — a row the active filter says must not be there, which
  // no later refresh removes because prependLive does not re-check scope.
  useEffect(() => {
    return () => {
      if (timerRef.current) {
        clearTimeout(timerRef.current)
        timerRef.current = null
      }
      pendingRef.current = []
    }
  }, [scopeKey])

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
