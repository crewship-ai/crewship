"use client"

import Link from "next/link"
import { cn } from "@/lib/utils"

export const ROUTINE_VIEWS = ["definition", "history", "versions", "plan"] as const
export type RoutineView = typeof ROUTINE_VIEWS[number]
const labels: Record<RoutineView, string> = { definition: "Overview", history: "History", versions: "Versions", plan: "Schedule" }
export const routineViewHref = (slug: string, view: RoutineView) => `/routines?${new URLSearchParams({ slug, ...(view === "definition" ? {} : { view }) })}`
const tabClass = (active: boolean) => cn("rounded-full px-3 py-1.5 text-xs transition-colors", active ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:bg-muted/60 hover:text-foreground")
export function RoutineNavigation({ slug, view, onChange, runId }: { slug: string; view: string; onChange?: (view: RoutineView) => void; runId?: string }) {
  const items = ROUTINE_VIEWS.flatMap(v => v === "definition" && runId ? [v, "run"] : [v])
  return <nav aria-label="Routine detail" className="flex flex-wrap gap-2 border-b border-border pb-2">{items.map(v => v === "run"
    ? <Link key={v} className={tabClass(view === v)} href={`/routines?${new URLSearchParams({ slug, run: runId! })}`} aria-current={view === v ? "page" : undefined}>Run</Link>
    : onChange ? <button key={v} className={tabClass(view === v)} onClick={() => onChange(v as RoutineView)} aria-pressed={view === v}>{labels[v as RoutineView]}</button> : <Link key={v} className={tabClass(view === v)} href={routineViewHref(slug, v as RoutineView)} aria-current={view === v ? "page" : undefined}>{labels[v as RoutineView]}</Link>)}</nav>
}
export function RoutineSubNavigation({ items, value, onChange, label }: { items: readonly string[]; value: string; onChange: (value: string) => void; label: string }) {
  return <nav aria-label={label} className="flex flex-wrap gap-2 border-b border-border pb-2">{items.map(t => <button key={t} className={tabClass(value === t)} onClick={() => onChange(t)} aria-pressed={value === t}>{t[0].toUpperCase() + t.slice(1)}</button>)}</nav>
}
