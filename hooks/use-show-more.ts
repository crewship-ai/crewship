"use client"

import { useCallback, useMemo, useState } from "react"

/**
 * Paging for a list the browser already holds whole (docs/ux/README.md §4:
 * priority, cap, fold). The nested Settings and Admin pages read endpoints
 * that return every row; this keeps a list of hundreds to one step on screen
 * and grows it on request. A list the server pages uses `usePagedList`.
 */
export interface ShowMoreState<T> {
  shown: T[]
  total: number
  hasMore: boolean
  /** How many the next step adds: the label of the button. */
  nextCount: number
  /** True once the list was longer than one step, so the footer stays. */
  paged: boolean
  showMore: () => void
}

export function useShowMore<T>(items: readonly T[], { step = 25, resetKey }: { step?: number; resetKey?: unknown } = {}): ShowMoreState<T> {
  const [state, setState] = useState<{ key: unknown; count: number }>({ key: resetKey, count: step })
  // A new filter starts from the top: derived during render, so the first
  // paint after the change is already the short list.
  const count = Object.is(state.key, resetKey) ? state.count : step
  const showMore = useCallback(() => {
    setState((prev) => ({ key: resetKey, count: (Object.is(prev.key, resetKey) ? prev.count : step) + step }))
  }, [resetKey, step])
  return useMemo(() => {
    const total = items.length
    const visible = Math.min(count, total)
    return {
      shown: items.slice(0, visible),
      total,
      hasMore: visible < total,
      nextCount: Math.min(step, total - visible),
      paged: total > step,
      showMore,
    }
  }, [items, count, step, showMore])
}
