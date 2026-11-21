"use client"

import { useState } from "react"
import { Copy, Lock, Mail } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { StatusPill } from "@/components/ui/status-pill"
import { personLabel } from "@/components/ui/user-avatar"

import { ROLE_LABEL } from "./members-model"
import { RolePill } from "./member-bits"
import { joinedDate } from "./people-table"
import { daysUntil, expiryText, invitationStatus, reissueInvitation, revokeInvitation, type Invitation } from "./use-invitations"

/**
 * Invitations, one row each: who, the role they will get, who sent it, and
 * what the SERVER says about it — open, ran out, or revoked (a revoked one is
 * not an expired one, though the server also moves its date to 1970).
 *
 * With `manage` (OWNER/ADMIN, as the server requires) a row offers Reissue — a
 * new link, shown once, valid 7 days, the old one stops working — and Revoke
 * after a confirmation. An ADMIN invitation is the owner's to manage: for
 * anyone else the row says so instead. A revoked one can only be invited again.
 */
export function InvitationRows({ invitations, workspaceId, manage = false, callerRole, onChanged }: {
  invitations: Invitation[]
  workspaceId?: string
  manage?: boolean
  callerRole?: string | null
  /** Re-read the list after a reissue or revoke. */
  onChanged?: () => void
}) {
  const [links, setLinks] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<Invitation | null>(null)

  async function reissue(inv: Invitation) {
    if (!workspaceId) return
    setBusy(inv.id)
    try {
      const { link } = await reissueInvitation(workspaceId, inv.id)
      setLinks((l) => ({ ...l, [inv.id]: link }))
      toast.success(`New link for ${inv.email}`, { description: "Valid 7 days. The old link no longer works." })
      onChanged?.()
    } catch (e) {
      toast.error(`Couldn’t reissue the invitation for ${inv.email}`, { description: e instanceof Error ? e.message : undefined })
    } finally {
      setBusy(null)
    }
  }

  async function revoke() {
    if (!workspaceId || !revoking) return
    try {
      await revokeInvitation(workspaceId, revoking.id)
    } catch (e) {
      toast.error(`Couldn’t revoke the invitation for ${revoking.email}`, { description: e instanceof Error ? e.message : undefined })
      throw e
    }
    toast.success(`Invitation for ${revoking.email} revoked`)
    onChanged?.()
  }

  return (
    <>
      <ul data-slot="members-invitations">
        {invitations.map((inv) => {
          const status = invitationStatus(inv)
          const days = daysUntil(inv.expires_at)
          const inviter = inv.inviter ? personLabel(inv.inviter.full_name ?? null, inv.inviter.email) : null
          const ownersOnly = inv.role === "ADMIN" && callerRole !== "OWNER"
          const link = links[inv.id]
          return (
            <li key={inv.id} data-invitation={inv.id} data-status={status} className="border-b border-border/60 px-4 py-2.5 last:border-b-0">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                <span className="grid h-7 w-7 shrink-0 place-items-center rounded-full border border-dashed border-border text-muted-foreground"><Mail className="h-3.5 w-3.5" aria-hidden /></span>
                <span className="min-w-0 flex-1 basis-48">
                  <span className="block truncate text-control">{inv.email}</span>
                  <span className="block truncate text-micro text-muted-foreground-soft">{ROLE_LABEL[inv.role] ?? inv.role}{inviter ? ` · invited by ${inviter}` : ""} · {joinedDate(inv.created_at)}</span>
                </span>
                <RolePill role={inv.role} className="max-md:hidden" />
                <StatusPill
                  tone={status === "expired" ? "danger" : status === "pending" && days <= 2 ? "warn" : "muted"}
                  label={status === "revoked" ? "Revoked" : expiryText(inv.expires_at)} />
                {manage && (ownersOnly ? (
                  <span className="inline-flex items-center gap-1 text-label text-muted-foreground">
                    <Lock className="h-3 w-3" aria-hidden />Only the owner manages an Admin invitation
                  </span>
                ) : (
                  <span className="flex items-center gap-1">
                    <Button variant="outline" size="sm" className="coarse:h-[44px]" disabled={busy === inv.id} aria-label={`Reissue ${inv.email}`} onClick={() => void reissue(inv)}>
                      {status === "pending" ? "Reissue" : "Invite again"}
                    </Button>
                    {status !== "revoked" && (
                      <Button variant="ghost" size="sm" className="text-destructive hover:bg-destructive/10 hover:text-destructive coarse:h-[44px]" aria-label={`Revoke ${inv.email}`} onClick={() => setRevoking(inv)}>
                        Revoke
                      </Button>
                    )}
                  </span>
                ))}
              </div>
              {link && (
                <div className="mt-2 flex flex-wrap items-center gap-2 pl-10" data-slot="invitation-link">
                  <input readOnly value={link} aria-label={`New invitation link for ${inv.email}`} onFocus={(e) => e.currentTarget.select()}
                    className="h-8 min-w-0 flex-1 rounded-md border border-control-border bg-surface-subtle px-2.5 font-mono text-label text-foreground" />
                  <Button variant="outline" size="sm" className="gap-1" onClick={() => {
                    navigator.clipboard.writeText(link).then(() => toast.success("Link copied"), () => toast.error("Couldn’t copy the link", { duration: 6000 }))
                  }}>
                    <Copy className="h-3 w-3" aria-hidden />Copy
                  </Button>
                  <span className="w-full text-label text-muted-foreground">Copy it now — it won’t be shown again. Valid 7 days.</span>
                </div>
              )}
            </li>
          )
        })}
      </ul>
      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(o) => { if (!o) setRevoking(null) }}
        title={`Revoke the invitation for ${revoking?.email ?? ""}?`}
        description="The link stops working at once."
        consequences={[
          { tone: "lost", text: "They can no longer join with the link they have" },
          { tone: "kept", text: "You can invite the same address again later" },
        ]}
        confirmLabel="Revoke invitation"
        destructive
        onConfirm={revoke}
      />
    </>
  )
}
