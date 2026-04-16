"use client"

/**
 * @deprecated Captain UI is no longer actively developed (2026-04-16).
 * The floating compass bubble opens the deprecated Captain chat panel.
 * See captain-panel.tsx for deprecation details and
 * docs/guides/captain.mdx for migration notes.
 * Component retained for backward compatibility.
 */

import { useEffect } from "react"
import { Compass } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useCaptainStore } from "@/hooks/use-captain-store"
import { cn } from "@/lib/utils"

/** @deprecated See module-level deprecation notice. */
export function CaptainBubble() {
  const { isOpen, toggle, badgeCount } = useCaptainStore()

  // Keyboard shortcut: Cmd+. / Ctrl+.
  useEffect(() => {
    function handleKeydown(e: KeyboardEvent) {
      if (e.key === "." && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        toggle()
      }
    }
    document.addEventListener("keydown", handleKeydown)
    return () => document.removeEventListener("keydown", handleKeydown)
  }, [toggle])

  return (
    <Button
      onClick={toggle}
      size="icon"
      variant="default"
      className={cn(
        "fixed bottom-6 right-6 z-50 h-12 w-12 rounded-full shadow-lg",
        "transition-transform duration-200 hover:scale-110",
        isOpen && "ring-2 ring-primary ring-offset-2"
      )}
      aria-label="Toggle Captain assistant"
    >
      <Compass className="h-6 w-6" />
      {badgeCount > 0 && (
        <span className="absolute -top-1 -right-1 flex h-5 min-w-5 items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-bold text-destructive-foreground">
          {badgeCount > 99 ? "99+" : badgeCount}
        </span>
      )}
    </Button>
  )
}
