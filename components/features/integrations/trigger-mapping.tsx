"use client"

import * as React from "react"
import { Plus, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

/**
 * A routine trigger's mapping (`inputs_template`): routine input ← what it
 * takes from the call, usually a `{{ inputs.event.… }}` template. Edited in
 * New trigger (step 2) and on the trigger's Mapping tab; saved with
 * PATCH /pipeline-webhooks/{id} `inputs_template`.
 *
 * `event`, `raw` and `headers` come from the call itself; the server ignores a
 * mapping that names them (pipeline.WebhookUntrustedInputKeys), so the editor
 * refuses them instead of saving something that silently does nothing.
 */

export const RESERVED_INPUTS = ["event", "raw", "headers"] as const

export interface MappingRow {
  /** Stable React key; not sent. */
  key: string
  name: string
  value: string
  /** A non-string value from the server, kept as is while its text is untouched. */
  original?: unknown
}

let seq = 0
const nextKey = () => `m${++seq}`

const show = (v: unknown) => (typeof v === "string" ? v : JSON.stringify(v))

export function mappingRows(template: Record<string, unknown> | null | undefined): MappingRow[] {
  return Object.entries(template ?? {}).map(([name, v]) => ({ key: nextKey(), name, value: show(v), ...(typeof v === "string" ? {} : { original: v }) }))
}

export function emptyMappingRow(): MappingRow {
  return { key: nextKey(), name: "", value: "" }
}

/** Rows with neither a name nor a value are left out, so an added blank row is no change. */
export function mappingTemplate(rows: MappingRow[]): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const r of rows) {
    const name = r.name.trim()
    if (!name && !r.value.trim()) continue
    out[name] = r.original !== undefined && r.value === show(r.original) ? r.original : r.value
  }
  return out
}

/** Why the mapping cannot be saved, or null. */
export function mappingProblem(rows: MappingRow[]): string | null {
  const used = rows.filter((r) => r.name.trim() || r.value.trim())
  if (used.some((r) => !r.name.trim())) return "Every input needs a name"
  if (used.some((r) => (RESERVED_INPUTS as readonly string[]).includes(r.name.trim()))) return "event, raw and headers come from the call and cannot be mapped"
  const names = used.map((r) => r.name.trim())
  if (new Set(names).size !== names.length) return "Each input name can be used once"
  if (used.some((r) => !r.value.trim())) return "Every input needs a value"
  return null
}

/** Same mapping as the server's: names and values, order ignored. */
export function sameMapping(a: Record<string, unknown>, b: Record<string, unknown>): boolean {
  const ka = Object.keys(a)
  const kb = Object.keys(b)
  return ka.length === kb.length && ka.every((k) => k in b && JSON.stringify(a[k]) === JSON.stringify(b[k]))
}

export function MappingEditor({
  rows,
  onChange,
  readOnly = false,
  problem,
}: {
  rows: MappingRow[]
  onChange: (rows: MappingRow[]) => void
  readOnly?: boolean
  problem: string | null
}) {
  const id = React.useId()
  const set = (i: number, patch: Partial<MappingRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  return (
    <div className="grid gap-2">
      {rows.length > 0 && (
        <ul className="grid gap-1.5" aria-label="Mapping">
          {rows.map((r, i) => (
            <li key={r.key} className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] items-center gap-1.5 max-sm:grid-cols-[minmax(0,1fr)_auto]">
              <Input aria-label={`Input name ${i + 1}`} value={r.name} disabled={readOnly} placeholder="repo" className="h-8 font-mono text-label" onChange={(e) => set(i, { name: e.target.value })} />
              <Input
                aria-label={`Value of input ${i + 1}`}
                value={r.value}
                disabled={readOnly}
                placeholder="{{ inputs.event.repository.full_name }}"
                className="h-8 font-mono text-label max-sm:order-3 max-sm:col-span-2"
                onChange={(e) => set(i, { value: e.target.value })}
              />
              {!readOnly && (
                <Button type="button" variant="ghost" size="icon-sm" aria-label={`Remove input ${i + 1}`} onClick={() => onChange(rows.filter((_, j) => j !== i))}>
                  <X className="size-3.5" aria-hidden />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {problem && (
        <p id={`${id}-problem`} className="text-label text-destructive">
          {problem}
        </p>
      )}
      {!readOnly && (
        <div>
          <Button type="button" variant="outline" size="sm" onClick={() => onChange([...rows, emptyMappingRow()])}>
            <Plus className="size-3.5" aria-hidden />
            Add input
          </Button>
        </div>
      )}
    </div>
  )
}
