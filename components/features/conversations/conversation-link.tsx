"use client"

import { useState, type ReactNode } from "react"
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog"

/** Only root-relative paths are trusted app navigation. Backslashes and control
 * characters have special URL parsing rules and must never bypass confirmation. */
export function isConversationAppLink(href: string): boolean {
  return href.startsWith("/") && !href.startsWith("//") && !/[\\\u0000-\u0020\u007f]/.test(href)
}

export function ConversationLink({ href, children }: { href?: string; children?: ReactNode }) {
  const [open, setOpen] = useState(false)
  if (!href) return <span>{children}</span>
  if (isConversationAppLink(href)) return <a href={href} className="break-words font-medium text-primary underline" data-streamdown="link">{children}</a>
  // Streamdown already sanitizes href; keep this component safe on its own,
  // including when a caller passes an untrusted scheme directly.
  let destination: URL
  try { destination = new URL(href, "https://crewship.invalid") } catch { return <span>{children}</span> }
  if (!["https:", "http:", "mailto:"].includes(destination.protocol)) return <span>{children}</span>
  return <><button type="button" className="break-words text-left font-medium text-primary underline" data-streamdown="link" onClick={() => setOpen(true)}>{children}</button><AlertDialog open={open} onOpenChange={setOpen}><AlertDialogContent><AlertDialogHeader><AlertDialogTitle>Open external link?</AlertDialogTitle><AlertDialogDescription className="break-all">This link leaves the conversation. Check its destination before opening: {href}</AlertDialogDescription></AlertDialogHeader><AlertDialogFooter><AlertDialogCancel>Cancel</AlertDialogCancel><AlertDialogAction onClick={() => window.open(href, "_blank", "noopener,noreferrer")}>Open link</AlertDialogAction></AlertDialogFooter></AlertDialogContent></AlertDialog></>
}
