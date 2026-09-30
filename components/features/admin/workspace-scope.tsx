"use client"

import * as React from "react"
import { Check, Minus } from "lucide-react"

import { cn } from "@/lib/utils"
import { DrillNavItem, DrillNavSection } from "@/components/layout/drill-page"

/**
 * The Workspaces section of an Admin page's panel. Admin is the instance: it
 * never depends on the workspace its user happens to sit in, so every page
 * that shows something per workspace starts on all of them and lets the
 * admin tick the ones they want, the way Crew links lists "All crews" above
 * each crew.
 *
 * The selection lives in the URL (?ws=slug,slug), so a link shows what its
 * sender saw. No ?ws means all; ?ws=none means nothing ticked.
 */

export interface ScopeWorkspace {
  id: string
  name: string
  slug: string
  /** A number beside the name, e.g. how many rows the page holds for it. */
  count?: number
}

export function readScope(all: ScopeWorkspace[]): Set<string> {
  if (typeof window === "undefined") return new Set(all.map((w) => w.id))
  const raw = new URLSearchParams(window.location.search).get("ws")
  if (raw === null) return new Set(all.map((w) => w.id))
  if (raw === "none") return new Set()
  const want = new Set(raw.split(",").filter(Boolean))
  return new Set(all.filter((w) => want.has(w.slug) || want.has(w.id)).map((w) => w.id))
}

export function writeScope(all: ScopeWorkspace[], selected: Set<string>) {
  const url = new URL(window.location.href)
  if (selected.size === all.length) url.searchParams.delete("ws")
  else if (selected.size === 0) url.searchParams.set("ws", "none")
  else url.searchParams.set("ws", all.filter((w) => selected.has(w.id)).map((w) => w.slug).join(","))
  window.history.replaceState(window.history.state, "", url.toString())
}

function Box({ state }: { state: "on" | "off" | "mixed" }) {
  return (
    <span
      aria-hidden
      className={cn(
        "grid h-3.5 w-3.5 shrink-0 place-items-center rounded-[4px] border",
        state === "off" ? "border-control-border" : "border-primary bg-primary text-primary-foreground",
      )}
    >
      {state === "on" && <Check className="h-2.5 w-2.5" strokeWidth={3} />}
      {state === "mixed" && <Minus className="h-2.5 w-2.5" strokeWidth={3} />}
    </span>
  )
}

function Avatar({ name }: { name: string }) {
  return (
    <span aria-hidden className="grid h-4 w-4 shrink-0 place-items-center rounded-[5px] bg-muted text-[9.5px] font-semibold text-muted-foreground">
      {(name.trim()[0] ?? "·").toUpperCase()}
    </span>
  )
}

/** One line under the list saying what the ticks mean for this page. */
export function scopeSummary(all: ScopeWorkspace[], selected: Set<string>, mode: "edit" | "view"): string {
  const n = selected.size
  if (n === 0) return "Nothing selected"
  const one = all.find((w) => selected.has(w.id))
  if (mode === "view") return n === all.length ? `All ${n} workspaces` : n === 1 ? one!.name : `${n} of ${all.length} workspaces`
  if (n === 1) return `Editing ${one!.name}`
  return n === all.length ? `All ${n} workspaces · saving overwrites all` : `${n} workspaces · saving overwrites them`
}

export function WorkspaceScopeSection({
  workspaces, selected, onChange, currentId, mode = "view", inert,
}: {
  workspaces: ScopeWorkspace[]
  selected: Set<string>
  onChange: (next: Set<string>) => void
  /** The workspace the admin sits in, marked "here" and nothing more. */
  currentId?: string | null
  mode?: "edit" | "view"
  /** When set, the selection does not apply to what is on screen (an
   *  instance-wide setting): the list is greyed out and this says why. */
  inert?: string
}) {
  const n = selected.size
  const all = n === workspaces.length && n > 0
  const toggle = (id: string) => {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    onChange(next)
  }
  return (
    <DrillNavSection label="Workspaces" count={workspaces.length} collapsible={false}>
      <div data-slot="workspace-scope" className={cn(inert && "pointer-events-none opacity-45")} aria-disabled={inert ? true : undefined}>
        <DrillNavItem
          pressed={all}
          onSelect={() => onChange(all ? new Set() : new Set(workspaces.map((w) => w.id)))}
          icon={<Box state={all ? "on" : n ? "mixed" : "off"} />}
          label="All workspaces"
        />
        {workspaces.map((w, i) => (
          <DrillNavItem
            key={w.id}
            index={i + 1}
            pressed={selected.has(w.id)}
            onSelect={() => toggle(w.id)}
            icon={<span className="flex items-center gap-2"><Box state={selected.has(w.id) ? "on" : "off"} /><Avatar name={w.name} /></span>}
            label={
              <span className="flex min-w-0 items-center gap-1.5">
                <span className="truncate">{w.name}</span>
                {w.id === currentId && (
                  <span className="rounded border border-dashed border-control-border px-1 font-mono text-[9px] text-muted-foreground" title="The workspace you are in">here</span>
                )}
              </span>
            }
            meta={w.count !== undefined ? w.count : undefined}
          />
        ))}
      </div>
      <p className="px-2 pb-1 pt-1 text-[11px] text-muted-foreground" data-slot="workspace-scope-summary">
        {inert ?? scopeSummary(workspaces, selected, mode)}
      </p>
    </DrillNavSection>
  )
}
