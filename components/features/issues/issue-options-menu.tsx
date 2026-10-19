"use client"

// The issue page's ⋯ menu (v67 `H.POPS.imenu`, issues.js:261): Copy link,
// Move to project…, Cancel issue…, Delete issue….
//
// Duplicate issue is the existing create call with the title, brief, priority,
// project and labels of this issue (a copy starts in Backlog, with nobody
// holding it). Left out on purpose: Notify me and Mark as duplicate of
// (no backend yet, #3459), Bind a routine and Convert to sub-issue (the
// properties already carry Routine and Parent), Take over and Hand off
// (Work & control carries them, with their revision contract).

import * as React from "react"
import { Archive, Copy, FolderKanban, Link2, MoreHorizontal, Trash2 } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import { cn } from "@/lib/utils"
import { getCrewIconDef, iconColorProps } from "@/lib/entities"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { SubBarIconButton } from "@/components/layout/sub-bar"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"
import type { IssueAction } from "@/lib/chat-entity-cards"
import type { Mission, Project } from "@/lib/types/mission"

/** The server deletes only BACKLOG and CANCELLED issues (issue_handler_update.go). */
const DELETABLE = new Set(["BACKLOG", "CANCELLED"])
const CLOSED = new Set(["DONE", "CANCELLED", "DUPLICATE", "COMPLETED"])

async function problem(res: Response, fallback: string): Promise<string> {
  const b = (await res.json().catch(() => null)) as { detail?: string; error?: string } | null
  return b?.detail || b?.error || fallback
}

export function IssueOptionsMenu({
  issue,
  projects,
  workspaceId,
  onChanged,
  onDeleted,
  onDuplicated,
}: {
  issue: Mission
  projects: Project[]
  workspaceId: string
  /** A write landed; the page reloads the issue. */
  onChanged: () => void
  /** The issue is gone; the page leaves. */
  onDeleted: () => void
  /** A copy was created; the page opens it. */
  onDuplicated?: (identifier: string) => void
}) {
  const [dialog, setDialog] = React.useState<"move" | "cancel" | "delete" | null>(null)
  const id = issue.identifier ?? issue.id
  const url = `/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(id)}?workspace_id=${encodeURIComponent(workspaceId)}`
  const allowed = issue.allowed_actions
  const can = (a: IssueAction) => !allowed || allowed.includes(a)
  // The server grants "delete" with the other write actions and then drops it
  // while the issue is not Backlog or Cancelled (issue_chat_metadata.go
  // issueActionsForState). A writer still sees the row, disabled with why.
  const deletable = DELETABLE.has(String(issue.status))
  const showDelete = can("delete") || (!deletable && can("edit_fields"))

  const copyLink = () => {
    const link = `${window.location.origin}/issues/${encodeURIComponent(id)}`
    navigator.clipboard?.writeText(link).then(() => toast.success("Link copied"), () => toast.error("Could not copy the link"))
  }

  const patch = async (body: Record<string, unknown>, done: string) => {
    const res = await apiFetch(url, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
    if (!res.ok) {
      toast.error(await problem(res, "The issue was not changed."))
      return false
    }
    toast.success(done)
    onChanged()
    return true
  }

  const duplicate = async () => {
    const res = await apiFetch(`/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues?workspace_id=${encodeURIComponent(workspaceId)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        title: `${issue.title} (copy)`,
        ...(issue.description ? { description: issue.description } : {}),
        ...(issue.priority && issue.priority !== "none" ? { priority: issue.priority } : {}),
        ...(issue.project_id ? { project_id: issue.project_id } : {}),
        ...(issue.labels?.length ? { labels: issue.labels.map((l) => l.id) } : {}),
      }),
    })
    if (!res.ok) {
      toast.error(await problem(res, "The issue was not duplicated."))
      return
    }
    const created = (await res.json().catch(() => null)) as { identifier?: string | null; id?: string } | null
    const next = created?.identifier ?? created?.id
    toast.success(next ? `${next} created from ${id}` : `${id} duplicated`)
    if (next) onDuplicated?.(next)
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <SubBarIconButton icon={MoreHorizontal} aria-label="Issue options" title="Issue options" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuLabel className="font-mono text-label text-muted-foreground">{id}</DropdownMenuLabel>
          <DropdownMenuItem onSelect={copyLink}>
            <Link2 className="size-3.5" aria-hidden /> Copy link
          </DropdownMenuItem>
          {can("edit_fields") && (
            <DropdownMenuItem onSelect={() => void duplicate()}>
              <Copy className="size-3.5" aria-hidden /> Duplicate issue
            </DropdownMenuItem>
          )}
          {can("edit_fields") && (
            <DropdownMenuItem onSelect={() => setDialog("move")}>
              <FolderKanban className="size-3.5" aria-hidden /> Move to project…
            </DropdownMenuItem>
          )}
          {(can("change_status") || showDelete) && <DropdownMenuSeparator />}
          {can("change_status") && !CLOSED.has(String(issue.status)) && (
            <DropdownMenuItem onSelect={() => setDialog("cancel")}>
              <Archive className="size-3.5" aria-hidden /> Cancel issue…
            </DropdownMenuItem>
          )}
          {showDelete && (
            <DropdownMenuItem
              variant="destructive"
              disabled={!deletable}
              title={deletable ? undefined : "Only Backlog or Cancelled issues can be deleted. Cancel it first."}
              onSelect={() => setDialog("delete")}
            >
              <Trash2 className="size-3.5" aria-hidden /> Delete issue…
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <MoveToProjectDialog
        open={dialog === "move"}
        onOpenChange={(o) => setDialog(o ? "move" : null)}
        issue={issue}
        projects={projects}
        onMove={async (projectId) => {
          const name = projects.find((p) => p.id === projectId)?.name
          return patch({ project_id: projectId }, projectId ? `${id} moved to ${name ?? "the project"}` : `${id} is out of its project`)
        }}
      />

      {dialog === "cancel" && (
        <ConfirmDialog
          open
          onOpenChange={(o) => { if (!o) setDialog(null) }}
          title={`Cancel ${id}?`}
          description="The issue moves to Cancelled and stops counting as open work. You can reopen it from its status."
          confirmLabel="Cancel issue"
          onConfirm={async () => {
            setDialog(null)
            await patch({ status: "CANCELLED" }, `${id} is cancelled`)
          }}
        />
      )}

      {dialog === "delete" && (
        <ConfirmDialog
          open
          onOpenChange={(o) => { if (!o) setDialog(null) }}
          title={`Delete ${id}?`}
          description="Permanently removes this issue, its comments and attachments. This cannot be undone."
          destructive
          confirmLabel="Delete issue"
          onConfirm={async () => {
            setDialog(null)
            const res = await apiFetch(url, { method: "DELETE" })
            if (!res.ok) {
              toast.error(await problem(res, "The issue was not deleted."))
              return
            }
            toast.success(`${id} deleted`)
            onDeleted()
          }}
        />
      )}
    </>
  )
}

function MoveToProjectDialog({
  open,
  onOpenChange,
  issue,
  projects,
  onMove,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  issue: Mission
  projects: Project[]
  onMove: (projectId: string) => Promise<boolean>
}) {
  const [pick, setPick] = React.useState<string>(issue.project_id ?? "")
  const [busy, setBusy] = React.useState(false)
  React.useEffect(() => {
    if (open) setPick(issue.project_id ?? "")
  }, [open, issue.project_id])
  const id = issue.identifier ?? issue.id
  const options = [{ id: "", name: "No project", icon: "folder", color: "" } as Pick<Project, "id" | "name" | "icon" | "color">, ...projects]

  const move = async () => {
    if (busy || pick === (issue.project_id ?? "")) return
    setBusy(true)
    try {
      if (await onMove(pick)) onOpenChange(false)
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface open={open} onOpenChange={(o) => !busy && onOpenChange(o)} size="sm" onSubmit={() => void move()}>
      <CreateSurfaceHeader concept="issues" title={`Move ${id} to a project`} description="Pick the project this issue belongs to." onClose={() => !busy && onOpenChange(false)} />
      <CreateSurfaceBody>
        <div role="radiogroup" aria-label="Project" className="flex max-h-72 flex-col overflow-y-auto">
          {options.map((p) => {
            const Icon = getCrewIconDef(p.icon || "folder").icon
            const color = iconColorProps(p.color)
            const on = pick === p.id
            return (
              <button
                key={p.id || "none"}
                type="button"
                role="radio"
                aria-checked={on}
                onClick={() => setPick(p.id)}
                className={cn(
                  "kit-tap flex items-center gap-2 rounded-md px-2 py-2 text-left text-body transition-colors",
                  on ? "bg-primary/10 text-primary-hover" : "hover:bg-foreground/[0.06]",
                )}
              >
                <Icon className={cn("size-3.5 shrink-0", p.id ? color.className : "text-muted-foreground")} style={p.id ? color.style : undefined} aria-hidden />
                <span className="truncate">{p.name}</span>
              </button>
            )
          })}
        </div>
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={busy ? "Moving…" : "Move"}
        onPrimary={() => void move()}
        primaryDisabled={pick === (issue.project_id ?? "")}
        busy={busy}
      />
    </CreateSurface>
  )
}
