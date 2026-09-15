"use client"

import { useEffect, useRef, useState } from "react"

/** Do not mount a new workspace's layout until old entity links are cleared. */
export function useWorkspaceURLReady(workspaceId: string | null, loading: boolean) {
  const previous = useRef<string | null>(null)
  const [ready, setReady] = useState<string | null>(null)
  useEffect(() => {
    if (loading || !workspaceId) return
    if (previous.current && previous.current !== workspaceId) {
      const url = new URL(window.location.href)
      for (const key of ["section", "target", "server"]) url.searchParams.delete(key)
      window.history.replaceState(window.history.state, "", url)
    }
    previous.current = workspaceId
    setReady(workspaceId)
  }, [workspaceId, loading])
  return ready === workspaceId
}
