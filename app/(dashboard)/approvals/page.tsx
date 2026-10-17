"use client"

import { Suspense, useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"

import { LEGACY_APPROVALS_DEFAULT, legacyApprovalsTarget } from "@/components/features/approvals/legacy-approvals-link"

/**
 * /approvals is a redirect, not a page. Approval requests are decided in the
 * Inbox — the same list, detail and Approve / Deny dock as every other request
 * that waits on a person — so a second approvals screen with its own layout
 * could only drift from it.
 *
 * The route stays because its URL is already out there: notification emails
 * and Slack "Review" buttons (internal/notifyroute/links.go), and
 * `crewship open approvals` (cmd/crewship/cmd_open.go). Never delete it.
 * Who may decide is unchanged: the Inbox loads the approvals feed only for the
 * admin tier, and POST /api/v1/approvals/{id}/decide stays roleManage.
 */
function Redirect() {
  const router = useRouter()
  const params = useSearchParams()
  const target = legacyApprovalsTarget(params)
  useEffect(() => {
    router.replace(target)
  }, [router, target])
  return null
}

export default function LegacyApprovalsRedirect() {
  return (
    <>
      <Suspense fallback={null}>
        <Redirect />
      </Suspense>
      {/* The static export prerenders this without a query string, so the
          no-JavaScript path always lands on the decisions waiting. */}
      <noscript>
        <meta httpEquiv="refresh" content={`0;url=${LEGACY_APPROVALS_DEFAULT}`} />
        <p>
          Approvals moved to the Inbox: <a href={LEGACY_APPROVALS_DEFAULT}>open the decisions waiting</a>.
        </p>
      </noscript>
    </>
  )
}
