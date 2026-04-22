"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

export default function LegacyCrewsRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/fleet/crews")
  }, [router])
  return (
    <>
      <meta httpEquiv="refresh" content="0;url=/fleet/crews" />
      <noscript>
        <p>Redirecting to <a href="/fleet/crews">/fleet/crews</a>…</p>
      </noscript>
    </>
  )
}
