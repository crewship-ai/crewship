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

export interface IssuesToolbarStripProps {
  issueViewMode: "board" | "list"
  onViewModeChange: (mode: "board" | "list") => void
  savedViews: SavedView[]
  savedViewsOpen: boolean
  onSavedViewsOpenChange: (open: boolean) => void
  activeViewId: string | null
  onActiveViewChange: (id: string | null, viewType?: "board" | "list") => void
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
}: IssuesToolbarStripProps) {
  return (
    <div className="flex items-center gap-2 px-4 py-2 border-b border-white/[0.06] shrink-0">
      <div className="flex gap-1 bg-white/[0.04] rounded-md p-0.5">
        <button
          onClick={() => onViewModeChange("board")}
          className={cn("p-1.5 rounded", issueViewMode === "board" ? "bg-white/[0.1] text-foreground" : "text-muted-foreground")}
        >
          <LayoutGrid className="h-3.5 w-3.5" />
        </button>
        <button
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
    </div>
  )
}
