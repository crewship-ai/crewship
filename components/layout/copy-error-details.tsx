"use client"

import * as React from "react"

/**
 * What "Copy details" puts on the clipboard for a crashed page: the error id,
 * the page and the time — never the error's message or stack, which can carry
 * internal paths and data the person did not mean to paste into a ticket.
 */
export function errorDetails(digest: string | undefined, now: Date = new Date()): string {
  const where = typeof window === "undefined" ? "" : window.location.pathname
  return [
    `Error ID: ${digest ?? "none"}`,
    where && `Page: ${where}`,
    `Time: ${now.toISOString()}`,
  ].filter(Boolean).join("\n")
}

/** The footer's quiet "Copy details" link; says "Copied" for a moment after. */
export function CopyErrorDetails({ digest }: { digest?: string }) {
  const [copied, setCopied] = React.useState(false)
  React.useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 2000)
    return () => clearTimeout(t)
  }, [copied])
  return (
    <button
      type="button"
      className="page-link-btn"
      onClick={() => {
        void navigator.clipboard?.writeText(errorDetails(digest)).then(() => setCopied(true), () => {})
      }}
    >
      {copied ? "Copied" : "Copy details"}
    </button>
  )
}
