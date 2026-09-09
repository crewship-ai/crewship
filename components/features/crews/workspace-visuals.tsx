"use client"

import type { LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"

export function WorkspaceGlyph({ icon: Icon, tone = "blue", className }: { icon: LucideIcon; tone?: "blue" | "purple" | "green" | "amber"; className?: string }) {
  return <span className={cn("inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl", { blue: "bg-primary/10 text-primary", purple: "bg-purple/10 text-purple", green: "bg-success/10 text-success", amber: "bg-warn/10 text-warn" }[tone], className)}><Icon className="h-4 w-4" aria-hidden="true" /></span>
}

export function WorkspaceEmpty({ icon: Icon, title, description }: { icon: LucideIcon; title: string; description?: string }) {
  return <div className="flex flex-col items-center justify-center py-4 px-3 text-center"><span className="relative mb-2 rounded-2xl border border-border/60 bg-muted/30 p-3"><Icon className="h-6 w-6 text-muted-foreground" aria-hidden="true" /></span><p className="text-sm text-muted-foreground">{title}</p>{description && <p className="mt-1 max-w-sm text-xs text-muted-foreground">{description}</p>}</div>
}
