"use client"

import { Tag } from "lucide-react"

// RunTagChips — renders a run's tags (set at run-create time, persisted
// in the run_tags join table) as small chips. Only the single-run
// detail endpoint (GetRun) returns tags — list endpoints omit them —
// so this is fed from a run-detail fetch, not the runs list row.
// Renders nothing when there are no tags.
export function RunTagChips({ tags, className }: { tags?: string[] | null; className?: string }) {
  const clean = (tags ?? []).filter((t): t is string => typeof t === "string" && t.trim().length > 0)
  if (clean.length === 0) return null
  return (
    <div className={className ? `flex flex-wrap items-center gap-1 ${className}` : "flex flex-wrap items-center gap-1"}>
      {clean.map((t) => (
        <span
          key={t}
          className="inline-flex items-center gap-1 rounded-full border border-border/60 bg-white/[0.04] px-1.5 py-0 text-[10px] font-medium text-foreground/80"
          title={`Run tag: ${t}`}
        >
          <Tag className="h-2.5 w-2.5 text-muted-foreground" aria-hidden />
          {t}
        </span>
      ))}
    </div>
  )
}
