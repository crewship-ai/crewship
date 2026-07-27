"use client"

import { useState, type FormEvent } from "react"
import { Check, Copy, UserPlus } from "lucide-react"

import { inviteMemberSchema } from "@/lib/validations"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

/**
 * Add someone to the workspace.
 *
 * This used to POST an invitation and report "Invitation sent successfully."
 * Nothing was sent: CreateInvitation holds no mailer, and the token it
 * returned was never rendered anywhere. The invitee heard nothing, the admin
 * had nothing to pass on, and the dialog closed itself claiming success.
 *
 * It now provisions the account and hands back a setup link the admin
 * delivers themselves — Slack, SMS, in person. The invitee sets their own
 * password through the reset-password page, so no password is ever chosen
 * by, or visible to, anyone but them.
 */

interface InviteMemberDialogProps {
  workspaceId: string
  onInvited?: () => void
}

interface ProvisionResult {
  setup_url: string
  created_user: boolean
  email: string
}

export function InviteMemberDialog({ workspaceId, onInvited }: InviteMemberDialogProps) {
  const [open, setOpen] = useState(false)
  const [email, setEmail] = useState("")
  const [fullName, setFullName] = useState("")
  const [role, setRole] = useState("MEMBER")
  const [status, setStatus] = useState<"idle" | "saving" | "error">("idle")
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<ProvisionResult | null>(null)
  const [copied, setCopied] = useState(false)

  /** Close or restart, refreshing the roster on the way out. Every path
   *  that discards the link goes through here, so the parent is refreshed
   *  exactly once and never while the link is still readable. */
  function dismiss(next: "close" | "again") {
    if (result) onInvited?.()
    reset()
    if (next === "close") setOpen(false)
  }

  function reset() {
    setEmail("")
    setFullName("")
    setRole("MEMBER")
    setStatus("idle")
    setError(null)
    setResult(null)
    setCopied(false)
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setStatus("saving")
    setError(null)

    const parsed = inviteMemberSchema.safeParse({ email, role })
    if (!parsed.success) {
      setStatus("error")
      setError(parsed.error.issues[0]?.message ?? "Check the email and role")
      return
    }

    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/members/provision?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          // full_name is omitted rather than sent blank: the server stores
          // NULL for an absent name so the UI can fall back to the email,
          // and "" would defeat that fallback.
          body: JSON.stringify({
            email: email.trim(),
            role,
            ...(fullName.trim() ? { full_name: fullName.trim() } : {}),
          }),
        },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        setStatus("error")
        // Verbatim: the server distinguishes "already a member" from "no
        // public URL configured", and those need different action from the
        // reader. A generic "failed" throws that away.
        setError(typeof body?.error === "string" ? body.error : `Failed (HTTP ${res.status})`)
        return
      }
      setResult((await res.json()) as ProvisionResult)
      setStatus("idle")
      // Deliberately NOT refreshing the roster here. onInvited makes the
      // settings layout refetch, which flips it to loading=true and swaps
      // MembersSection for a skeleton — unmounting this dialog with the
      // link still in its state. The link flashed and vanished before it
      // could be copied, and it cannot be shown again. The roster can wait
      // until the admin is finished; see dismiss() below.
    } catch {
      setStatus("error")
      setError("Couldn't reach the server")
    }
  }

  async function copyLink() {
    if (!result) return
    await navigator.clipboard.writeText(result.setup_url)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // Covers Escape and the X, not just the buttons — those discard the
        // link too, so the roster still needs refreshing.
        if (!next) dismiss("close")
        else setOpen(true)
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm">
          <UserPlus className="mr-2 h-4 w-4" />
          Add member
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{result ? "Send them this link" : "Add member"}</DialogTitle>
          <DialogDescription>
            {result
              ? result.setup_url
                ? "This is the only time the link is shown — the token is stored hashed and cannot be displayed again."
                : "They already have an account, so nothing needs setting up."
              : "Creates the account if the email is new, then gives you a setup link to send them."}
          </DialogDescription>
        </DialogHeader>

        {result ? (
          /* Deliberately NOT auto-closing. The old dialog dismissed itself
             after 1.5s; doing that with a link on screen would destroy the
             only copy of something that cannot be shown again. */
          <div className="space-y-4">
            <p className="text-body text-muted-foreground">
              {result.created_user
                ? `Account created for ${result.email}. They set their own password through this link.`
                : `${result.email} already had an account and has been added to this workspace. They sign in with their existing password — no setup link is issued, because one would let anybody holding it change that password.`}
            </p>
            {result.setup_url ? (
            <div className="space-y-2">
              <Label htmlFor="setup-link">Setup link</Label>
              <div className="flex gap-2">
                <Input id="setup-link" readOnly value={result.setup_url} className="font-mono text-xs" />
                <Button type="button" variant="outline" onClick={copyLink} aria-label="Copy setup link">
                  {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
                </Button>
              </div>
            </div>
            ) : null}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={() => dismiss("again")}>
                Add another
              </Button>
              <Button type="button" onClick={() => dismiss("close")}>
                Done
              </Button>
            </div>
          </div>
        ) : (
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="invite-email">Email address</Label>
              <Input
                id="invite-email"
                type="email"
                placeholder="colleague@company.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="invite-name">
                Name <span className="text-muted-foreground font-normal">(optional)</span>
              </Label>
              <Input
                id="invite-name"
                placeholder="Ada Lovelace"
                value={fullName}
                onChange={(e) => setFullName(e.target.value)}
              />
              <p className="text-[11px] text-muted-foreground">
                They can change this later. Without it the roster shows their email.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="invite-role">Role</Label>
              <Select value={role} onValueChange={setRole}>
                <SelectTrigger id="invite-role">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ADMIN">Admin</SelectItem>
                  <SelectItem value="MANAGER">Manager</SelectItem>
                  <SelectItem value="MEMBER">Member</SelectItem>
                  <SelectItem value="VIEWER">Viewer</SelectItem>
                </SelectContent>
              </Select>
            </div>

            {status === "error" && error && (
              <p className="text-body text-destructive">{error}</p>
            )}

            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={status === "saving"}>
                {status === "saving" ? "Adding…" : "Add member"}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
