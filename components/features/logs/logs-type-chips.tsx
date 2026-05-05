"use client"

import { cn } from "@/lib/utils"
import { GROUP_COLOR, GROUP_LABEL, GROUP_ORDER, type EntryGroup } from "@/lib/journal-style"

interface LogsTypeChipsProps {
  /** Counts per group, derived from currently filtered entries (severity + search applied). */
  counts: Record<EntryGroup, number>
  /** Set of muted (clicked-off) groups. */
  muted: Set<EntryGroup>
  onToggle: (g: EntryGroup) => void
  onResetAll: () => void
}

export function LogsTypeChips({ counts, muted, onToggle, onResetAll }: LogsTypeChipsProps) {
  const visible = GROUP_ORDER.filter((g) => counts[g] > 0)
  if (visible.length === 0) return null

  const anyMuted = muted.size > 0

  return (
    <div className="px-3 py-1.5 border-b border-border/50 bg-card/40 flex flex-wrap items-center gap-1.5">
      <span className="text-[10px] uppercase tracking-wider text-muted-foreground mr-1">Types</span>
      {visible.map((g) => {
        const off = muted.has(g)
        const count = counts[g]
        return (
          <button
            key={g}
            type="button"
            onClick={() => onToggle(g)}
            className={cn(
              "inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border text-[10px] font-mono transition-colors",
              off
                ? "bg-card border-border/40 text-muted-foreground/50 line-through hover:text-muted-foreground"
                : "bg-sky-500/10 border-sky-500/30 text-sky-200 hover:bg-sky-500/20",
            )}
          >
            <span
              className="h-1.5 w-1.5 rounded-full inline-block"
              style={{ background: off ? "rgba(255,255,255,0.2)" : GROUP_COLOR[g] }}
            />
            <span>{GROUP_LABEL[g]}</span>
            <span className="opacity-60 tabular-nums">{count}</span>
          </button>
        )
      })}
      {anyMuted && (
        <button
          type="button"
          onClick={onResetAll}
          className="ml-auto text-[10px] text-muted-foreground hover:text-foreground"
        >
          Reset
        </button>
      )}
    </div>
  )
}
