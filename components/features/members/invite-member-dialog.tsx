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
  const [role, setRole] = useState("MEMBER")
  const [status, setStatus] = useState<"idle" | "saving" | "error">("idle")
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<ProvisionResult | null>(null)
  const [copied, setCopied] = useState(false)

  function reset() {
    setEmail("")
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
          body: JSON.stringify({ email: email.trim(), role }),
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
      onInvited?.()
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
        setOpen(next)
        if (!next) reset()
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
              ? "This is the only time the link is shown — the token is stored hashed and cannot be displayed again."
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
                : `${result.email} already had an account and has been added to this workspace. They can sign in as usual — the link below is only needed if they have forgotten their password.`}
            </p>
            <div className="space-y-2">
              <Label htmlFor="setup-link">Setup link</Label>
              <div className="flex gap-2">
                <Input id="setup-link" readOnly value={result.setup_url} className="font-mono text-xs" />
                <Button type="button" variant="outline" onClick={copyLink} aria-label="Copy setup link">
                  {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
                </Button>
              </div>
            </div>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={reset}>
                Add another
              </Button>
              <Button type="button" onClick={() => setOpen(false)}>
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
