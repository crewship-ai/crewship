"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

export default function LegacyAgentsRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/fleet/agents")
  }, [router])
  return (
    <>
      <meta httpEquiv="refresh" content="0;url=/fleet/agents" />
      <noscript>
        <p>Redirecting to <a href="/fleet/agents">/fleet/agents</a>…</p>
      </noscript>
    </>
  )
}
