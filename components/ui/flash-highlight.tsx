"use client"

import { useEffect, useRef, useState } from "react"
import { cn } from "@/lib/utils"

interface FlashHighlightProps {
  children: React.ReactNode
  /** Any value -- when it changes the highlight triggers. */
  trigger: unknown
  /** Duration of flash in ms. */
  duration?: number
  className?: string
  /** Tailwind class applied during flash (default: ring + bg tint). */
  flashClassName?: string
}

export function FlashHighlight({
  children,
  trigger,
  duration = 1500,
  className,
  flashClassName = "bg-primary/5 ring-1 ring-primary/20",
}: FlashHighlightProps) {
  const [flash, setFlash] = useState(false)
  const prevRef = useRef(trigger)
  const mountedRef = useRef(false)

  useEffect(() => {
    if (!mountedRef.current) {
      mountedRef.current = true
      prevRef.current = trigger
      return
    }
    if (prevRef.current !== trigger) {
      prevRef.current = trigger
      setFlash(true)
      const timer = setTimeout(() => setFlash(false), duration)
      return () => clearTimeout(timer)
    }
  }, [trigger, duration])

  return (
    <div
      className={cn(
        "transition-all rounded-md",
        flash ? `${flashClassName} duration-150` : "duration-700",
        className,
      )}
    >
      {children}
    </div>
  )
}
