"use client"

import { useState } from "react"
import { LayoutGrid, List, Plus, Search, Tag } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Checkbox } from "@/components/ui/checkbox"
import { LabelsDialog } from "./labels-dialog"
import { cn } from "@/lib/utils"
import type { IssueLabel, IssuePriority, MissionStatus } from "@/lib/types/mission"

export interface IssuesFilters {
  status: MissionStatus[]
  priority: IssuePriority[]
  crew_id: string
  search: string
}

interface IssuesToolbarProps {
  viewMode: "board" | "list"
  onViewModeChange: (mode: "board" | "list") => void
  filters: IssuesFilters
  onFiltersChange: (filters: IssuesFilters) => void
  onCreateClick: () => void
  labels: IssueLabel[]
  workspaceId: string
  onLabelsChanged: () => void
}

const STATUS_OPTIONS: { value: MissionStatus; label: string }[] = [
  { value: "BACKLOG", label: "Backlog" },
  { value: "TODO", label: "Todo" },
  { value: "IN_PROGRESS", label: "In Progress" },
  { value: "REVIEW", label: "In Review" },
  { value: "COMPLETED", label: "Done" },
  { value: "FAILED", label: "Failed" },
  { value: "CANCELLED", label: "Cancelled" },
]

const PRIORITY_OPTIONS: { value: IssuePriority; label: string }[] = [
  { value: "urgent", label: "Urgent" },
  { value: "high", label: "High" },
  { value: "medium", label: "Medium" },
  { value: "low", label: "Low" },
  { value: "none", label: "No priority" },
]

export function IssuesToolbar({
  viewMode,
  onViewModeChange,
  filters,
  onFiltersChange,
  onCreateClick,
  labels,
  workspaceId,
  onLabelsChanged,
}: IssuesToolbarProps) {
  const [labelsDialogOpen, setLabelsDialogOpen] = useState(false)
  function toggleStatus(status: MissionStatus) {
    const current = filters.status
    const next = current.includes(status)
      ? current.filter((s) => s !== status)
      : [...current, status]
    onFiltersChange({ ...filters, status: next })
  }

  function togglePriority(priority: IssuePriority) {
    const current = filters.priority
    const next = current.includes(priority)
      ? current.filter((p) => p !== priority)
      : [...current, priority]
    onFiltersChange({ ...filters, priority: next })
  }

  return (
    <div className="flex items-center gap-2 flex-wrap">
      {/* View mode toggle */}
      <div className="flex items-center rounded-md border border-border bg-muted/30 p-0.5">
        <Button
          variant="ghost"
          size="icon"
          className={cn(
            "h-7 w-7 rounded-sm",
            viewMode === "board" && "bg-background shadow-sm",
          )}
          onClick={() => onViewModeChange("board")}
          aria-label="Board view"
          aria-pressed={viewMode === "board"}
        >
          <LayoutGrid className="h-3.5 w-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className={cn(
            "h-7 w-7 rounded-sm",
            viewMode === "list" && "bg-background shadow-sm",
          )}
          onClick={() => onViewModeChange("list")}
          aria-label="List view"
          aria-pressed={viewMode === "list"}
        >
          <List className="h-3.5 w-3.5" />
        </Button>
      </div>

      {/* Search */}
      <div className="relative flex-1 min-w-[200px] max-w-[320px]">
        <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
        <Input
          placeholder="Search issues..."
          value={filters.search}
          onChange={(e) =>
            onFiltersChange({ ...filters, search: e.target.value })
          }
          className="h-8 pl-8 text-sm"
        />
      </div>

      {/* Status filter */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" size="sm" className="h-8 text-xs gap-1">
            Status
            {filters.status.length > 0 && (
              <span className="ml-0.5 rounded-full bg-primary/10 text-primary px-1.5 text-[10px] font-medium">
                {filters.status.length}
              </span>
            )}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-48">
          {STATUS_OPTIONS.map((opt) => (
            <DropdownMenuItem
              key={opt.value}
              onSelect={(e) => e.preventDefault()}
              onClick={() => toggleStatus(opt.value)}
              className="gap-2"
            >
              <Checkbox
                checked={filters.status.includes(opt.value)}
                className="pointer-events-none"
              />
              <span className="text-sm">{opt.label}</span>
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>

      {/* Priority filter */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" size="sm" className="h-8 text-xs gap-1">
            Priority
            {filters.priority.length > 0 && (
              <span className="ml-0.5 rounded-full bg-primary/10 text-primary px-1.5 text-[10px] font-medium">
                {filters.priority.length}
              </span>
            )}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-48">
          {PRIORITY_OPTIONS.map((opt) => (
            <DropdownMenuItem
              key={opt.value}
              onSelect={(e) => e.preventDefault()}
              onClick={() => togglePriority(opt.value)}
              className="gap-2"
            >
              <Checkbox
                checked={filters.priority.includes(opt.value)}
                className="pointer-events-none"
              />
              <span className="text-sm">{opt.label}</span>
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>

      {/* Labels management */}
      <Button
        variant="outline"
        size="sm"
        className="h-8 text-xs gap-1"
        onClick={() => setLabelsDialogOpen(true)}
      >
        <Tag className="h-3.5 w-3.5" />
        Labels
      </Button>

      <div className="flex-1" />

      {/* Create button */}
      <Button size="sm" className="h-8 gap-1.5" onClick={onCreateClick}>
        <Plus className="h-3.5 w-3.5" />
        New Issue
      </Button>

      <LabelsDialog
        open={labelsDialogOpen}
        onOpenChange={setLabelsDialogOpen}
        labels={labels}
        workspaceId={workspaceId}
        onLabelsChanged={onLabelsChanged}
      />
    </div>
  )
}
