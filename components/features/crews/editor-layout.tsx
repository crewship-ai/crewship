"use client"

import { useLayoutEffect, useRef, type ReactNode } from "react"
import type { LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"

export interface EditorSection { id: string; label: string; icon: LucideIcon }

/** Persistent section navigation; the form owns drafts across section changes. */
export function EditorLayout({ label, sections, active, onChange, children, hidden = false }: {
  label: string; sections: EditorSection[]; active: string; onChange: (id: string) => void; children: ReactNode; hidden?: boolean
}) {
  const root = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => { const body = root.current?.querySelector('[data-slot="create-surface-body"]'); if (body) body.scrollTop = 0 }, [active])
  return <div ref={root} className="flex min-h-0 flex-1 flex-col sm:flex-row">
    {!hidden && <>
      <nav aria-label={label} className="hidden w-48 shrink-0 space-y-1 overflow-y-auto border-r border-border/60 p-3 sm:block">
        {sections.map(({ id, label: title, icon: Icon }) => <button key={id} type="button" aria-current={active === id ? "page" : undefined} onClick={() => onChange(id)} className={cn("flex w-full items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary", active === id ? "bg-primary/10 text-primary" : "text-muted-foreground hover:bg-muted hover:text-foreground")}><Icon className="h-4 w-4 shrink-0" aria-hidden="true" />{title}</button>)}
      </nav>
      <label className="flex shrink-0 items-center gap-3 border-b border-border/60 px-4 py-2 sm:hidden"><span className="text-xs text-muted-foreground">Section</span><select aria-label={label} value={active} onChange={event => onChange(event.target.value)} className="min-h-11 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-sm">{sections.map(section => <option key={section.id} value={section.id}>{section.label}</option>)}</select></label>
    </>}
    {children}
  </div>
}

export function EditorPanel({ active, children }: { active: boolean; children: ReactNode }) {
  return <div hidden={!active} className="space-y-5 [&>section]:rounded-xl [&>section]:border [&>section]:border-border/60 [&>section]:bg-card [&>section]:p-4">{children}</div>
}
