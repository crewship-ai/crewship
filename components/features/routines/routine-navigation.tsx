"use client"

import Link from "next/link"
import { cn } from "@/lib/utils"

export const ROUTINE_VIEWS = ["definition", "history", "versions", "plan"] as const
export type RoutineView = typeof ROUTINE_VIEWS[number]
const labels: Record<RoutineView, string> = { definition: "Definition", history: "History", versions: "Versions", plan: "Plan" }
export const routineViewHref = (slug: string, view: RoutineView) => `/routines?${new URLSearchParams({ slug, ...(view === "definition" ? {} : { view }) })}`
const tabClass = (active: boolean) => cn("rounded-full px-3 py-1.5 text-xs transition-colors", active ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:bg-muted/60 hover:text-foreground")
export function RoutineNavigation({ slug, view, onChange }: { slug: string; view: string; onChange?: (view: RoutineView) => void }) {
  return <nav aria-label="Routine detail" className="flex flex-wrap gap-2 border-b border-border pb-2">{ROUTINE_VIEWS.map(v => onChange ? <button key={v} className={tabClass(view === v)} onClick={() => onChange(v)} aria-pressed={view === v}>{labels[v]}</button> : <Link key={v} className={tabClass(view === v)} href={routineViewHref(slug, v)} aria-current={view === v ? "page" : undefined}>{labels[v]}</Link>)}</nav>
}
export function RoutineSubNavigation({ items, value, onChange, label }: { items: readonly string[]; value: string; onChange: (value: string) => void; label: string }) {
  return <nav aria-label={label} className="flex flex-wrap gap-2 border-b border-border pb-2">{items.map(t => <button key={t} className={tabClass(value === t)} onClick={() => onChange(t)} aria-pressed={value === t}>{t[0].toUpperCase() + t.slice(1)}</button>)}</nav>
}
