"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

/**
 * A retired route that keeps old bookmarks working. The static export has no
 * server, so a `redirect()` inside (dashboard) never runs (CHR-05): the
 * client replaces the URL, and a browser without JavaScript follows the
 * <noscript> meta refresh. Same pattern as /audit, /agents and /approvals.
 */
export function LegacyRedirect({ target, children }: { target: string; children: React.ReactNode }) {
  const router = useRouter()
  useEffect(() => {
    router.replace(target)
  }, [router, target])
  return (
    <noscript>
      <meta httpEquiv="refresh" content={`0;url=${target}`} />
      <p>{children}</p>
    </noscript>
  )
}
