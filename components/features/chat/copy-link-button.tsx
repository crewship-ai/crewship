"use client"

import { useState } from "react"
import { Check, Link2 } from "lucide-react"

/** The session id, in the one form a person can use: the address.
 *
 *  On phones it is an icon button like its
 *  neighbours (the coarse-pointer 44px minimum turned the pill into a ring
 *  with the icon off-centre); from sm up it is a labelled pill. The
 *  accessible name says "Link copied" because phones hide the label text. */
export function CopyLinkButton() {
  const [copied, setCopied] = useState(false)
  const label = copied ? "Link copied" : "Copy link to this conversation"
  const Icon = copied ? Check : Link2
  return (
    <button
      type="button"
      onClick={() => {
        navigator.clipboard?.writeText(window.location.href).then(() => {
          setCopied(true)
          window.setTimeout(() => setCopied(false), 1500)
        }).catch(() => {})
      }}
      aria-label={label}
      title={label}
      className="inline-flex h-8 min-w-8 items-center justify-center gap-1.5 rounded-md text-micro text-muted-foreground transition-colors hover:text-foreground sm:h-auto sm:rounded-full sm:border sm:border-border sm:px-2 sm:py-0.5"
    >
      <Icon className="h-4 w-4 sm:h-3 sm:w-3" aria-hidden="true" />
      <span className="hidden sm:inline">{copied ? "Copied" : "Copy link"}</span>
    </button>
  )
}
