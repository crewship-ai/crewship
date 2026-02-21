"use client"

import { Loader2 } from "lucide-react"

interface StatusIndicatorProps {
  content: string
}

export function StatusIndicator({ content }: StatusIndicatorProps) {
  return (
    <div className="flex items-center gap-2 py-1 text-xs text-muted-foreground animate-in fade-in duration-300">
      <Loader2 className="h-3 w-3 animate-spin" />
      <span>{content}</span>
    </div>
  )
}
