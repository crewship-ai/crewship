"use client"

import { useState } from "react"
import { ArrowDownToLine, Braces, Hash, List, Type, ToggleLeft } from "lucide-react"
import { DetailCard } from "@/components/ui/detail"
import { routineInputSpecs } from "@/lib/routine-inputs"

export const readableFieldName = (name: string) => name.replace(/[_-]+/g, " ").replace(/^./, c => c.toUpperCase())
const valueKind = (value: unknown) => Array.isArray(value) ? { label: "List", icon: List } : value === null ? { label: "Null", icon: Braces } : typeof value === "object" ? { label: "Fields", icon: Braces } : typeof value === "boolean" ? { label: "Yes / no", icon: ToggleLeft } : typeof value === "number" ? { label: "Number", icon: Hash } : { label: "Text", icon: Type }

/** Display saved values verbatim; do not parse strings or apply today's defaults. */
export function RoutineRecordedValue({ value, depth = 0 }: { value: unknown; depth?: number }) {
  const [all, setAll] = useState(false)
  if (value === null) return <span className="text-sm text-muted-foreground">No value (null)</span>
  if (typeof value === "boolean") return <span className="inline-flex rounded-full border border-border bg-muted px-3 py-1 text-sm font-medium">{value ? "Yes" : "No"}</span>
  if (typeof value === "number") return <span className="text-sm tabular-nums">{String(value)}</span>
  if (typeof value === "string") return value === "" ? <span className="text-sm italic text-muted-foreground">Empty text</span> : <p className="max-h-80 overflow-auto whitespace-pre-wrap text-sm leading-7 [overflow-wrap:anywhere]">{value}</p>
  if (value === undefined) return <span className="text-sm text-muted-foreground">Not supplied</span>
  const entries = Object.entries(value as object)
  if (!entries.length) return <span className="text-sm italic text-muted-foreground">{Array.isArray(value) ? "Empty list" : "No fields"}</span>
  if (depth >= 5) return <details className="text-xs"><summary className="cursor-pointer text-primary">View nested value</summary><pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap [overflow-wrap:anywhere]">{JSON.stringify(value, null, 2)}</pre></details>
  return <div className="min-w-0 space-y-2">{(all ? entries : entries.slice(0, 20)).map(([key, item]) => <div key={key} className="min-w-0 rounded-lg border border-border/50 bg-muted/15 px-3 py-2"><p className="mb-1 text-xs font-medium text-muted-foreground">{Array.isArray(value) ? `Item ${Number(key) + 1}` : readableFieldName(key)}</p><RoutineRecordedValue value={item} depth={depth + 1} /></div>)}{entries.length > 20 && <button className="text-xs text-primary" onClick={() => setAll(v => !v)}>{all ? "Show fewer items" : `Show all ${entries.length} items`}</button>}</div>
}

export function RoutineSavedInputs({ values, definition }: { values?: Record<string, unknown> | null; definition: unknown }) {
  const [showJson, setShowJson] = useState(false)
  const specs = routineInputSpecs(definition as Record<string, unknown> | null)
  const names = [...new Set([...specs.map(s => s.name), ...Object.keys(values ?? {})])]
  return <DetailCard title="Inputs used for this run" icon={ArrowDownToLine} subtitle={values ? `${Object.keys(values).length} saved` : undefined}>
    <p className="mb-5 text-xs text-muted-foreground">Saved at execution time. Editing the recipe does not change these values.</p>
    {values == null ? <p role="status" className="text-sm text-muted-foreground">Saved inputs are unavailable for this run.</p> : !names.length ? <p className="rounded-xl border border-border/60 bg-muted/20 p-4 text-sm text-muted-foreground">This run has no saved inputs.</p> : <div className="space-y-4">{names.map(name => {
      const spec = specs.find(s => s.name === name)
      const supplied = Object.hasOwn(values, name)
      const kind = valueKind(values[name]); const Icon = kind.icon
      return <section key={name} aria-label={spec?.label || readableFieldName(name)} className="overflow-hidden rounded-2xl border border-border/60 bg-muted/15">
        <div className="flex items-start gap-3 border-b border-border/50 p-4"><span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary"><Icon className="h-4 w-4" /></span><div className="min-w-0 flex-1"><h3 className="text-sm font-medium [overflow-wrap:anywhere]">{spec?.label || readableFieldName(name)}</h3>{spec?.description && <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{spec.description}</p>}</div><span className="shrink-0 rounded-full bg-muted px-2 py-1 text-[10px] text-muted-foreground">{supplied ? kind.label : "Not supplied"}</span></div>
        <div className="min-w-0 p-4 sm:px-5"><RoutineRecordedValue value={supplied ? values[name] : undefined} /></div>
      </section>
    })}</div>}
    {values != null && <details className="mt-5 border-t border-border/60 pt-3 text-xs text-muted-foreground" onToggle={e => setShowJson(e.currentTarget.open)}><summary className="cursor-pointer">Technical details · saved JSON</summary>{showJson && <pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap [overflow-wrap:anywhere]">{JSON.stringify(values, null, 2)}</pre>}</details>}
  </DetailCard>
}
