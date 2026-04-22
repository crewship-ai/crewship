"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

export default function LegacyFleetRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/crews")
  }, [router])
  return (
    <>
      <meta httpEquiv="refresh" content="0;url=/crews" />
      <noscript>
        <p>Redirecting to <a href="/crews">/crews</a>…</p>
      </noscript>
    </>
  )
}
