"use client"

import Link from "next/link"
import { cn } from "@/lib/utils"

export type InboxKind = "escalation" | "keeper" | "review" | "proposal" | "mention"

export interface InboxEntry {
  id: string
  kind: InboxKind
  title: string
  subtitle?: string
  relative: string
  href?: string
}

interface InboxTileProps {
  entries: InboxEntry[]
  emptyLabel?: string
}

const KIND_META: Record<InboxKind, { icon: string; cls: string }> = {
  escalation: { icon: "!", cls: "bg-red-500/12 text-red-400" },
  keeper:     { icon: "🔑", cls: "bg-amber-500/12 text-amber-400" },
  review:     { icon: "✓", cls: "bg-blue-500/12 text-blue-400" },
  proposal:   { icon: "?", cls: "bg-blue-500/12 text-blue-400" },
  mention:    { icon: "@", cls: "bg-violet-500/12 text-violet-400" },
}

/** Consolidated urgent items list — the "what needs me" feed. */
export function InboxTile({ entries, emptyLabel = "Inbox empty — you're clear ✓" }: InboxTileProps) {
  if (entries.length === 0) {
    return (
      <div className="flex items-center justify-center h-[240px] text-[11px] text-muted-foreground/50">
        {emptyLabel}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-1.5 max-h-[300px] overflow-y-auto pr-1 -mr-1">
      {entries.map((e) => {
        const meta = KIND_META[e.kind]
        const content = (
          <div className="flex items-center gap-2.5 px-2.5 py-2 rounded-lg border border-border/60 bg-card hover:border-white/[0.12] transition-colors">
            <div className={cn("inline-flex items-center justify-center w-5 h-5 rounded text-[11px] font-semibold shrink-0", meta.cls)}>
              {meta.icon}
            </div>
            <div className="flex-1 min-w-0 leading-tight">
              <div className="text-[11px] font-medium text-foreground/90 truncate">{e.title}</div>
              {e.subtitle && (
                <div className="text-[10px] text-muted-foreground truncate">{e.subtitle}</div>
              )}
            </div>
            <div className="text-[10px] font-mono text-muted-foreground/50 shrink-0">{e.relative}</div>
          </div>
        )
        return e.href ? (
          <Link key={e.id} href={e.href} className="block">
            {content}
          </Link>
        ) : (
          <div key={e.id}>{content}</div>
        )
      })}
    </div>
  )
}
