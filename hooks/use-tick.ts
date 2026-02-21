import { useEffect, useState } from "react"

export function useTick(intervalMs = 1000): number {
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (intervalMs <= 0) return
    const id = setInterval(() => setTick((t) => t + 1), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return tick
}
