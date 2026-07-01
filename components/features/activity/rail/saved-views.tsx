"use client"

import { useState } from "react"
import { Plus, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { useUserPreference } from "@/hooks/use-user-preference"
import type { GroupAxis, RunFilter } from "@/lib/activity/run-filters"
import type { SortAxis } from "./rail-toolbar"

// Saved views — named filter combinations the user can quickly recall.
// Persisted per-user via useUserPreference (same mechanism as the
// rail collapsed flag, the heatmap toggle, etc.).
//
// Why local-only persistence: views are personal opinions about how to
// look at a workspace; sharing/team-saved is out of scope for v1 but
// can graduate to a server-backed table later.

export interface SavedView {
  id: string // ulid-ish; we don't need cryptographic uniqueness
  name: string
  filter: RunFilter
  sort: SortAxis
  group: GroupAxis
}

interface Bundle {
  views: SavedView[]
  // Last-active view id, so the next time the user opens /activity
  // the same lens is loaded. null = "default" (no view applied).
  activeId: string | null
}

const DEFAULT_BUNDLE: Bundle = { views: [], activeId: null }

export function useSavedViews() {
  const [bundle, setBundle] = useUserPreference<Bundle>(
    "activity.rail.savedViews",
    DEFAULT_BUNDLE,
  )

  const save = (name: string, view: Omit<SavedView, "id" | "name">) => {
    const id = `sv_${Date.now().toString(36)}`
    const next: Bundle = {
      views: [...bundle.views, { id, name, ...view }],
      activeId: id,
    }
    setBundle(next)
    return id
  }

  const remove = (id: string) => {
    setBundle({
      views: bundle.views.filter((v) => v.id !== id),
      activeId: bundle.activeId === id ? null : bundle.activeId,
    })
  }

  const setActive = (id: string | null) => {
    setBundle({ ...bundle, activeId: id })
  }

  return { views: bundle.views, activeId: bundle.activeId, save, remove, setActive }
}

interface SavedViewsMenuSectionProps {
  current: { filter: RunFilter; sort: SortAxis; group: GroupAxis }
  onApply: (view: SavedView) => void
  /** Close the surrounding menu after applying / saving / clearing. */
  onClose?: () => void
}

/**
 * Saved-views content, rendered INSIDE a parent DropdownMenuContent — it's
 * folded into the rail's ⋮ View menu (Sort / Group / Saved views) rather than
 * living as its own toolbar button, so row 1 stays Search + Filter + View and
 * the search keeps its full width.
 */
export function SavedViewsMenuSection({ current, onApply, onClose }: SavedViewsMenuSectionProps) {
  const { views, activeId, save, remove, setActive } = useSavedViews()
  const [naming, setNaming] = useState(false)
  const [nameDraft, setNameDraft] = useState("")

  const handleSave = () => {
    const trimmed = nameDraft.trim()
    if (!trimmed) return
    save(trimmed, current)
    setNameDraft("")
    setNaming(false)
    onClose?.()
  }

  return (
    <>
      <DropdownMenuLabel className="text-[10px] uppercase tracking-wider text-muted-foreground/60">
        Saved views
      </DropdownMenuLabel>
      {views.length === 0 && (
        <div className="px-2 py-1 text-[11px] text-muted-foreground/60">
          No saved views yet.
        </div>
      )}
      {views.map((v) => (
        <div
          key={v.id}
          className="flex items-center gap-1 rounded px-1 py-0.5 hover:bg-white/[0.04]"
        >
          <button
            type="button"
            onClick={() => {
              onApply(v)
              setActive(v.id)
              onClose?.()
            }}
            className={`flex-1 truncate px-1 py-0.5 text-left text-[11px] ${activeId === v.id ? "text-primary-hover" : ""}`}
          >
            {v.name}
          </button>
          <button
            type="button"
            onClick={() => remove(v.id)}
            aria-label={`Delete view ${v.name}`}
            className="rounded p-0.5 text-muted-foreground/50 hover:text-rose-300"
          >
            <Trash2 className="h-3 w-3" />
          </button>
        </div>
      ))}
      <DropdownMenuSeparator />
      {naming ? (
        <div className="flex gap-1 p-1">
          <input
            type="text"
            autoFocus
            value={nameDraft}
            onChange={(e) => setNameDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") handleSave()
              if (e.key === "Escape") {
                setNaming(false)
                setNameDraft("")
              }
            }}
            placeholder="View name"
            className="h-6 flex-1 rounded border border-border bg-background px-1.5 text-[11px]"
          />
          <Button size="xs" onClick={handleSave} disabled={!nameDraft.trim()}>
            Save
          </Button>
        </div>
      ) : (
        <DropdownMenuItem onSelect={(e) => { e.preventDefault(); setNaming(true) }} className="text-[11px]">
          <Plus className="h-3 w-3" />
          Save current as view
        </DropdownMenuItem>
      )}
      {activeId && (
        <DropdownMenuItem
          onSelect={() => { setActive(null); onClose?.() }}
          className="text-[11px] text-muted-foreground/70"
        >
          Clear active view
        </DropdownMenuItem>
      )}
    </>
  )
}
