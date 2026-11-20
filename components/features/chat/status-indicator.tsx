"use client"

interface StatusIndicatorProps {
  content: string
}

/**
 * The turn's current step ("Starting agent…", "Container ready"), as quiet
 * text. The agent's face beside the turn is already turning; this line used to
 * animate a sparkle of its own, a second working signal on the same turn.
 */
export function StatusIndicator({ content }: StatusIndicatorProps) {
  return (
    <div
      className="flex items-center gap-2 py-1 text-label text-muted-foreground animate-in fade-in duration-300 motion-reduce:animate-none"
      role="status"
      aria-live="polite"
    >
      <span>{content}</span>
    </div>
  )
}
