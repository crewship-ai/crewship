"use client"

import { useEffect } from "react"

/**
 * Asks before leaving only while leaving would lose work the server does not
 * hold. Reload and tab close get the browser-drawn prompt; in-app navigation is
 * the host's (the Pages editor route asks through `onDirtyChange`), which is
 * told the same answer and told false on unmount. The prompt cannot save
 * anything and is never described as if it could.
 */
export function useLeaveGuard(losesWork: boolean, onDirtyChange?: (dirty: boolean) => void): void {
  useEffect(() => {
    if (!losesWork) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = "" }
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [losesWork])
  useEffect(() => {
    onDirtyChange?.(losesWork)
    return () => onDirtyChange?.(false)
  }, [losesWork, onDirtyChange])
}
