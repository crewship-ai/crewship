"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

/**
 * /agents is a redirect, not a page. It has been one since the agents roster
 * moved under /crews, and it stays one because it still has a live caller:
 * `crewship open agents` builds <server>/agents (cmd/crewship/cmd_open.go),
 * documented in docs/cli/open.mdx. Deleting the route would turn that CLI
 * command into a fall-through to the SPA root — the dashboard, under a URL
 * that says agents — which is the exact failure this file used to cause.
 *
 * It aimed at /crews/agents, a route the selection-driven redesign deleted
 * along with the rest of the subtree: no page.tsx, no crews/agents.html in the
 * static export. The roster it was reaching for is the /crews canvas itself,
 * so that is where it goes. Plain /crews and not /crews?agent=<slug> — this
 * page has no agent in scope, and /crews reads ?agent= as a *slug*
 * (hooks/use-crews-selection.tsx), so inventing one here would only clear
 * itself and land the user on an empty canvas.
 */
export default function LegacyAgentsRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/crews")
  }, [router])
  return (
    <noscript>
      <meta httpEquiv="refresh" content="0;url=/crews" />
      <p>Redirecting to <a href="/crews">/crews</a>…</p>
    </noscript>
  )
}
