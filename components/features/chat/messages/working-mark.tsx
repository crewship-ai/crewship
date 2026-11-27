import { cn } from "@/lib/utils"

/**
 * The calm "working" graphic: three dots that fade in turn, slowly. Used once
 * per assistant turn (its activity line) and while the workspace is prepared.
 * With reduced motion the dots hold still and stay visible, so the reader still
 * sees that the agent has not finished.
 */
export function WorkingMark({ active = true, className }: { active?: boolean; className?: string }) {
  return (
    <span
      aria-hidden="true"
      data-testid="working-mark"
      data-active={active ? "true" : "false"}
      className={cn("inline-flex shrink-0 items-center gap-0.5", className)}
    >
      {[0, 1, 2].map((i) => (
        <span
          key={i}
          className={cn("size-1 rounded-full bg-muted-foreground/70", active && "animate-pulse motion-reduce:animate-none")}
          style={active ? { animationDelay: `${i * 240}ms`, animationDuration: "1.8s" } : undefined}
        />
      ))}
    </span>
  )
}
