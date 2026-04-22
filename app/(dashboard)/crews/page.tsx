"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

export default function LegacyCrewsRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/cruise/crews")
  }, [router])
  return (
    <>
      <meta httpEquiv="refresh" content="0;url=/cruise/crews" />
      <noscript>
        <p>Redirecting to <a href="/cruise/crews">/cruise/crews</a>…</p>
      </noscript>
    </>
  )
}
