"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { Bookmark, Check, Loader2, Save, Trash2 } from "lucide-react"
import { toast } from "sonner"

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Checkbox } from "@/components/ui/checkbox"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { JOURNAL_SURFACE, journalViews, parseSavedViews } from "@/lib/saved-views"
import { journalFiltersFromJson } from "@/hooks/use-journal-url-state"
import type { SavedView } from "@/lib/types/mission"

/**
 * Saved views for /journal.
 *
 * `crewship saved-view create|list|update|delete` has shipped for a while —
 * server-stored named query bookmarks, `--shared` to hand one to the whole
 * workspace, `--filters` taking arbitrary JSON. The journal was the one
 * surface that could not use them, so the only way to keep
 * `routine:nightly-digest outcome:failed` was a browser bookmark, which the
 * URL contract then dropped the query from anyway.
 *
 * No backend change: `filters_json` carries the journal's own URL params, and
 * the `surface` key inside it is what keeps these out of the issue board's
 * dropdown (lib/saved-views.ts — `view_type` cannot do that job, the column
 * has a CHECK constraint pinning it to 'board' or 'list'). A view written by
 * hand from the CLI works as typed:
 *
 *   crewship saved-view create --name "Failed nightly digests" \
 *     --filters '{"surface":"journal","params":{"tab":"runs","run_status":"FAILED"}}'
 */

/** Shape stored in `filters_json`. The envelope leaves room to version it. */
interface JournalViewPayload {
  surface: typeof JOURNAL_SURFACE
  params: Record<string, string>
}

export function encodeJournalViewFilters(params: Record<string, string>): string {
  const payload: JournalViewPayload = { surface: JOURNAL_SURFACE, params }
  return JSON.stringify(payload)
}

/** Two filter sets are the same view when they name the same keys and values. */
function sameFilters(a: Record<string, string>, b: Record<string, string>): boolean {
  const ka = Object.keys(a)
  const kb = Object.keys(b)
  if (ka.length !== kb.length) return false
  return ka.every((k) => a[k] === b[k])
}

export interface JournalSavedViewsProps {
  workspaceId: string | null
  /** The journal-owned params currently in the URL. */
  filters: Record<string, string>
  /** Apply a saved view — replaces the whole journal param set. */
  onApply: (params: Record<string, string>) => void
}

export function JournalSavedViews({ workspaceId, filters, onApply }: JournalSavedViewsProps) {
  const [views, setViews] = useState<SavedView[]>([])
  const [open, setOpen] = useState(false)
  const [saveOpen, setSaveOpen] = useState(false)
  const [name, setName] = useState("")
  const [shared, setShared] = useState(false)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await apiFetch(
        `/api/v1/saved-views?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (!res.ok) return
      setViews(journalViews(parseSavedViews(await res.json())))
    } catch {
      /* the strip degrades to "no saved views"; nothing else depends on it */
    }
  }, [workspaceId])

  useEffect(() => {
    void load()
  }, [load])

  const decoded = useMemo(
    () =>
      views.map((v) => ({ view: v, params: journalFiltersFromJson(v.filters_json) ?? {} })),
    [views],
  )

  const active = useMemo(
    () => decoded.find((d) => sameFilters(d.params, filters))?.view ?? null,
    [decoded, filters],
  )

  const hasFilters = Object.keys(filters).length > 0

  const create = useCallback(async () => {
    if (!workspaceId || !name.trim()) return
    setSaving(true)
    try {
      const res = await apiFetch(
        `/api/v1/saved-views?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name: name.trim(),
            filters_json: encodeJournalViewFilters(filters),
            // Not "journal": saved_views.view_type is CHECK-constrained to
            // 'board' or 'list', and the insert 500s on anything else. The
            // surface marker lives in filters_json instead.
            view_type: "list",
            shared,
          }),
        },
      )
      if (!res.ok) {
        // 403 here is the VIEWER role, which the API refuses for `create`.
        toast.error(
          res.status === 403
            ? "You do not have permission to save views in this workspace"
            : "Could not save the view",
        )
        return
      }
      toast.success(`Saved “${name.trim()}”`)
      setSaveOpen(false)
      setName("")
      setShared(false)
      await load()
    } catch {
      toast.error("Could not save the view")
    } finally {
      setSaving(false)
    }
  }, [workspaceId, name, shared, filters, load])

  const remove = useCallback(
    async (view: SavedView) => {
      if (!workspaceId) return
      try {
        const res = await apiFetch(
          `/api/v1/saved-views/${encodeURIComponent(view.id)}?workspace_id=${encodeURIComponent(workspaceId)}`,
          { method: "DELETE" },
        )
        if (!res.ok) {
          // The API lets everyone SEE a shared view and only its owner
          // delete it, so this is the expected answer for someone else's.
          toast.error(
            res.status === 403
              ? "Only the owner can delete this view"
              : "Could not delete the view",
          )
          return
        }
        await load()
      } catch {
        toast.error("Could not delete the view")
      }
    },
    [workspaceId, load],
  )

  return (
    <div className="flex items-center gap-1.5 px-4 py-1.5 border-b border-border/60 shrink-0">
      <DropdownMenu open={open} onOpenChange={setOpen}>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label="Saved views"
            className={cn(
              "inline-flex items-center gap-1.5 h-6 px-2 rounded-md border text-[11px] transition-colors",
              active
                ? "border-primary/40 bg-primary/10 text-primary-hover"
                : "border-border/60 bg-card/50 text-muted-foreground hover:text-foreground",
            )}
          >
            <Bookmark className="h-3 w-3" />
            <span className="max-w-[16rem] truncate">{active ? active.name : "Saved views"}</span>
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-64">
          <DropdownMenuLabel className="text-[10px] uppercase tracking-wider text-muted-foreground/70">
            Saved views
          </DropdownMenuLabel>
          {views.length === 0 && (
            <div className="px-2 py-1.5 text-[11px] text-muted-foreground/70">
              No saved views yet. Filter the journal, then save it.
            </div>
          )}
          {decoded.map(({ view, params }) => (
            <DropdownMenuItem
              key={view.id}
              className="text-xs gap-1.5"
              onSelect={() => {
                onApply(params)
                setOpen(false)
              }}
            >
              {active?.id === view.id ? (
                <Check className="h-3 w-3 text-primary" />
              ) : (
                <Save className="h-3 w-3 text-muted-foreground/50" />
              )}
              <span className="truncate">{view.name}</span>
              {view.shared && (
                <span className="ml-auto text-[9px] text-foreground/40 uppercase">shared</span>
              )}
              <button
                type="button"
                aria-label={`Delete view ${view.name}`}
                className="ml-1 text-muted-foreground/50 hover:text-destructive"
                onClick={(e) => {
                  e.preventDefault()
                  e.stopPropagation()
                  void remove(view)
                }}
              >
                <Trash2 className="h-3 w-3" />
              </button>
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuItem
            className="text-xs gap-1.5"
            disabled={!hasFilters || !workspaceId}
            onSelect={() => {
              setOpen(false)
              setSaveOpen(true)
            }}
          >
            <Bookmark className="h-3 w-3 text-muted-foreground/50" />
            {hasFilters ? "Save current view…" : "Filter something to save a view"}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      {active && (
        <button
          type="button"
          onClick={() => onApply({})}
          className="text-[10px] font-mono text-muted-foreground/60 hover:text-foreground"
        >
          clear
        </button>
      )}

      <Dialog open={saveOpen} onOpenChange={setSaveOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Save this view</DialogTitle>
            <DialogDescription>
              Stores the current tab, time range, scope, severity and search so you can come
              back to it — or hand it to the crew.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="journal-view-name" className="text-xs">
                Name
              </Label>
              <Input
                id="journal-view-name"
                value={name}
                autoFocus
                placeholder="Failed nightly digests"
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && name.trim() && !saving) void create()
                }}
              />
            </div>
            <label className="flex items-center gap-2 text-xs text-muted-foreground">
              <Checkbox
                checked={shared}
                onCheckedChange={(v) => setShared(v === true)}
                aria-label="Share with the workspace"
              />
              Share with the workspace
            </label>
            <pre className="text-[10px] font-mono text-muted-foreground/70 bg-muted/40 rounded-md p-2 overflow-x-auto">
              {Object.entries(filters)
                .map(([k, v]) => `${k}=${v}`)
                .join("\n") || "(no filters)"}
            </pre>
          </div>
          <DialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setSaveOpen(false)}>
              Cancel
            </Button>
            <Button size="sm" disabled={!name.trim() || saving} onClick={() => void create()}>
              {saving && <Loader2 className="h-3 w-3 mr-1 animate-spin" />}
              Save view
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
