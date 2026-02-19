"use client"

import { useEffect, useState, useCallback } from "react"
import { AlertTriangle, X } from "lucide-react"
import Link from "next/link"

export function RuntimeBanner() {
  const [visible, setVisible] = useState(false)
  const [dismissed, setDismissed] = useState(false)

  const check = useCallback(async () => {
    try {
      const res = await fetch("/api/v1/system/runtime")
      if (!res.ok) return
      const data = await res.json()
      setVisible(!data.available)
      if (data.available) setDismissed(false)
    } catch {
      // silently ignore
    }
  }, [])

  useEffect(() => {
    check()
    const interval = setInterval(check, 30_000)
    const onFocus = () => check()
    window.addEventListener("focus", onFocus)
    return () => {
      clearInterval(interval)
      window.removeEventListener("focus", onFocus)
    }
  }, [check])

  if (!visible || dismissed) return null

  return (
    <div className="flex items-center gap-2 bg-amber-50 border-b border-amber-200 px-4 py-2 text-xs">
      <AlertTriangle className="h-3.5 w-3.5 text-amber-600 shrink-0" />
      <span className="text-amber-800">
        No container runtime detected. Agents cannot run.{" "}
        <a
          href="https://docs.docker.com/get-docker/"
          target="_blank"
          rel="noopener noreferrer"
          className="underline font-medium"
        >
          Install Docker
        </a>
        {" | "}
        <Link href="/admin" className="underline font-medium">
          Admin Settings
        </Link>
      </span>
      <button
        onClick={() => setDismissed(true)}
        className="ml-auto text-amber-600 hover:text-amber-800"
        aria-label="Dismiss"
      >
        <X className="h-3.5 w-3.5" />
      </button>
    </div>
  )
}
