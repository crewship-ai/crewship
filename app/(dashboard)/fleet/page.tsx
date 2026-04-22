"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

export default function LegacyFleetRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/cruise")
  }, [router])
  return (
    <>
      <meta httpEquiv="refresh" content="0;url=/cruise" />
      <noscript>
        <p>Redirecting to <a href="/cruise">/cruise</a>…</p>
      </noscript>
    </>
  )
}
