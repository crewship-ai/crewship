"use client"

import {
  LayoutGrid, List, Bookmark, Save,
} from "lucide-react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { cn } from "@/lib/utils"
import type { SavedView } from "@/lib/types/mission"
import { Spinner } from "@/components/ui/spinner"

/** 1015 → "1 015": a count a person reads, not a locale-dependent one. */
export function formatCount(n: number): string {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, " ")
}

/**
 * How much of the list is on screen, from the server's total — null when the
 * page IS the list, or the total is not known yet. The board used to print
 * the length of the page it received as if it were the workspace.
 */
export function issuesShowingLabel(loaded: number, total: number | null): string | null {
  if (total == null || total <= loaded) return null
  return `Showing newest ${formatCount(loaded)} of ${formatCount(total)}`
}

export interface IssuesToolbarStripProps {
  issueViewMode: "board" | "list"
  onViewModeChange: (mode: "board" | "list") => void
  savedViews: SavedView[]
  savedViewsOpen: boolean
  onSavedViewsOpenChange: (open: boolean) => void
  activeViewId: string | null
  onActiveViewChange: (id: string | null, viewType?: "board" | "list") => void
  /** Paging facts from usePagedList: what is loaded, the server's total, and a way to get more. */
  loaded?: number
  total?: number | null
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
}

/** Toolbar strip for the issues center panel — view mode toggle + saved views dropdown */
export function IssuesToolbarStrip({
  issueViewMode,
  onViewModeChange,
  savedViews,
  savedViewsOpen,
  onSavedViewsOpenChange,
  activeViewId,
  onActiveViewChange,
  loaded = 0,
  total = null,
  hasMore = false,
  loadingMore = false,
  onLoadMore,
}: IssuesToolbarStripProps) {
  const showing = issuesShowingLabel(loaded, total)
  return (
    <div className="flex items-center gap-2 px-4 py-2 border-b border-white/[0.06] shrink-0">
      <div className="flex gap-1 bg-white/[0.04] rounded-md p-0.5" role="group" aria-label="View mode">
        <button
          type="button"
          aria-label="Board view"
          aria-pressed={issueViewMode === "board"}
          onClick={() => onViewModeChange("board")}
          className={cn("p-1.5 rounded", issueViewMode === "board" ? "bg-white/[0.1] text-foreground" : "text-muted-foreground")}
        >
          <LayoutGrid className="h-3.5 w-3.5" />
        </button>
        <button
          type="button"
          aria-label="List view"
          aria-pressed={issueViewMode === "list"}
          onClick={() => onViewModeChange("list")}
          className={cn("p-1.5 rounded", issueViewMode === "list" ? "bg-white/[0.1] text-foreground" : "text-muted-foreground")}
        >
          <List className="h-3.5 w-3.5" />
        </button>
      </div>

      {/* Saved views dropdown */}
      {savedViews.length > 0 && (
        <DropdownMenu open={savedViewsOpen} onOpenChange={onSavedViewsOpenChange}>
          <DropdownMenuTrigger asChild>
            <button className="flex items-center gap-1.5 px-2 py-1 rounded-md text-xs hover:bg-white/[0.06] text-muted-foreground transition-colors">
              <Bookmark className="h-3 w-3" />
              <span>{activeViewId ? savedViews.find((v) => v.id === activeViewId)?.name || "Saved Views" : "Saved Views"}</span>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-52">
            <DropdownMenuItem
              onClick={() => { onActiveViewChange(null); onSavedViewsOpenChange(false) }}
              className={cn("text-xs", !activeViewId && "bg-white/[0.04]")}
            >
              All Issues
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            {savedViews.map((view) => (
              <DropdownMenuItem
                key={view.id}
                onClick={() => {
                  if (view.view_type === "board" || view.view_type === "list") {
                    onActiveViewChange(view.id, view.view_type)
                  } else {
                    onActiveViewChange(view.id)
                  }
                  onSavedViewsOpenChange(false)
                }}
                className={cn("text-xs", activeViewId === view.id && "bg-white/[0.04]")}
              >
                <Save className="h-3 w-3 mr-1.5 text-muted-foreground/50" />
                {view.name}
                {view.shared && (
                  <span className="ml-auto text-[9px] text-foreground/40 uppercase">shared</span>
                )}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}

      <div className="flex-1" />
      {/* The list is bigger than the page: say so, and offer the next page.
          Silent when the page is the list. */}
      {showing && (
        <span className="flex items-center gap-2 font-mono text-[11px] text-muted-foreground" data-testid="issues-showing">
          {showing}
          {hasMore && onLoadMore && (
            <button
              type="button"
              onClick={onLoadMore}
              disabled={loadingMore}
              className="inline-flex items-center gap-1 text-primary-hover hover:underline disabled:opacity-60"
            >
              {loadingMore && <Spinner className="h-3 w-3" />}
              Load 100 more
            </button>
          )}
        </span>
      )}
    </div>
  )
}
