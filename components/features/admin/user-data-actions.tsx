"use client"

import { useCallback, useState } from "react"
import { Download, Trash2 } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { apiFetch } from "@/lib/api-fetch"
import { controlHeight } from "@/components/features/settings/shared"

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

  // Throws on failure: ConfirmDialog then stays open with the reason and the
  // typed e-mail where they were; it closes itself once this resolves.
  const handleDelete = useCallback(async () => {
    if (!reason.trim()) {
      toast.error("A reason is required for the audit trail")
      throw new Error("reason required")
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
        throw new Error(`erase failed (${r.status})`)
      }
      const body = (await r.json().catch(() => ({}))) as DeleteResponse
      const actionId = body.action_id ?? "unknown"
      toast.success(
        `Erased ${email}${body.rows_deleted != null ? ` (${body.rows_deleted} rows)` : ""} · audit ${actionId}`,
      )
      onErased?.()
    } catch (e) {
      if (!(e instanceof Error && /^(erase failed|reason required)/.test(e.message))) {
        toast.error(`Erase failed: ${(e as Error).message}`)
      }
      throw e
    } finally {
      setBusy(null)
    }
  }, [dataURL, email, reason, onErased])

  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <p className="max-w-md text-label leading-snug text-muted-foreground">
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

        <ConfirmDialog
          open={confirmOpen}
          onOpenChange={(open) => {
            setConfirmOpen(open)
            // Only a deliberate close clears the reason; a failed DELETE
            // leaves the dialog open (onConfirm throws) with it intact.
            if (!open) setReason("")
          }}
          destructive
          title={`Erase everything held about ${email}`}
          description="This removes every record referencing this person in this workspace. It cannot be undone, and it will be recorded against your account with the reason you give below."
          consequences={[
            { tone: "lost", text: "Every record referencing this person in this workspace." },
            { tone: "kept", text: "The audit entry: who erased it, why, and what it touched." },
          ]}
          typeToConfirm={email}
          confirmLabel="Erase permanently"
          confirmDisabled={!reason.trim()}
          onConfirm={handleDelete}
        >
          <Label htmlFor="erase-reason" className="text-xs">
            Reason (recorded in the audit trail)
          </Label>
          <Input
            id="erase-reason"
            placeholder="e.g. erasure request #1234 from the data subject"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            className={controlHeight}
            autoFocus
          />
        </ConfirmDialog>
      </div>
    </div>
  )
}
