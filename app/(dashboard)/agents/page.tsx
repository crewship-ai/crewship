"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

export default function LegacyAgentsRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/cruise/agents")
  }, [router])
  return (
    <>
      <meta httpEquiv="refresh" content="0;url=/cruise/agents" />
      <noscript>
        <p>Redirecting to <a href="/cruise/agents">/cruise/agents</a>…</p>
      </noscript>
    </>
  )
}
