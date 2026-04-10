import { useEffect, useState } from "react"

/**
 * Returns a counter that increments at the given interval, triggering re-renders.
 * Useful for forcing periodic UI updates (e.g. relative timestamps).
 * @param intervalMs - Tick interval in milliseconds (default: 1000).
 */
export function useTick(intervalMs = 1000): number {
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (intervalMs <= 0) return
    const id = setInterval(() => setTick((t) => t + 1), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return tick
}
