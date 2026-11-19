"use client"

import * as React from "react"
import { Building2, Check, ChevronsUpDown, Search } from "lucide-react"

import { cn } from "@/lib/utils"
import { useShowMore } from "@/hooks/use-show-more"
import { ShowMore } from "@/components/ui/show-more"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Checkbox } from "@/components/ui/checkbox"
import { Button } from "@/components/ui/button"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { scopeLabel, type BackupScope, type ScopeWorkspace } from "./backups-model"

/**
 * Search, "tick all found", and the workspaces 50 a step: the list both the
 * scope control and a plan's workspaces use. Built for hundreds.
 */
function WorkspaceChecklist({ workspaces, ticked, onSet, top, idPrefix }: {
  workspaces: ScopeWorkspace[]
  ticked: Set<string>
  onSet: (next: Set<string>) => void
  /** Rows above the list (the scope control's "Whole instance"). */
  top?: React.ReactNode
  idPrefix: string
}) {
  const [q, setQ] = React.useState("")
  const needle = q.trim().toLowerCase()
  const matching = React.useMemo(
    () => workspaces.filter((w) => !needle || w.name.toLowerCase().includes(needle) || w.slug.toLowerCase().includes(needle)),
    [workspaces, needle],
  )
  const page = useShowMore(matching, { step: 50, resetKey: needle })
  const toggle = (id: string) => {
    const next = new Set(ticked)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    onSet(next)
  }
  return (
    <>
      <div className="flex items-center gap-2 border-b border-border px-3">
        <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <input type="search" aria-label="Search workspaces" placeholder={`Search ${workspaces.length} workspaces…`} value={q} onChange={(e) => setQ(e.target.value)}
          className="h-10 min-w-0 flex-1 bg-transparent text-control outline-none placeholder:text-muted-foreground coarse:h-[44px]" />
      </div>
      <div className="max-h-[min(360px,55dvh)] overflow-y-auto py-1" data-slot="scope-list">
        {top}
        <div className="mt-1 flex items-center justify-between gap-2 border-t border-border px-3 pb-1 pt-2">
          <span className="text-micro text-muted-foreground" aria-live="polite">
            {ticked.size ? `${ticked.size} of ${workspaces.length} ticked` : "Tick the workspaces"}
          </span>
          <span className="flex gap-1">
            {matching.length > 0 && <Button type="button" variant="ghost" size="sm" className="h-6 px-2 text-micro" onClick={() => onSet(new Set([...ticked, ...matching.map((w) => w.id)]))}>{needle ? `Tick all ${matching.length} found` : "Tick all"}</Button>}
            {ticked.size > 0 && <Button type="button" variant="ghost" size="sm" className="h-6 px-2 text-micro" onClick={() => onSet(new Set())}>Clear</Button>}
          </span>
        </div>
        {matching.length === 0 && <p className="px-3 py-4 text-center text-label text-muted-foreground">No workspace matches.</p>}
        {page.shown.map((w) => {
          const id = `${idPrefix}-${w.id}`
          const on = ticked.has(w.id)
          return (
            <label key={w.id} htmlFor={id} className="kit-tap flex cursor-pointer items-center gap-2.5 px-3 py-1.5 hover:bg-accent coarse:min-h-[44px]">
              <Checkbox id={id} checked={on} onCheckedChange={() => toggle(w.id)} aria-label={`Show ${w.name}`} />
              <WorkspaceTile id={w.id} name={w.name} logoUrl={w.logoUrl} />
              <span className="min-w-0 flex-1 truncate text-control">{w.name}</span>
              {on && <Check className="h-3 w-3 shrink-0 text-primary-hover" aria-hidden />}
            </label>
          )
        })}
      </div>
      <ShowMore shown={page.shown.length} total={page.total} nextCount={page.nextCount} onMore={page.showMore} noun="workspaces" paged={page.paged} />
    </>
  )
}

/**
 * "Showing: Whole instance | N workspaces": the one place the scope of
 * Overview, Runs, Restore and Back up now is chosen. Nothing ticked is the
 * whole instance. The scope lives in the URL (?scope=&ws=); the page commits it.
 */
export function BackupsScopePicker({ scope, selected, workspaces, onCommit, className }: {
  scope: BackupScope
  selected: Set<string>
  workspaces: ScopeWorkspace[]
  onCommit: (scope: BackupScope, selected: Set<string>) => void
  className?: string
}) {
  const [open, setOpen] = React.useState(false)
  const ticked = scope === "workspaces" ? selected : new Set<string>()
  const set = (next: Set<string>) => (next.size === 0 ? onCommit("instance", next) : onCommit("workspaces", next))
  const value = scopeLabel(scope, selected, workspaces)
  return (
    <Popover open={open} onOpenChange={setOpen} modal>
      <PopoverTrigger asChild>
        <button type="button" data-slot="backups-scope" aria-label={`Showing: ${value}`} aria-haspopup="dialog" aria-expanded={open}
          className={cn("kit-tap flex w-full min-w-0 items-center gap-2.5 rounded-lg border border-control-border bg-card px-3 py-2 text-left transition-colors hover:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring", className)}>
          <Building2 className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="min-w-0 flex-1">
            <span className="block text-micro text-muted-foreground">Showing</span>
            <span className="block truncate text-control font-medium text-foreground" data-slot="scope-value">{value}</span>
          </span>
          <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[min(340px,calc(100vw-24px))] p-0">
        <WorkspaceChecklist workspaces={workspaces} ticked={ticked} onSet={set} idPrefix="scope-ws" top={
          <button type="button" role="radio" aria-checked={scope === "instance"} onClick={() => { onCommit("instance", new Set()); setOpen(false) }}
            className="kit-tap flex w-full items-start gap-2.5 px-3 py-2 text-left hover:bg-accent coarse:min-h-[44px]">
            <span aria-hidden className={cn("mt-1 h-3.5 w-3.5 shrink-0 rounded-full border", scope === "instance" ? "border-[4px] border-primary" : "border-control-border")} />
            <span className="min-w-0">
              <span className="block text-control text-foreground">Whole instance</span>
              <span className="block text-label text-muted-foreground">Every workspace, users and settings, as one backup</span>
            </span>
          </button>
        } />
      </PopoverContent>
    </Popover>
  )
}

/**
 * A plan's workspaces: none ticked means every workspace, including ones
 * created later. A search over hundreds, ticked in a popover.
 */
export function WorkspacesField({ workspaces, picked, onChange }: {
  workspaces: ScopeWorkspace[]
  /** Workspace ids; empty is every workspace. */
  picked: string[]
  onChange: (ids: string[]) => void
}) {
  const ticked = new Set(picked)
  const text = picked.length === 0 ? "Every workspace" : picked.length === 1 ? workspaces.find((w) => w.id === picked[0])?.name ?? "1 workspace" : `${picked.length} workspaces`
  return (
    <Popover modal>
      <PopoverTrigger asChild>
        <button type="button" aria-label={`Workspaces: ${text}`} data-slot="plan-workspaces"
          className="kit-tap flex h-[var(--control-md)] w-full min-w-0 items-center gap-2 rounded-md border border-control-border bg-card px-2.5 text-left text-control hover:border-ring sm:w-64 coarse:h-[44px]">
          <span className="min-w-0 flex-1 truncate">{text}</span>
          <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[min(340px,calc(100vw-24px))] p-0">
        <WorkspaceChecklist workspaces={workspaces} ticked={ticked} onSet={(n) => onChange(n.size === workspaces.length ? [] : [...n])} idPrefix="plan-ws" />
      </PopoverContent>
    </Popover>
  )
}
