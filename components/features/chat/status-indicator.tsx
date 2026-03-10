"use client"

import { useRef, useEffect } from "react"
import { SparklesIcon, type SparklesIconHandle } from "@/components/ui/sparkles"

interface StatusIndicatorProps {
  content: string
}

export function StatusIndicator({ content }: StatusIndicatorProps) {
  const iconRef = useRef<SparklesIconHandle>(null)

  useEffect(() => {
    const id = requestAnimationFrame(() => {
      iconRef.current?.startAnimation()
    })
    return () => cancelAnimationFrame(id)
  }, [])

  return (
    <div
      className="flex items-center gap-2 py-1 text-xs text-muted-foreground animate-in fade-in duration-300"
      role="status"
      aria-live="polite"
    >
      <SparklesIcon ref={iconRef} size={14} aria-hidden="true" />
      <span>{content}</span>
    </div>
  )
}
