"use client"

import * as React from "react"
import { Check, ChevronDown, Search } from "lucide-react"

import { cn } from "@/lib/utils"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import type { InstanceGovRow } from "./use-instance-keeper"

/** At most this many rows draw; the search narrows the rest (hundreds of workspaces). */
const LIMIT = 50

/**
 * Which workspaces the activity is read for: the whole instance, or several
 * picked by name. Searchable; the choice is a list of slugs, which the URL
 * carries as ?in=a,b.
 */
export function WorkspacePicker({ rows, value, onChange }: { rows: InstanceGovRow[]; value: string[]; onChange: (slugs: string[]) => void }) {
  const [open, setOpen] = React.useState(false)
  const [q, setQ] = React.useState("")
  const needle = q.trim().toLowerCase()
  const matches = rows.filter((r) => !needle || r.workspace_name.toLowerCase().includes(needle) || r.workspace_slug.includes(needle))
  const shown = matches.slice(0, LIMIT)
  const name = (slug: string) => rows.find((r) => r.workspace_slug === slug)?.workspace_name ?? slug
  const label = value.length === 0 ? "All workspaces" : value.length === 1 ? name(value[0]) : `${value.length} workspaces`
  const toggle = (slug: string) => onChange(value.includes(slug) ? value.filter((s) => s !== slug) : [...value, slug])
  return (
    <Popover open={open} onOpenChange={(o) => { setOpen(o); if (!o) setQ("") }}>
      <PopoverTrigger asChild>
        <button type="button" role="combobox" aria-expanded={open} aria-label="Workspaces"
          className="inline-flex h-[var(--control-sm)] max-w-[14rem] items-center gap-1.5 rounded-md border border-control-border bg-surface-subtle px-2.5 text-control coarse:h-[44px]">
          <span className="truncate">{label}</span><ChevronDown className="size-3 shrink-0 text-muted-foreground" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-2">
        <label className="flex items-center gap-2 rounded-md border border-control-border px-2">
          <Search className="size-3.5 text-muted-foreground" aria-hidden />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={`Search ${rows.length} workspaces`} aria-label="Search workspaces"
            className="h-8 min-w-0 flex-1 bg-transparent text-control outline-none placeholder:text-muted-foreground" />
        </label>
        <div className="mt-2 max-h-64 overflow-y-auto" role="group" aria-label="Workspaces to show">
          <button type="button" aria-pressed={value.length === 0} onClick={() => onChange([])}
            className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-control hover:bg-accent coarse:min-h-[44px]">
            <Check className={cn("size-3.5", value.length === 0 ? "opacity-100" : "opacity-0")} aria-hidden /><span className="flex-1 font-medium">Whole instance</span>
            <span className="tabular-nums text-label text-muted-foreground">{rows.length}</span>
          </button>
          {shown.map((r) => {
            const on = value.includes(r.workspace_slug)
            return (
              <button key={r.workspace_id} type="button" role="checkbox" aria-checked={on} onClick={() => toggle(r.workspace_slug)}
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-control hover:bg-accent coarse:min-h-[44px]">
                <Check className={cn("size-3.5", on ? "opacity-100" : "opacity-0")} aria-hidden />
                <WorkspaceTile id={r.workspace_id} name={r.workspace_name} logoUrl={r.workspace_logo_url} />
                <span className="min-w-0 flex-1 truncate">{r.workspace_name}</span>
              </button>
            )
          })}
          {matches.length === 0 && <p className="px-2 py-3 text-label text-muted-foreground">No workspace matches.</p>}
          {matches.length > LIMIT && <p className="px-2 py-2 text-label text-muted-foreground">{matches.length - LIMIT} more. Type to narrow the list.</p>}
        </div>
      </PopoverContent>
    </Popover>
  )
}
