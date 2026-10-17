"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

const TARGET = "/settings/audit"

/**
 * /audit is a redirect, not a page: the Audit Log moved to Settings (admin
 * compliance), now its own nested page. Old bookmarks and links posted in
 * Slack, docs or dashboards land here and keep resolving.
 *
 * It was a server `redirect()` inside (dashboard). The static export has no
 * server, and the dashboard layout prerenders only its loading frame until
 * the session resolves, so audit.html carried neither the redirect nor a
 * meta refresh (CHR-05, inventory Z02). Like /approvals and /agents it lives
 * in the layout-less (legacy) group: the client replaces the URL, and a
 * browser without JavaScript follows the <noscript> meta refresh.
 */
export default function LegacyAuditRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace(TARGET)
  }, [router])
  return (
    <noscript>
      <meta httpEquiv="refresh" content={`0;url=${TARGET}`} />
      <p>
        The Audit Log moved to Settings: <a href={TARGET}>open the Audit Log</a>.
      </p>
    </noscript>
  )
}
