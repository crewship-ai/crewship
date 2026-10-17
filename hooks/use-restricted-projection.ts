"use client"

import { useRef, useState } from "react"

// Protected views come from matching live reads, never cached success alone.
export function useRestrictedProjection<T>(scope: string) {
  const [held, setHeld] = useState<{ scope: string; value: T | null }>({ scope, value: null })
  const sequence = useRef(0)
  const lease = useRef({ scope: "", verified: false })
  // A scope switch must not resurrect a held snapshot when returning to a
  // cached key before its new read. React discards this stale render.
  if (held.scope !== scope) setHeld({ scope, value: null })
  return {
    value: held.scope === scope ? held.value : null,
    read(signal: AbortSignal) {
      const epoch = ++sequence.current
      const current = () => !signal.aborted && epoch === sequence.current
      return {
        accept(value: T) {
          if (!current()) return
          lease.current = { scope, verified: true }
          setHeld({ scope, value })
        },
        reject(status: number) {
          if (!current() || ![401, 403, 404].includes(status)) return
          lease.current = { scope, verified: false }
          setHeld({ scope, value: null })
        },
      }
    },
    captureSave() {
      const started = lease.current
      return (value: T) => {
        if (!started.verified || started.scope !== scope || started !== lease.current) return false
        setHeld({ scope, value })
        return true
      }
    },
  }
}
