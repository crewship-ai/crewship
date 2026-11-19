"use client"

import * as React from "react"
import { Building2, Search } from "lucide-react"

import { Input } from "@/components/ui/input"
import type { ComboboxOption } from "@/components/ui/combobox"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { WorkspaceTile } from "@/app/(dashboard)/admin/tabs/admin-kit"
import { displayName, membersOf, type Person, type Workspace } from "./people-model"
import { PersonAvatar } from "./people-parts"

/** Every workspace as a Combobox row: tile, name, address. Search finds all of them. */
export const workspaceOptions = (workspaces: readonly Workspace[]): ComboboxOption[] =>
  workspaces.map((w) => ({
    value: w.id, label: w.name, description: w.slug, keywords: [w.slug],
    icon: <WorkspaceTile id={w.id} name={w.name} size="xs" logoUrl={w.logo_url} />,
  }))

/** Every person as a Combobox row: face, name, email. */
export const personOptions = (people: readonly Person[]): ComboboxOption[] =>
  people.map((p) => ({
    value: p.id, label: displayName(p), description: p.email, keywords: [p.email],
    icon: <PersonAvatar person={p} className="h-5 w-5" />,
  }))

/** How many matches a search list draws; the rest is one more keystroke away. */
const VISIBLE = 40

/**
 * Pick workspaces from all of them: a search over name and address, a list of
 * the first matches. With `max` it is "Choose workspaces" for the access grid
 * (ticks, up to six); with `max` of 1 a pick closes it (the Workspace facet).
 */
export function ChooseWorkspacesDialog({ open, onOpenChange, workspaces, people, selected, max, title, description, primaryLabel = "Show", onPick }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaces: Workspace[]
  people: Person[]
  selected: string[]
  max: number
  title: string
  description?: string
  primaryLabel?: string
  onPick: (ids: string[]) => void
}) {
  const [q, setQ] = React.useState("")
  const [ticked, setTicked] = React.useState<string[]>(selected)
  React.useEffect(() => { if (open) { setQ(""); setTicked(selected) } }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  const single = max === 1
  const needle = q.trim().toLowerCase()
  const matches = workspaces.filter((w) => !needle || `${w.name} ${w.slug}`.toLowerCase().includes(needle))
  const toggle = (id: string) => {
    if (single) { onPick([id]); onOpenChange(false); return }
    setTicked((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : prev.length < max ? [...prev, id] : prev))
  }
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm">
      <CreateSurfaceHeader icon={Building2} title={title} description={description} onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody className="grid gap-3">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <Input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={`Search ${workspaces.length} workspaces`} aria-label="Search workspaces" className="pl-8" />
        </div>
        <ul className="max-h-72 overflow-y-auto rounded-lg border border-border" aria-label="Workspaces">
          {matches.slice(0, VISIBLE).map((w) => {
            const on = ticked.includes(w.id) || (single && selected.includes(w.id))
            const full = !single && !on && ticked.length >= max
            return (
              <li key={w.id} className="border-b border-border last:border-b-0">
                {single ? (
                  <button type="button" aria-pressed={on} onClick={() => toggle(w.id)}
                    className="flex min-h-10 w-full items-center gap-2.5 px-3 py-1.5 text-left text-control hover:bg-accent coarse:min-h-[44px]">
                    <WorkspaceTile id={w.id} name={w.name} size="xs" logoUrl={w.logo_url} />
                    <span className="min-w-0 flex-1 truncate">{w.name}</span>
                    <span className="text-label tabular-nums text-muted-foreground">{membersOf(people, w.id).length}</span>
                  </button>
                ) : (
                  <label className="flex min-h-10 cursor-pointer items-center gap-2.5 px-3 py-1.5 text-control coarse:min-h-[44px]">
                    <input type="checkbox" checked={on} disabled={full} onChange={() => toggle(w.id)} className="size-3.5 accent-[var(--primary)]" />
                    <WorkspaceTile id={w.id} name={w.name} size="xs" logoUrl={w.logo_url} />
                    <span className="min-w-0 flex-1 truncate">{w.name}</span>
                    <span className="text-label tabular-nums text-muted-foreground">{membersOf(people, w.id).length}</span>
                  </label>
                )}
              </li>
            )
          })}
          {matches.length === 0 && <li className="px-3 py-4 text-center text-label text-muted-foreground">No workspace matches.</li>}
        </ul>
        {matches.length > VISIBLE && <p className="text-label text-muted-foreground">{VISIBLE} of {matches.length} shown. Type to narrow.</p>}
      </CreateSurfaceBody>
      {single ? (
        <CreateSurfaceFooter hint={null} onCancel={() => onOpenChange(false)} />
      ) : (
        <CreateSurfaceFooter hint={<span className="tabular-nums">{ticked.length} of {max}</span>} onCancel={() => onOpenChange(false)}
          primaryLabel={primaryLabel} primaryDisabled={ticked.length === 0} onPrimary={() => { onPick(ticked); onOpenChange(false) }} />
      )}
    </CreateSurface>
  )
}
