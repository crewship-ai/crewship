"use client"

import { useCallback, useState } from "react"
import { Download, ShieldAlert, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { apiFetch } from "@/lib/api-fetch"

/**
 * What can be done to one person's data: hand it over, or erase it.
 *
 * These used to live behind their own nav row with their own user picker — a
 * second roster of the same humans the Users list already showed, kept in
 * step by hand. The actions belong to a PERSON, so they live where people
 * live, one row expansion away from the person they act on.
 */
export interface UserDataActionsProps {
  userId: string
  email: string
  /** The workspace the action is scoped to. The admin API is
   *  workspace-scoped by middleware: without it the request is refused with
   *  400 before the handler ever runs. */
  workspaceId: string
  /** Called after an erasure lands, so the roster drops the row. */
  onErased?: () => void
}

interface DeleteResponse {
  action_id?: string
  rows_deleted?: number
}

export function UserDataActions({ userId, email, workspaceId, onErased }: UserDataActionsProps) {
  const [busy, setBusy] = useState<"export" | "delete" | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [reason, setReason] = useState("")
  const [confirmed, setConfirmed] = useState(false)

  const dataURL = `/api/v1/admin/users/${encodeURIComponent(userId)}/data?workspace_id=${encodeURIComponent(workspaceId)}`

  const handleExport = useCallback(async () => {
    setBusy("export")
    try {
      const r = await apiFetch(dataURL, { headers: { Accept: "application/json" } })
      if (!r.ok) {
        const txt = await r.text().catch(() => "")
        toast.error(`Export failed (${r.status}): ${txt.slice(0, 200) || r.statusText}`)
        return
      }
      const data = await r.json()
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" })
      const url = URL.createObjectURL(blob)
      const a = document.createElement("a")
      a.href = url
      a.download = `data-export-${userId}-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.json`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
      toast.success(`Export downloaded for ${email}`)
    } catch (e) {
      toast.error(`Export failed: ${(e as Error).message}`)
    } finally {
      setBusy(null)
    }
  }, [dataURL, email, userId])

  const handleDelete = useCallback(async () => {
    if (!reason.trim()) {
      toast.error("A reason is required for the audit trail")
      return
    }
    setBusy("delete")
    try {
      const r = await apiFetch(dataURL, {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ reason: reason.trim() }),
      })
      if (!r.ok) {
        const txt = await r.text().catch(() => "")
        toast.error(`Erase failed (${r.status}): ${txt.slice(0, 200) || r.statusText}`)
        return
      }
      const body = (await r.json().catch(() => ({}))) as DeleteResponse
      const actionId = body.action_id ?? "unknown"
      toast.success(
        `Erased ${email}${body.rows_deleted != null ? ` (${body.rows_deleted} rows)` : ""} · audit ${actionId}`,
      )
      setConfirmOpen(false)
      setReason("")
      setConfirmed(false)
      onErased?.()
    } catch (e) {
      toast.error(`Erase failed: ${(e as Error).message}`)
    } finally {
      setBusy(null)
    }
  }, [dataURL, email, reason, onErased])

  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <p className="max-w-md text-[11px] leading-snug text-muted-foreground">
        If this person asks for a copy of what you hold about them, export it.
        If they ask to be forgotten, erase it. Both are written to an
        append-only trail — who did it, why, what it touched.
      </p>

      <div className="flex shrink-0 items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={handleExport}
          disabled={busy !== null}
        >
          <Download className="mr-1.5 h-3 w-3" />
          {busy === "export" ? "Exporting…" : "Export data"}
        </Button>

        <AlertDialog
          open={confirmOpen}
          onOpenChange={(open) => {
            // Only a deliberate close clears the form. A failed request that
            // auto-closed the dialog would otherwise discard what the
            // operator typed and make them enter the reason again.
            setConfirmOpen(open)
            if (!open && busy !== "delete") {
              setReason("")
              setConfirmed(false)
            }
          }}
        >
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs text-destructive hover:bg-destructive/10 hover:text-destructive"
            onClick={() => setConfirmOpen(true)}
            disabled={busy !== null}
          >
            <Trash2 className="mr-1.5 h-3 w-3" />
            Erase data
          </Button>

          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle className="flex items-center gap-2">
                <ShieldAlert className="h-4 w-4 text-destructive" />
                Erase everything held about {email}
              </AlertDialogTitle>
              <AlertDialogDescription>
                This removes every record referencing this person in this
                workspace. It cannot be undone, and it will be recorded against
                your account with the reason you give below.
              </AlertDialogDescription>
            </AlertDialogHeader>

            <div className="space-y-3 py-2">
              <div className="space-y-1.5">
                <Label htmlFor="erase-reason" className="text-xs">
                  Reason (recorded in the audit trail)
                </Label>
                <Input
                  id="erase-reason"
                  placeholder="e.g. erasure request #1234 from the data subject"
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  className="h-8 text-xs"
                  autoFocus
                />
              </div>
              <div className="flex items-start gap-2">
                <Checkbox
                  id="erase-confirm"
                  checked={confirmed}
                  onCheckedChange={(v) => setConfirmed(v === true)}
                />
                <Label htmlFor="erase-confirm" className="cursor-pointer text-xs font-normal leading-snug">
                  I understand this is irreversible and will be attributed to my
                  account.
                </Label>
              </div>
            </div>

            <AlertDialogFooter>
              <AlertDialogCancel disabled={busy === "delete"}>Cancel</AlertDialogCancel>
              {/*
                Plain <Button>, not AlertDialogAction: Radix's action closes
                the dialog on click, so the async DELETE would resolve after
                the dialog is gone and a failure would silently discard the
                reason. handleDelete decides when it closes — only once the
                server confirms the erasure landed.
              */}
              <Button
                type="button"
                variant="destructive"
                onClick={() => { void handleDelete() }}
                disabled={busy === "delete" || !reason.trim() || !confirmed}
              >
                {busy === "delete" ? "Erasing…" : "Erase permanently"}
              </Button>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </div>
  )
}
