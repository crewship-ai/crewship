"use client"

// The routine kebab: the verbs.
//
// It shipped carrying Disable and Close, which is a button that promises
// actions and delivers housekeeping. Everything here does something
// real — nothing is listed that the API cannot perform, because a menu
// item that no-ops is the same lie as a save button that saves nothing.
//
// Rename and Duplicate go through /pipelines/save, the same path the
// code editor uses, because `display_name` and `description` live in
// the definition rather than in columns of their own. Export hits the
// export endpoint that already existed and was unreachable from the UI.

import * as React from "react"
import {
  Copy,
  Download,
  MoreHorizontal,
  Pencil,
  Power,
  PowerOff,
  Type,
  X,
} from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { apiFetch } from "@/lib/api-fetch"
import { extractProblemDetail } from "@/lib/problem-details"
import {
  duplicatePayload,
  renamePayload,
  type RoutineSaveBody,
} from "@/lib/routine-save-payload"
import type { RoutineDetail } from "./routines-detail-panel"

/**
 * Server's own words when it refuses, or a fallback.
 *
 * A 403 here is expected — skip_test_gate is OWNER/ADMIN only — and the
 * server explains why. Swallowing that for a generic "Save failed"
 * turns a clear permissions message into a mystery.
 */
async function problemMessage(res: Response, fallback: string): Promise<string> {
  try {
    return extractProblemDetail(await res.clone().json()) ?? fallback
  } catch {
    return fallback
  }
}

interface Props {
  routine: RoutineDetail
  workspaceId: string
  onEditCode: () => void
  onChanged: () => void
  onClose: () => void
  /** Enable / Disable, when the viewer's role allows it. */
  lifecycle?: "active" | "proposed" | "disabled"
  showKillControl?: boolean
  onGovernance?: (action: "disable" | "enable") => void
  governanceBusy?: boolean
}

type OpenDialog = "rename" | "duplicate" | null

export function RoutineActionsMenu({
  routine,
  workspaceId,
  onEditCode,
  onChanged,
  onClose,
  lifecycle = "active",
  showKillControl = false,
  onGovernance,
  governanceBusy,
}: Props) {
  const [dialog, setDialog] = React.useState<OpenDialog>(null)
  const [busy, setBusy] = React.useState(false)

  const definition = routine.definition as Record<string, unknown>
  const [displayName, setDisplayName] = React.useState(
    (definition?.display_name as string) ?? routine.name ?? "",
  )
  const [description, setDescription] = React.useState(routine.description ?? "")
  const [copyName, setCopyName] = React.useState(`${routine.name ?? routine.slug} (copy)`)

  // Re-seed the drafts whenever the dialog opens, so a cancelled edit
  // does not survive into the next one.
  const openDialog = (which: OpenDialog) => {
    if (which === "rename") {
      setDisplayName((definition?.display_name as string) ?? routine.name ?? "")
      setDescription(routine.description ?? "")
    }
    if (which === "duplicate") setCopyName(`${routine.name ?? routine.slug} (copy)`)
    setDialog(which)
  }

  /** POST a prepared body to the save endpoint. */
  const post = async (body: RoutineSaveBody, successMsg: string) => {
    setBusy(true)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/save`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      )
      if (!res.ok) {
        toast.error(await problemMessage(res, "Save failed"))
        return false
      }
      toast.success(successMsg)
      setDialog(null)
      onChanged()
      return true
    } catch {
      toast.error("Save failed")
      return false
    } finally {
      setBusy(false)
    }
  }

  const source = {
    slug: routine.slug,
    name: routine.name,
    description: routine.description,
    definition,
    author_crew_id: routine.author_crew_id,
  }

  const submitRename = () =>
    post(renamePayload(source, { name: displayName, description }), "Renamed")

  const submitDuplicate = () => {
    if (!copyName.trim()) return
    return post(duplicatePayload(source, { name: copyName }), "Duplicated")
  }

  const exportBundle = async () => {
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(routine.slug)}/export`,
      )
      if (!res.ok) {
        toast.error(await problemMessage(res, "Export failed"))
        return
      }
      const text = await res.text()
      // Download rather than copy: a bundle is a file you hand to
      // `crewship routine validate` or check into a repo, not something
      // anyone wants sitting in their clipboard.
      const url = URL.createObjectURL(new Blob([text], { type: "application/json" }))
      const a = document.createElement("a")
      a.href = url
      a.download = `${routine.slug}.json`
      a.click()
      URL.revokeObjectURL(url)
      toast.success("Bundle exported")
    } catch {
      toast.error("Export failed")
    }
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label="More actions"
            className="rounded-lg border border-border/60 p-1.5 text-muted-foreground transition-colors hover:text-foreground"
          >
            <MoreHorizontal className="h-3.5 w-3.5" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuItem onSelect={onEditCode}>
            <Pencil className="h-3.5 w-3.5" />
            Edit definition
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => openDialog("rename")}>
            <Type className="h-3.5 w-3.5" />
            Rename &amp; description
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => openDialog("duplicate")}>
            <Copy className="h-3.5 w-3.5" />
            Duplicate
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={exportBundle}>
            <Download className="h-3.5 w-3.5" />
            Export bundle
          </DropdownMenuItem>
          {showKillControl && lifecycle !== "proposed" && onGovernance && (
            <>
              <DropdownMenuSeparator />
              {lifecycle === "disabled" ? (
                <DropdownMenuItem
                  onSelect={() => onGovernance("enable")}
                  disabled={governanceBusy}
                >
                  <Power className="h-3.5 w-3.5" />
                  Enable routine
                </DropdownMenuItem>
              ) : (
                <DropdownMenuItem
                  variant="destructive"
                  onSelect={() => onGovernance("disable")}
                  disabled={governanceBusy}
                >
                  <PowerOff className="h-3.5 w-3.5" />
                  Disable routine
                </DropdownMenuItem>
              )}
            </>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={onClose}>
            <X className="h-3.5 w-3.5" />
            Close
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={dialog === "rename"} onOpenChange={(o) => !o && setDialog(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rename &amp; description</DialogTitle>
            <DialogDescription>
              The slug <span className="font-mono">{routine.slug}</span> does not change — every
              reference to this routine keeps working.
            </DialogDescription>
          </DialogHeader>
          <label className="block space-y-1.5">
            <span className="text-[11px] uppercase tracking-wider text-muted-foreground">Name</span>
            <input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              className="w-full rounded-md border border-white/[0.1] bg-background px-2.5 py-2 text-[13px]"
            />
          </label>
          <label className="block space-y-1.5">
            <span className="text-[11px] uppercase tracking-wider text-muted-foreground">
              Description
            </span>
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="h-20 w-full resize-none rounded-md border border-white/[0.1] bg-background px-2.5 py-2 text-[13px] leading-relaxed"
            />
          </label>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialog(null)} disabled={busy}>
              Cancel
            </Button>
            <Button onClick={submitRename} disabled={busy || !displayName.trim()} className="gap-1.5">
              {busy && <Spinner className="h-3.5 w-3.5" />}
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={dialog === "duplicate"} onOpenChange={(o) => !o && setDialog(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Duplicate routine</DialogTitle>
            <DialogDescription>
              Copies the definition under a new name. Schedules, webhooks and run history stay with
              the original.
            </DialogDescription>
          </DialogHeader>
          <label className="block space-y-1.5">
            <span className="text-[11px] uppercase tracking-wider text-muted-foreground">
              New name
            </span>
            <input
              value={copyName}
              onChange={(e) => setCopyName(e.target.value)}
              className="w-full rounded-md border border-white/[0.1] bg-background px-2.5 py-2 text-[13px]"
            />
          </label>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialog(null)} disabled={busy}>
              Cancel
            </Button>
            <Button onClick={submitDuplicate} disabled={busy || !copyName.trim()} className="gap-1.5">
              {busy && <Spinner className="h-3.5 w-3.5" />}
              Duplicate
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
