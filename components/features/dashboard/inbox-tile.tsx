"use client"

import Link from "next/link"
import { AlertTriangle, KeyRound, CheckCircle2, FileText, AtSign, type LucideIcon } from "lucide-react"
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

const KIND_META: Record<InboxKind, { Icon: LucideIcon; cls: string }> = {
  escalation: { Icon: AlertTriangle, cls: "bg-red-500/12 text-red-400" },
  keeper:     { Icon: KeyRound,      cls: "bg-amber-500/12 text-amber-400" },
  review:     { Icon: CheckCircle2,  cls: "bg-blue-500/12 text-blue-400" },
  proposal:   { Icon: FileText,      cls: "bg-blue-500/12 text-blue-400" },
  mention:    { Icon: AtSign,        cls: "bg-violet-500/12 text-violet-400" },
}

/** Consolidated urgent items list — the "what needs me" feed. */
export function InboxTile({ entries, emptyLabel = "Inbox empty — you're clear ✓" }: InboxTileProps) {
  if (entries.length === 0) {
    return (
      <div className="flex items-center justify-center h-[240px] text-[11px] text-muted-foreground-soft">
        {emptyLabel}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-2 max-h-[320px] overflow-y-auto pr-1 -mr-1">
      {entries.map((e) => {
        const meta = KIND_META[e.kind]
        const Icon = meta.Icon
        const content = (
          <div className="flex items-center gap-3 px-3 py-2.5 rounded-lg border border-border/60 bg-card hover:border-white/[0.12] transition-colors">
            <div className={cn("inline-flex items-center justify-center w-6 h-6 rounded shrink-0", meta.cls)}>
              <Icon className="h-3 w-3" />
            </div>
            <div className="flex-1 min-w-0 leading-tight">
              <div className="text-[12px] font-medium text-foreground/90 truncate">{e.title}</div>
              {e.subtitle && (
                <div className="text-[10px] text-muted-foreground truncate mt-0.5">{e.subtitle}</div>
              )}
            </div>
            <div className="text-[10px] font-mono text-muted-foreground-soft shrink-0">{e.relative}</div>
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
