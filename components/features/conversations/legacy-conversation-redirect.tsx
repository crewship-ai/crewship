"use client"

import { useEffect } from "react"

export function LegacyConversationRedirect() {
  useEffect(() => {
    const url = new URL(window.location.href)
    url.pathname = "/chat"
    window.location.replace(url.pathname + url.search + url.hash)
  }, [])
  return <p className="p-6 text-sm">Opening Chat…</p>
}
