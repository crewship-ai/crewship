"use client"

import { useCallback, useMemo, useState, type CSSProperties } from "react"
import { MoreHorizontal, Plus } from "lucide-react"

import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import type { Mission } from "@/lib/types/mission"
import { IssueCard } from "./issue-card"
import { StatusIcon } from "./status-icon"
import { isCreatableStatus } from "./issue-constants"
import { BOARD_STATUSES, STATUS_TEXT, normalStatus } from "./issues-view-model"

interface IssuesBoardViewProps {
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
  /** A column's +. Gets the column's status. */
  onCreateClick?: (status?: string) => void
  selectedIssueId?: string | null
  /** Enables drag and drop between columns; the move is a PATCH of the issue's status. */
  workspaceId?: string | null
  /** After the server accepted a move. */
  onMoved?: () => void
}

/**
 * How many cards a column shows before it folds. A backlog of 982 is not
 * five screens of cards; it is six and a count.
 */
export const BOARD_COLUMN_CAP = 6

/** Statuses that set something off (automations, a review): a drop asks first, as the detail does. */
const ASKING = new Set(["DONE", "REVIEW"])

/** Which column an issue sits in. Planning is Todo; the retired COMPLETED is Done. */
function columnOf(i: Mission): string {
  const s = normalStatus(i.status)
  return s === "PLANNING" ? "TODO" : s
}

/** The columns, and what each shows: the first `cap` cards and how many are behind the fold. */
export function foldColumns(issues: Mission[], statuses: readonly string[], cap: number, open: ReadonlySet<string>) {
  const byStatus = new Map<string, Mission[]>()
  for (const s of statuses) byStatus.set(s, [])
  for (const i of issues) byStatus.get(columnOf(i))?.push(i)
  return statuses.map((status) => {
    const all = byStatus.get(status) ?? []
    const shown = open.has(status) ? all : all.slice(0, cap)
    return { status, all, shown, hidden: all.length - shown.length }
  })
}

/**
 * The wide-screen grid template: a column with cards takes a share of the
 * width, an empty one narrows to a 9rem rail that still shows its head and
 * drop target. With nothing or everything populated the columns stay equal.
 */
export function boardColumnTemplate(counts: number[]): string {
  const populated = counts.filter((n) => n > 0).length
  if (populated === 0 || populated === counts.length) return `repeat(${counts.length},minmax(0,1fr))`
  return counts.map((n) => (n > 0 ? "minmax(0,1fr)" : "minmax(0,9rem)")).join(" ")
}

export function IssuesBoardView({ issues, onIssueClick, onCreateClick, selectedIssueId, workspaceId, onMoved }: IssuesBoardViewProps) {
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set())
  const [dropTarget, setDropTarget] = useState<string | null>(null)
  const [pending, setPending] = useState<{ issue: Mission; status: string } | null>(null)
  const toggleOpen = useCallback((status: string) => {
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(status)) next.delete(status)
      else next.add(status)
      return next
    })
  }, [])

  const cols = useMemo(() => foldColumns(issues, BOARD_STATUSES, BOARD_COLUMN_CAP, open), [issues, open])
  const template = useMemo(() => boardColumnTemplate(cols.map((c) => c.all.length)), [cols])
  const canDrag = Boolean(workspaceId)

  const move = useCallback(
    async (issue: Mission, status: string) => {
      if (!workspaceId || !issue.identifier || !issue.crew_id) return
      try {
        const res = await apiFetch(
          `/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(issue.identifier)}?workspace_id=${encodeURIComponent(workspaceId)}`,
          { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ status }) },
        )
        if (!res.ok) {
          // The card stays where it was; the server says why (open sub-issues, a running mission…).
          const b = (await res.json().catch(() => null)) as { error?: string; detail?: string } | null
          toast.error(`${issue.identifier} stays in ${STATUS_TEXT[columnOf(issue)]}: ${b?.detail || b?.error || `HTTP ${res.status}`}`)
          return
        }
        onMoved?.()
      } catch (e) {
        toast.error(`${issue.identifier} was not moved: ${e instanceof Error ? e.message : "the server could not be reached"}`)
      }
    },
    [workspaceId, onMoved],
  )

  /** A drop and the keyboard's Move to share this: same confirmation, same PATCH. */
  const requestMove = (issue: Mission, status: string) => {
    if (columnOf(issue) === status) return
    if (ASKING.has(status)) setPending({ issue, status })
    else void move(issue, status)
  }

  const onDrop = (status: string, e: React.DragEvent) => {
    e.preventDefault()
    setDropTarget(null)
    const id = e.dataTransfer.getData("text/x-crewship-issue")
    const issue = issues.find((i) => i.id === id)
    if (issue) requestMove(issue, status)
  }

  return (
    <>
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(o) => {
          if (!o) setPending(null)
        }}
        title={pending ? `Move ${pending.issue.identifier} to ${STATUS_TEXT[pending.status]}?` : ""}
        description="Automations that react to a status change may run, and the issue's people are notified."
        confirmLabel={pending ? `Move to ${STATUS_TEXT[pending.status]}` : "Move"}
        onConfirm={() => {
          const p = pending
          setPending(null)
          if (p) void move(p.issue, p.status)
        }}
      />
      {/* Six columns that FIT: `minmax(0,1fr)` at 1440, one column per status
          stacked below md. Each column folds at six cards. */}
      <div
        data-slot="issues-board-grid"
        className="grid grid-cols-1 gap-3 px-4 py-3.5 md:grid-cols-3 lg:[grid-template-columns:var(--board-cols)]"
        style={{ "--board-cols": template } as CSSProperties}
      >
        {cols.map((col) => (
          <section
            key={col.status}
            className={cn("flex min-w-0 flex-col gap-2 rounded-card transition-colors", dropTarget === col.status && "bg-primary/[0.06]")}
            aria-label={STATUS_TEXT[col.status]}
            data-empty={col.all.length === 0 ? "true" : "false"}
            onDragOver={canDrag ? (e) => { e.preventDefault(); setDropTarget(col.status) } : undefined}
            onDragLeave={canDrag ? () => setDropTarget((t) => (t === col.status ? null : t)) : undefined}
            onDrop={canDrag ? (e) => onDrop(col.status, e) : undefined}
          >
            <div className="flex items-center gap-2 px-0.5 pb-1 pt-0.5">
              <StatusIcon status={col.status} className="size-3.5 shrink-0" />
              <b className="truncate text-label font-semibold text-foreground">{STATUS_TEXT[col.status]}</b>
              <span className="font-mono text-label tabular-nums text-muted-foreground-soft">{col.all.length}</span>
              <span className="flex-1" />
              {onCreateClick && isCreatableStatus(col.status) && (
                <Button variant="ghost" size="icon-xs" aria-label={`New issue in ${STATUS_TEXT[col.status]}`} onClick={() => onCreateClick(col.status)}>
                  <Plus className="size-3.5" />
                </Button>
              )}
            </div>
            {col.shown.length === 0 ? (
              <p className="rounded-lg border border-dashed border-border p-2.5 text-center text-label text-muted-foreground-soft">
                Drop issues here
              </p>
            ) : (
              col.shown.map((issue) => (
                <div key={issue.id} className="group/card relative">
                  <IssueCard
                    issue={issue}
                    active={selectedIssueId === issue.id}
                    onClick={() => onIssueClick(issue)}
                    draggable={canDrag}
                    onDragStart={(e) => {
                      e.dataTransfer.setData("text/x-crewship-issue", issue.id)
                      e.dataTransfer.effectAllowed = "move"
                    }}
                  />
                  {canDrag && (
                    <MoveToMenu
                      issue={issue}
                      from={col.status}
                      onMove={(status) => requestMove(issue, status)}
                    />
                  )}
                </div>
              ))
            )}
            {col.all.length > BOARD_COLUMN_CAP && (
              <button
                type="button"
                onClick={() => toggleOpen(col.status)}
                className="w-full rounded-lg py-2 text-center font-mono text-label text-primary-hover hover:bg-[var(--row-hover-bg)]"
                data-testid={`board-fold-${col.status}`}
              >
                {col.hidden > 0 ? `${col.hidden} more · Show all` : "Show fewer"}
              </button>
            )}
          </section>
        ))}
      </div>
    </>
  )
}

/**
 * The keyboard path for a drag: a small menu on the card's corner, shown on
 * hover or focus and always under a coarse pointer, listing the other columns.
 */
function MoveToMenu({ issue, from, onMove }: { issue: Mission; from: string; onMove: (status: string) => void }) {
  const label = issue.identifier || issue.title
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label={`Move ${label} to…`}
          title="Move to…"
          className="absolute bottom-1.5 right-1.5 bg-card opacity-0 focus-visible:opacity-100 group-hover/card:opacity-100 group-focus-within/card:opacity-100 data-[state=open]:opacity-100 pointer-coarse:opacity-100"
        >
          <MoreHorizontal className="size-3.5" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        <DropdownMenuLabel className="text-label text-muted-foreground">Move to</DropdownMenuLabel>
        {BOARD_STATUSES.filter((s) => s !== from).map((s) => (
          <DropdownMenuItem key={s} onSelect={() => onMove(s)}>
            <StatusIcon status={s} className="size-3.5" />
            {STATUS_TEXT[s]}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
