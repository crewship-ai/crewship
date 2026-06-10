"use client"

// PR-F2 — GDPR admin export / delete UI.
//
// Surfaces the two admin SAR endpoints shipped by the parallel backend
// PR (migration v107, gdpr_actions audit table):
//
//   GET    /api/v1/admin/users/{userId}/data  — export a single user's
//                                               data as JSON (browser
//                                               download).
//   DELETE /api/v1/admin/users/{userId}/data  — cascade delete every
//                                               row referencing the
//                                               user; writes an
//                                               append-only audit row
//                                               in gdpr_actions.
//
// Delete is irreversible and gated behind an AlertDialog that requires
// an explicit reason (recorded in gdpr_actions.reason) and an "I
// understand" checkbox before the destructive button enables. This
// matches the audit contract in migrate_v107_gdpr_cascade: every
// destructive action MUST carry a human-supplied reason.
//
// If the backend endpoint is not yet deployed when this UI is exercised,
// fetch surfaces the response status verbatim in an error banner so it
// is obvious the surface is wired and waiting on the API.

import React, { useCallback, useMemo, useState } from "react"
import { Download, Trash2, AlertTriangle } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Checkbox } from "@/components/ui/checkbox"
import {
  AlertDialog, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import {
  SettingsCard, SettingsDangerCard, SettingsRow,
} from "@/components/features/settings/shared"
import type { AdminUser } from "@/app/(dashboard)/admin/types"

export interface GdprActionsPanelProps {
  users: AdminUser[]
}

// Server response shape for the DELETE endpoint. Documented in
// internal/api/admin_gdpr.go (parallel agent PR). We code-defensively:
// any non-2xx surfaces the raw status; a 2xx with no body still
// reports success.
interface DeleteResponse {
  action_id?: string
  rows_deleted?: number
  scope?: Record<string, number>
}

export const GdprActionsPanel = React.memo(function GdprActionsPanel({
  users,
}: GdprActionsPanelProps) {
  const [query, setQuery] = useState("")
  const [selectedUserId, setSelectedUserId] = useState<string | null>(null)
  const [reason, setReason] = useState("")
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState<"export" | "delete" | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [lastActionId, setLastActionId] = useState<string | null>(null)

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return users.slice(0, 25)
    return users
      .filter((u) =>
        u.email.toLowerCase().includes(q) ||
        u.id.toLowerCase().includes(q) ||
        (u.full_name?.toLowerCase().includes(q) ?? false))
      .slice(0, 25)
  }, [query, users])

  const selectedUser = useMemo(
    () => users.find((u) => u.id === selectedUserId) ?? null,
    [users, selectedUserId],
  )

  const handleExport = useCallback(async () => {
    if (!selectedUser) return
    setBusy("export")
    try {
      const r = await fetch(
        `/api/v1/admin/users/${encodeURIComponent(selectedUser.id)}/data`,
        { headers: { Accept: "application/json" } },
      )
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
      a.download = `gdpr-export-${selectedUser.id}-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.json`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
      toast.success(`Export downloaded for ${selectedUser.email}`)
    } catch (e) {
      toast.error(`Export failed: ${(e as Error).message}`)
    } finally {
      setBusy(null)
    }
  }, [selectedUser])

  const handleDelete = useCallback(async () => {
    if (!selectedUser) return
    if (!reason.trim()) {
      toast.error("A reason is required for the audit trail")
      return
    }
    setBusy("delete")
    try {
      const r = await fetch(
        `/api/v1/admin/users/${encodeURIComponent(selectedUser.id)}/data`,
        {
          method: "DELETE",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ reason: reason.trim() }),
        },
      )
      if (!r.ok) {
        const txt = await r.text().catch(() => "")
        toast.error(`Delete failed (${r.status}): ${txt.slice(0, 200) || r.statusText}`)
        return
      }
      const body = (await r.json().catch(() => ({}))) as DeleteResponse
      const actionId = body.action_id ?? "unknown"
      setLastActionId(actionId)
      toast.success(
        `Deleted ${selectedUser.email}${
          body.rows_deleted != null ? ` (${body.rows_deleted} rows)` : ""
        } · gdpr_actions ${actionId}`,
      )
      // Reset so a second delete can't be triggered without re-confirm.
      setConfirmOpen(false)
      setReason("")
      setConfirmed(false)
      setSelectedUserId(null)
    } catch (e) {
      toast.error(`Delete failed: ${(e as Error).message}`)
    } finally {
      setBusy(null)
    }
  }, [selectedUser, reason])

  return (
    <div className="space-y-5">
      <div>
        <h3 className="text-body font-medium text-foreground/80 leading-none">
          GDPR data subject actions
        </h3>
        <p className="text-[11px] text-muted-foreground mt-1 leading-snug max-w-2xl">
          Export or cascade-delete a single user&apos;s data. Every action
          writes an append-only audit row in <code className="bg-muted/60 border border-border/60 px-1 py-0.5 rounded text-[10px] font-mono">gdpr_actions</code>{" "}
          (initiator, reason, scope, status). Delete is irreversible.
        </p>
      </div>

      <SettingsCard
        title="Select user"
        description={
          users.length === 0
            ? "No users available"
            : `Search by email, full name, or user ID (${users.length} users)`
        }
      >
        <div className="p-4 space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="gdpr-user-search" className="text-xs">
              Search
            </Label>
            <Input
              id="gdpr-user-search"
              placeholder="user@example.com or usr_xxx"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="h-8 text-xs"
            />
          </div>
          {filtered.length === 0 ? (
            <div className="text-[11px] text-muted-foreground py-3 text-center">
              No matching users
            </div>
          ) : (
            <div className="border border-border/60 rounded-md overflow-hidden max-h-[260px] overflow-y-auto">
              {filtered.map((u, idx) => {
                const isActive = u.id === selectedUserId
                return (
                  <button
                    key={u.id}
                    type="button"
                    onClick={() => setSelectedUserId(u.id)}
                    aria-pressed={isActive}
                    aria-label={`Select ${u.email} for GDPR action`}
                    className={
                      "w-full flex items-center justify-between gap-3 px-3 py-2 text-left transition-colors " +
                      (isActive ? "bg-emerald-500/10 " : "hover:bg-white/[0.02] ") +
                      (idx < filtered.length - 1 ? "border-b border-border/40" : "")
                    }
                  >
                    <div className="min-w-0 flex-1">
                      <div className="text-xs font-medium truncate">
                        {u.full_name ?? "(no name)"}
                      </div>
                      <div className="text-[10px] text-muted-foreground truncate">
                        {u.email} · <span className="font-mono">{u.id}</span>
                      </div>
                    </div>
                    <div className="text-[10px] text-muted-foreground shrink-0">
                      {u.workspace?.name ?? "—"}
                    </div>
                  </button>
                )
              })}
            </div>
          )}
        </div>
      </SettingsCard>

      {selectedUser && (
        <>
          <SettingsCard
            title="Export user data"
            description="Read-only JSON snapshot of every row referencing this user. Logged as action='export' in gdpr_actions."
          >
            <SettingsRow
              label={
                <div>
                  <div>{selectedUser.email}</div>
                  <div className="text-[10px] text-muted-foreground font-mono">
                    {selectedUser.id}
                  </div>
                </div>
              }
              border={false}
            >
              <Button
                size="sm"
                variant="outline"
                onClick={handleExport}
                disabled={busy !== null}
                className="h-7 text-xs"
              >
                <Download className="h-3 w-3 mr-1.5" />
                {busy === "export" ? "Exporting…" : "Export JSON"}
              </Button>
            </SettingsRow>
          </SettingsCard>

          <SettingsDangerCard
            title="Delete user data (cascade)"
            description="Irreversibly removes every row referencing this user across the workspace. Requires a reason for the audit trail."
          >
            <div className="p-4 space-y-3">
              <SettingsRow
                label={
                  <div>
                    <div>{selectedUser.email}</div>
                    <div className="text-[10px] text-muted-foreground font-mono">
                      {selectedUser.id}
                    </div>
                  </div>
                }
                border={false}
                className="!px-0 !py-0"
              >
                <AlertDialog
                  open={confirmOpen}
                  onOpenChange={(open) => {
                    // Don't clear reason/confirmed while the delete is
                    // in flight or while we're STILL open — only on a
                    // deliberate close by Cancel button. Otherwise a
                    // failed POST would wipe the operator's input on
                    // dialog auto-close and force re-entry (auditor
                    // catch round 6 — AlertDialogAction closes
                    // immediately on click which conflicted with this
                    // handler clearing state).
                    setConfirmOpen(open)
                    if (!open && busy !== "delete") {
                      setReason("")
                      setConfirmed(false)
                    }
                  }}
                >
                  <Button
                    size="sm"
                    variant="destructive"
                    onClick={() => setConfirmOpen(true)}
                    disabled={busy !== null}
                    className="h-7 text-xs"
                  >
                    <Trash2 className="h-3 w-3 mr-1.5" />
                    Delete user data
                  </Button>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle className="flex items-center gap-2">
                        <AlertTriangle className="h-4 w-4 text-destructive" />
                        Cascade delete all data for {selectedUser.email}
                      </AlertDialogTitle>
                      <AlertDialogDescription>
                        This will remove every record referencing this user
                        across the workspace. The action is irreversible and
                        will be recorded in the GDPR audit log with your
                        reason below.
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <div className="space-y-3 py-2">
                      <div className="space-y-1.5">
                        <Label htmlFor="gdpr-reason" className="text-xs">
                          Reason (required for audit trail)
                        </Label>
                        <Input
                          id="gdpr-reason"
                          placeholder="e.g. SAR request #1234 from data-subject"
                          value={reason}
                          onChange={(e) => setReason(e.target.value)}
                          className="h-8 text-xs"
                          autoFocus
                        />
                      </div>
                      <div className="flex items-start gap-2">
                        <Checkbox
                          id="gdpr-confirm"
                          checked={confirmed}
                          onCheckedChange={(v) => setConfirmed(v === true)}
                        />
                        <Label
                          htmlFor="gdpr-confirm"
                          className="text-xs leading-snug font-normal cursor-pointer"
                        >
                          I understand this is irreversible and will be
                          attributed to my account in the GDPR audit log.
                        </Label>
                      </div>
                    </div>
                    <AlertDialogFooter>
                      <AlertDialogCancel disabled={busy === "delete"}>
                        Cancel
                      </AlertDialogCancel>
                      {/*
                        Plain <Button> on purpose, NOT AlertDialogAction.
                        Radix AlertDialogAction calls setOpen(false) on
                        click which closes the dialog immediately — the
                        async DELETE then resolves AFTER the dialog is
                        gone, and the onOpenChange handler above would
                        already have wiped reason + confirmed on a
                        failure path. handleDelete itself controls when
                        the dialog closes (only on success, after the
                        server confirms the cascade landed).
                      */}
                      <Button
                        type="button"
                        onClick={() => { void handleDelete() }}
                        disabled={busy === "delete" || !reason.trim() || !confirmed}
                        variant="destructive"
                      >
                        {busy === "delete" ? "Deleting…" : "Delete permanently"}
                      </Button>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </SettingsRow>
            </div>
          </SettingsDangerCard>
        </>
      )}

      {lastActionId && (
        <SettingsCard
          title="Last action"
          description="Audit reference for the most recent destructive operation in this session."
        >
          <SettingsRow label="gdpr_actions.id" border={false}>
            <span className="text-[11px] font-mono text-muted-foreground">
              {lastActionId}
            </span>
          </SettingsRow>
        </SettingsCard>
      )}
    </div>
  )
})
