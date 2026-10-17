"use client"

import { useCallback, useMemo, useState } from "react"
import { Plus, X } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import type { IssuePriority, Mission, Project } from "@/lib/types/mission"
import { IssueRow } from "./issue-row"
import { PriorityIcon } from "./priority-icon"
import { StatusIcon } from "./status-icon"
import {
  DEFAULT_ROW_FIELDS,
  PRIORITY_TEXT,
  STATUS_TEXT,
  groupIssues,
  type GroupBy,
  type RowFields,
} from "./issues-view-model"

interface IssuesListViewProps {
  /** Already filtered and ordered. */
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
  /** The issue the peek shows. */
  selectedIssueId?: string | null
  onBulkAction?: (ids: string[], updates: Record<string, unknown>) => void
  workspaceId?: string | null
  groupBy?: GroupBy
  fields?: RowFields
  /** Projects the bulk bar can move issues into. */
  projects?: Project[]
  /** A group header's +. Gets the group's status when grouped by status. */
  onCreate?: (status?: string) => void
  /** After the server accepted a bulk change. */
  onBulkApplied?: () => void
}

// DONE, not the retired COMPLETED: the bulk endpoint validates each move
// against the issue transition table, which only knows DONE.
export const BULK_STATUSES = ["BACKLOG", "TODO", "IN_PROGRESS", "REVIEW", "DONE", "CANCELLED"] as const
const BULK_PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

type Pending = { updates: Record<string, unknown>; label: string; field: "status" | "priority" | "project" }

/**
 * The list: grouped 38 px rows under sticky group headers, and a bulk bar at
 * the bottom while issues are selected. A bulk change asks first, and a
 * refused one keeps both the selection and the server's reason on screen.
 */
export function IssuesListView({
  issues,
  onIssueClick,
  selectedIssueId,
  onBulkAction,
  workspaceId,
  groupBy = "status",
  fields = DEFAULT_ROW_FIELDS,
  projects = [],
  onCreate,
  onBulkApplied,
}: IssuesListViewProps) {
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const [bulkError, setBulkError] = useState<string | null>(null)
  const [pending, setPending] = useState<Pending | null>(null)

  const groups = useMemo(() => groupIssues(issues, groupBy), [issues, groupBy])

  // Selecting something else makes the last refusal describe rows that are no
  // longer the selection, so it goes with it.
  const setChecked = useCallback((id: string, on: boolean) => {
    setBulkError(null)
    setSelectedIds((prev) => {
      const next = new Set(prev)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }, [])

  const selectAll = useCallback(() => {
    setBulkError(null)
    setSelectedIds(new Set(issues.map((i) => i.id)))
  }, [issues])

  const clearSelection = useCallback(() => {
    setSelectedIds(new Set())
    setBulkError(null)
  }, [])

  const handleBulkUpdate = useCallback(
    async (updates: Record<string, unknown>) => {
      if (selectedIds.size === 0) return
      const ids = Array.from(selectedIds)
      if (onBulkAction) {
        onBulkAction(ids, updates)
        clearSelection()
        return
      }
      if (!workspaceId) return
      setBulkError(null)
      const refuse = (message: string) => {
        setBulkError(message)
        toast.error(message)
      }
      try {
        const res = await apiFetch(`/api/v1/issues/bulk?workspace_id=${encodeURIComponent(workspaceId)}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ ids, updates }),
        })
        if (!res.ok) {
          const body = (await res.json().catch(() => null)) as { error?: unknown; detail?: unknown } | null
          // The server knows which of the selected issues it refused and why.
          const said = [body?.error, body?.detail].find((m) => typeof m === "string" && m.trim())
          refuse(
            typeof said === "string"
              ? said
              : `Could not update ${ids.length} ${ids.length === 1 ? "issue" : "issues"} (HTTP ${res.status})`,
          )
          return
        }
        // The endpoint skips an issue it cannot change (a status it cannot
        // move to, a project in another workspace) and still answers 200 with
        // how many it did change. Fewer than asked is a partial refusal.
        const done = (await res.json().catch(() => null)) as { updated?: unknown } | null
        if (typeof done?.updated === "number" && done.updated < ids.length) {
          const skipped = ids.length - done.updated
          refuse(
            `${skipped} of ${ids.length} ${ids.length === 1 ? "issue was" : "issues were"} not changed: the server refused the change for ${skipped === 1 ? "it" : "them"}, for example a status the workflow does not allow from where ${skipped === 1 ? "it is" : "they are"}.`,
          )
          onBulkApplied?.()
          return
        }
      } catch (e) {
        refuse(e instanceof Error ? e.message : "Could not reach the server")
        return
      }
      // Only a write the server accepted may drop the selection.
      clearSelection()
      onBulkApplied?.()
    },
    [selectedIds, onBulkAction, workspaceId, clearSelection, onBulkApplied],
  )

  const n = selectedIds.size
  const plural = (k: number) => `${k} ${k === 1 ? "issue" : "issues"}`

  return (
    <div data-slot="issues-list" className="relative min-h-full pb-24">
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(o) => {
          if (!o) setPending(null)
        }}
        title={pending ? (pending.field === "project" ? `Move ${plural(n)} to ${pending.label}?` : `Set ${plural(n)} to ${pending.label}?`) : ""}
        description={
          pending?.field === "status"
            ? "Automations that react to a status change may run for each of them, and their people are notified."
            : pending?.field === "project"
              ? "Each issue leaves the project it is in now."
              : "Each issue's priority changes; the order of the board follows."
        }
        confirmLabel={pending ? (pending.field === "project" ? `Move to ${pending.label}` : `Set to ${pending.label}`) : "Set"}
        onConfirm={() => {
          const p = pending
          setPending(null)
          if (p) void handleBulkUpdate(p.updates)
        }}
      />

      {groups.map((g) => (
        <section key={g.key} aria-label={`${g.label}: ${plural(g.issues.length)}`} data-slot="issue-group">
          <div className="sticky top-0 z-[1] flex h-9 items-center gap-2 border-b border-border bg-surface-subtle px-4">
            {g.status && <StatusIcon status={g.status} className="size-3.5 shrink-0" />}
            {g.priority && <PriorityIcon priority={g.priority} className="size-3.5 shrink-0" />}
            <b className="truncate text-label font-semibold text-foreground">{g.label}</b>
            <span className="font-mono text-label tabular-nums text-muted-foreground-soft">{g.issues.length}</span>
            <span className="flex-1" />
            {onCreate && (
              <Button
                variant="ghost"
                size="icon-xs"
                onClick={() => onCreate(g.status)}
                aria-label={g.status ? `New issue in ${g.label}` : "New issue"}
              >
                <Plus className="size-3.5" />
              </Button>
            )}
          </div>
          {g.issues.map((issue) => (
            <IssueRow
              key={issue.id}
              issue={issue}
              fields={fields}
              active={selectedIssueId === issue.id}
              checked={selectedIds.has(issue.id)}
              onCheckedChange={(on) => setChecked(issue.id, on)}
              onOpen={onIssueClick}
            />
          ))}
        </section>
      ))}

      {n > 0 && (
        <div className="pointer-events-none sticky bottom-4 z-10 mt-4 flex justify-center px-4">
          <div
            role="toolbar"
            aria-label="Bulk actions"
            className="pointer-events-auto flex max-w-full items-center gap-1 overflow-x-auto rounded-xl bg-foreground py-1.5 pl-3.5 pr-2 text-body text-background shadow-lg"
          >
            <span className="shrink-0 whitespace-nowrap">{n} selected</span>
            {n < issues.length && (
              <Button variant="ghost" size="sm" className="text-background/70 hover:bg-background/15 hover:text-background" onClick={selectAll}>
                Select all {issues.length}
              </Button>
            )}
            <span aria-hidden className="mx-1 h-4 w-px shrink-0 bg-background/25" />
            <BulkMenu label="Status">
              {BULK_STATUSES.map((s) => (
                <DropdownMenuItem key={s} onSelect={() => setPending({ field: "status", updates: { status: s }, label: STATUS_TEXT[s] })}>
                  <StatusIcon status={s} className="size-3.5" />
                  {STATUS_TEXT[s]}
                </DropdownMenuItem>
              ))}
            </BulkMenu>
            <BulkMenu label="Priority">
              {BULK_PRIORITIES.map((p) => (
                <DropdownMenuItem key={p} onSelect={() => setPending({ field: "priority", updates: { priority: p }, label: PRIORITY_TEXT[p] })}>
                  <PriorityIcon priority={p} className="size-3.5" />
                  {PRIORITY_TEXT[p]}
                </DropdownMenuItem>
              ))}
            </BulkMenu>
            {projects.length > 0 && (
              <BulkMenu label="Project">
                {projects.map((p) => (
                  <DropdownMenuItem key={p.id} onSelect={() => setPending({ field: "project", updates: { project_id: p.id }, label: p.name })}>
                    {p.name}
                  </DropdownMenuItem>
                ))}
                <DropdownMenuItem onSelect={() => setPending({ field: "project", updates: { project_id: "" }, label: "No project" })}>
                  No project
                </DropdownMenuItem>
              </BulkMenu>
            )}
            {bulkError && (
              <span role="alert" className="ml-1 min-w-0 max-w-80 truncate text-label text-chip-danger-bg" title={bulkError}>
                {bulkError}
              </span>
            )}
            <span aria-hidden className="mx-1 h-4 w-px shrink-0 bg-background/25" />
            <Button variant="ghost" size="sm" className="gap-1 text-background hover:bg-background/15 hover:text-background" onClick={clearSelection}>
              <X className="size-3.5" aria-hidden />
              Clear
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

function BulkMenu({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" className={cn("shrink-0 text-background hover:bg-background/15 hover:text-background")}>
          {label}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="center" side="top" className="w-48">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
