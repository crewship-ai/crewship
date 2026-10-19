"use client"

import { useCallback, useEffect, useState } from "react"
import { Bookmark, Loader2, Plus, Trash2 } from "lucide-react"
import { toast } from "@/lib/toast"

import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { SidebarRow, SidebarSection } from "@/components/layout/sidebar-kit"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { apiFetch } from "@/lib/api-fetch"
import {
  AGENT_FACETS,
  EMPTY_CALENDAR_FILTERS,
  STATUS_FACETS,
  type AgentFacet,
  type CalendarFilters,
  type StatusFacet,
} from "@/lib/calendar/model"
import { CALENDAR_SURFACE, calendarViews, parseSavedViews } from "@/lib/saved-views"
import type { SavedView } from "@/lib/types/mission"

/**
 * Saved views of the Calendar (CAL-03). The same server store the Journal and
 * the issue board use (`crewship saved-view …`); the `surface` key in
 * filters_json keeps each surface's rows apart (lib/saved-views.ts). Only the
 * filter is stored, never access: a shared view still shows each person only
 * what they can open.
 */
export function encodeCalendarFilters(f: CalendarFilters): string {
  const params: Record<string, string> = {}
  if (f.scope !== EMPTY_CALENDAR_FILTERS.scope) params.scope = f.scope
  if (f.crewId) params.crew = f.crewId
  if (f.agentId) params.agent = f.agentId
  if (f.statuses.length) params.status = f.statuses.join(",")
  if (f.agentFacets.length) params.agent_facet = f.agentFacets.join(",")
  if (f.query.trim()) params.q = f.query.trim()
  return JSON.stringify({ surface: CALENDAR_SURFACE, params })
}

/** The filters in a stored view; null when the payload is not a calendar view. Unknown values are dropped. */
export function decodeCalendarFilters(json: string): CalendarFilters | null {
  try {
    const parsed = JSON.parse(json) as { surface?: unknown; params?: Record<string, unknown> }
    if (!parsed || parsed.surface !== CALENDAR_SURFACE) return null
    const p = parsed.params ?? {}
    const list = <T extends string>(raw: unknown, known: readonly T[]): T[] =>
      typeof raw === "string" ? raw.split(",").filter((x): x is T => (known as readonly string[]).includes(x)) : []
    return {
      scope: p.scope === "workspace" ? "workspace" : "mine",
      crewId: typeof p.crew === "string" && p.crew ? p.crew : null,
      agentId: typeof p.agent === "string" && p.agent ? p.agent : null,
      statuses: list<StatusFacet>(p.status, STATUS_FACETS.map((f) => f.id)),
      agentFacets: list<AgentFacet>(p.agent_facet, AGENT_FACETS.map((f) => f.id)),
      query: typeof p.q === "string" ? p.q : "",
    }
  } catch {
    return null
  }
}

export function CalendarSavedViews({
  workspaceId,
  filters,
  onApply,
}: {
  workspaceId: string
  filters: CalendarFilters
  onApply: (filters: CalendarFilters) => void
}) {
  const [views, setViews] = useState<SavedView[]>([])
  const [saveOpen, setSaveOpen] = useState(false)
  const [name, setName] = useState("")
  const [shared, setShared] = useState(false)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await apiFetch(`/api/v1/saved-views?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (!res.ok) return
      setViews(calendarViews(parseSavedViews(await res.json())))
    } catch {
      // Saved views are an aid; the calendar works without them.
    }
  }, [workspaceId])
  useEffect(() => {
    void load()
  }, [load])

  const create = async () => {
    if (!name.trim() || saving) return
    setSaving(true)
    try {
      const res = await apiFetch(`/api/v1/saved-views?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        // view_type is CHECK-constrained to board|list; the surface lives in filters_json.
        body: JSON.stringify({ name: name.trim(), filters_json: encodeCalendarFilters(filters), view_type: "list", shared }),
      })
      if (!res.ok) {
        toast.error(res.status === 403 ? "You do not have permission to save views in this workspace" : "Could not save the view")
        return
      }
      toast.success(`Saved “${name.trim()}”`, { description: "Only the filter is saved, never access." })
      setSaveOpen(false)
      setName("")
      setShared(false)
      await load()
    } catch {
      toast.error("Could not save the view")
    } finally {
      setSaving(false)
    }
  }

  const remove = async (view: SavedView) => {
    try {
      const res = await apiFetch(`/api/v1/saved-views/${encodeURIComponent(view.id)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "DELETE" })
      if (!res.ok) {
        toast.error(res.status === 403 ? "Only the owner can delete this view" : "Could not delete the view")
        return
      }
      await load()
    } catch {
      toast.error("Could not delete the view")
    }
  }

  return (
    <>
      <SidebarSection label="Saved views" count={views.length || undefined}>
        {views.map((v) => (
          <div key={v.id} className="group flex items-center">
            <SidebarRow
              className="min-w-0 flex-1"
              onSelect={() => {
                const f = decodeCalendarFilters(v.filters_json)
                if (f) onApply(f)
              }}
              as="div"
            >
              <Bookmark className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate">{v.name}</span>
              {v.shared && <span className="text-micro text-muted-foreground-soft">shared</span>}
            </SidebarRow>
            <button
              type="button"
              aria-label={`Delete view ${v.name}`}
              onClick={() => void remove(v)}
              className="kit-tap mr-1 hidden shrink-0 rounded p-1 text-muted-foreground hover:text-destructive group-hover:block focus-visible:block coarse:block"
            >
              <Trash2 className="size-3.5" aria-hidden />
            </button>
          </div>
        ))}
        <button
          type="button"
          onClick={() => setSaveOpen(true)}
          className="kit-tap mx-3.5 mt-1 inline-flex items-center gap-1.5 text-control font-medium text-muted-foreground hover:text-foreground"
        >
          <Plus className="size-3.5" aria-hidden />
          Save this view
        </button>
      </SidebarSection>

      <CreateSurface open={saveOpen} onOpenChange={setSaveOpen} size="sm" onSubmit={() => void create()}>
        <CreateSurfaceHeader
          title="Save this view"
          description="Stores the scope, crew, agent, status filters and search. Only the filter is shared, never access: everyone still sees only what they can open."
          onClose={() => setSaveOpen(false)}
        />
        <CreateSurfaceBody className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="calendar-view-name">Name</Label>
            <Input id="calendar-view-name" value={name} autoFocus placeholder="Research crew" onChange={(e) => setName(e.target.value)} />
          </div>
          <label className="flex items-center gap-2 text-body text-muted-foreground">
            <Checkbox checked={shared} onCheckedChange={(v) => setShared(v === true)} aria-label="Share with the workspace" />
            Share with the workspace
          </label>
        </CreateSurfaceBody>
        <CreateSurfaceFooter
          onCancel={() => setSaveOpen(false)}
          primaryLabel={
            <>
              {saving && <Loader2 className="size-3 animate-spin" aria-hidden />}
              Save view
            </>
          }
          onPrimary={() => void create()}
          primaryDisabled={!name.trim() || saving}
        />
      </CreateSurface>
    </>
  )
}
