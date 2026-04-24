"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

export default function LegacyAgentsRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/crews/agents")
  }, [router])
  return (
    <noscript>
      <meta httpEquiv="refresh" content="0;url=/crews/agents" />
      <p>Redirecting to <a href="/crews/agents">/crews/agents</a>…</p>
    </noscript>
  )
}
