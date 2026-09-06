"use client"

import { useEffect, useRef, type ReactNode } from "react"
import { cn } from "@/lib/utils"

/** Shared reading surface: one document, with supporting sections separated by rules. */
export function InboxMessageSurface({ children, className }: { children: ReactNode; className?: string }) {
  const ref = useRef<HTMLElement>(null)
  useEffect(() => {
    const element = ref.current
    if (!element) return
    // Markdown can finish rendering after mount. Keep wide code/table regions
    // reachable with a keyboard without moving focus while content arrives.
    const focusable = () => element.querySelectorAll("pre, table").forEach((node) => node.setAttribute("tabindex", "0"))
    focusable()
    const observer = new MutationObserver(focusable)
    observer.observe(element, { childList: true, subtree: true })
    return () => observer.disconnect()
  }, [])
  return <article ref={ref} className={cn("min-w-0 rounded-xl border border-border/60 bg-card [overflow-wrap:anywhere] motion-safe:animate-in motion-safe:fade-in motion-safe:duration-150 [&_button]:max-w-full [&_textarea]:max-w-full [&_pre]:max-w-full [&_table]:block [&_table]:overflow-x-auto", className)}>{children}</article>
}

export const messageSection = "min-w-0 px-4 py-4 sm:px-6 sm:py-5"
export const messageDivider = "border-t border-border/60"
