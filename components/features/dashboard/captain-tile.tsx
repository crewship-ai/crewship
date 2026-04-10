"use client"

import Link from "next/link"
import { Sparkles, ArrowRight } from "lucide-react"

const SUGGESTIONS = [
  "Why is QUA-1 taking so long?",
  "Summary of yesterday's failures",
  "What's the most expensive mission this week?",
  "Which agents are idle?",
]

/**
 * Captain prompt box — workspace AI assistant shortcut.
 * Clicking a suggestion (or the prompt box) navigates to /captain with
 * the question pre-filled via query param.
 */
export function CaptainTile() {
  return (
    <div className="flex flex-col gap-2">
      <Link
        href="/captain"
        className="flex items-center gap-2 px-3 py-2.5 rounded-lg border border-border/60 bg-card hover:border-violet-500/30 transition-colors"
      >
        <Sparkles className="h-3.5 w-3.5 text-violet-400 shrink-0" />
        <span className="text-[11px] text-muted-foreground flex-1">Ask anything about your fleet…</span>
        <kbd className="text-[9px] font-mono bg-white/[0.06] border border-border rounded px-1 py-0.5 text-muted-foreground/70">⌘/</kbd>
      </Link>
      <div className="flex flex-col gap-1">
        {SUGGESTIONS.map((q) => (
          <Link
            key={q}
            href={`/captain?q=${encodeURIComponent(q)}`}
            className="group flex items-center gap-1.5 px-2.5 py-1.5 rounded-md border border-dashed border-border hover:border-white/[0.15] hover:bg-white/[0.02] transition-colors"
          >
            <span className="text-[10px] text-muted-foreground/70 group-hover:text-foreground/80 flex-1 truncate">{q}</span>
            <ArrowRight className="h-3 w-3 text-muted-foreground/30 group-hover:text-violet-400 shrink-0" />
          </Link>
        ))}
      </div>
    </div>
  )
}
