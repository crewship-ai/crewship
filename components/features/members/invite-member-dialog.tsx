"use client"

import { useRef, useState, type FormEvent } from "react"
import { Check, Copy, TriangleAlert, UserPlus } from "lucide-react"

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
import { cn } from "@/lib/utils"
import { CAPABILITY_BUNDLES, Capability, type CapabilityBundle } from "@/lib/capabilities"
import { ACCESS_PROFILES, ASSIGNABLE_ROLES, ROLE_LABEL, ROLE_SUMMARY_BY_ROLE } from "@/components/features/settings/members/members-model"
import { AreaChips, MemberDialogIcon, ROLE_ICON } from "@/components/features/settings/members/member-bits"

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
 *
 * The admin also says, up front, what the person may do: the role's default,
 * or one of the three presets (`{"set"}` PATCH on their capabilities right
 * after the account exists). A preset that could not be applied is said
 * plainly next to the link — the person is added either way.
 */

/** "" keeps whatever the server gives a new membership. */
type Access = "" | CapabilityBundle

interface InviteMemberDialogProps {
  workspaceId: string
  onInvited?: () => void
}

interface ProvisionResult {
  setup_url: string
  created_user: boolean
  email: string
  user_id?: string
  /** Set when a preset was asked for: whether it was applied. */
  access_applied?: boolean
}


export function InviteMemberDialog({ workspaceId, onInvited }: InviteMemberDialogProps) {
  const [open, setOpen] = useState(false)
  const [email, setEmail] = useState("")
  const [fullName, setFullName] = useState("")
  const [role, setRole] = useState("MEMBER")
  const [access, setAccess] = useState<Access>("")
  const [status, setStatus] = useState<"idle" | "saving" | "error">("idle")
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<ProvisionResult | null>(null)
  const [copied, setCopied] = useState(false)
  // Survives reset(), so several additions in one sitting still cost the
  // parent exactly one refresh — on the way out.
  const provisionedAny = useRef(false)

  /** Close or restart, refreshing the roster on the way out. Every path
   *  that discards the link goes through here, so the parent is refreshed
   *  exactly once and never while the link is still readable. */
  function dismiss(next: "close" | "again") {
    // Refresh ONLY on the way out. onInvited flips the settings layout to
    // loading, which swaps MembersSection for a skeleton and unmounts this
    // dialog — fine once the admin is finished, fatal on "Add another",
    // where it would tear down the empty form they just asked for.
    reset()
    if (next === "again") return
    if (provisionedAny.current) {
      provisionedAny.current = false
      onInvited?.()
    }
    setOpen(false)
  }

  function reset() {
    setEmail("")
    setFullName("")
    setRole("MEMBER")
    setAccess("")
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
      const created = (await res.json()) as ProvisionResult
      if (access && created.user_id) {
        const ws = encodeURIComponent(workspaceId)
        const set = await apiFetch(`/api/v1/workspaces/${ws}/members/${encodeURIComponent(created.user_id)}/capabilities?workspace_id=${ws}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ set: [...CAPABILITY_BUNDLES[access]] }),
        }).then((r) => r.ok, () => false)
        created.access_applied = set
      } else if (access) {
        created.access_applied = false
      }
      setResult(created)
      setStatus("idle")
      provisionedAny.current = true
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
        {/* The card-action button, the same one Refresh / Export CSV / Check
            again use across Settings and Admin. This was a filled primary,
            which put it at a different visual weight from every other action
            sitting in the same slot — and a filled button is for a page's
            single main CTA, not for one of several equal actions on a card. */}
        <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs">
          <UserPlus className="mr-1.5 h-3 w-3" />
          Add member
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader className="flex-row items-start gap-3 space-y-0 text-left">
          <MemberDialogIcon icon={result ? Check : UserPlus} tint={result ? "var(--success)" : "var(--primary)"} />
          <div className="min-w-0 space-y-1">
          {/* Keyed off setup_url like everything else in this dialog. The
              heading is part of the claim: "Send them this link" above a
              dialog that deliberately issues no link says the opposite of
              the paragraph underneath it. */}
          <DialogTitle>
            {!result ? "Add member" : result.setup_url ? "Send them this link" : "Member added"}
          </DialogTitle>
          <DialogDescription>
            {result
              ? result.setup_url
                ? "This is the only time the link is shown — the token is stored hashed and cannot be displayed again."
                : "They already have an account, so nothing needs setting up."
              : "Creates the account if the email is new, then gives you a setup link to send them."}
          </DialogDescription>
          </div>
        </DialogHeader>

        {result ? (
          /* Deliberately NOT auto-closing. The old dialog dismissed itself
             after 1.5s; doing that with a link on screen would destroy the
             only copy of something that cannot be shown again. */
          <div className="space-y-4">
            <p className="text-body text-muted-foreground">
              {/* Keyed off setup_url, not created_user. They answer different
                  questions: an account that exists but was never set up gets
                  created_user=false AND a fresh link, so branching on the
                  former printed "no setup link is issued" directly above the
                  setup link. */}
              {result.created_user
                ? `Account created for ${result.email}. They set their own password through this link.`
                : result.setup_url
                  ? `${result.email} already had an account here but had never set it up, so this link is a fresh one. The previous link, if any, no longer works.`
                  : `${result.email} already had an account and has been added to this workspace. They sign in with the credential they already have — no setup link is issued, because one would let anybody holding it take the account over.`}
            </p>
            {result.access_applied === false && (
              <p role="status" className="flex items-start gap-2 rounded-lg border border-warn/40 bg-warn/[0.07] px-3 py-2 text-label">
                <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn" aria-hidden />
                They were added with the role&apos;s default access — the preset could not be applied. Set it from their panel in Members.
              </p>
            )}
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
            <fieldset className="space-y-2">
              <legend className="mb-2 text-sm font-medium">Role <span className="font-normal text-muted-foreground">· what they manage</span></legend>
              <div role="radiogroup" aria-label="Role" className="grid grid-cols-2 gap-1.5 sm:grid-cols-4">
                {ASSIGNABLE_ROLES.slice().reverse().map((r) => {
                  const Icon = ROLE_ICON[r]
                  const on = role === r
                  return (
                    <button key={r} type="button" role="radio" aria-checked={on} onClick={() => setRole(r)} title={ROLE_SUMMARY_BY_ROLE[r]}
                      className={cn("flex flex-col items-start gap-1 rounded-lg border px-2.5 py-2 text-left transition-colors coarse:min-h-[52px]",
                        on ? "border-primary bg-primary/[0.08] ring-1 ring-primary" : "border-border hover:bg-accent")}>
                      <Icon className={cn("h-3.5 w-3.5", on ? "text-primary-hover" : "text-muted-foreground")} aria-hidden />
                      <span className="text-xs font-medium">{ROLE_LABEL[r]}</span>
                    </button>
                  )
                })}
              </div>
              <p className="text-label text-muted-foreground">{ROLE_SUMMARY_BY_ROLE[role]}.</p>
            </fieldset>

            <fieldset className="space-y-2">
              <legend className="mb-2 text-sm font-medium">What they can do</legend>
              <div role="radiogroup" aria-label="What they can do" className="inline-flex flex-wrap gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5">
                {([{ key: "" as Access, label: "Role default" }, ...ACCESS_PROFILES.map((p) => ({ key: p.key as Access, label: p.label }))]).map((o) => (
                  <button key={o.key || "default"} type="button" role="radio" aria-checked={access === o.key} onClick={() => setAccess(o.key)}
                    className={cn("h-7 rounded-md px-2.5 text-xs transition-colors coarse:h-9",
                      access === o.key ? "bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground")}>
                    {o.label}
                  </button>
                ))}
              </div>
              {access ? (
                <div className="space-y-1.5" data-slot="invite-access-preview">
                  <AreaChips granted={CAPABILITY_BUNDLES[access]} />
                  <p className="text-label text-muted-foreground">
                    {ACCESS_PROFILES.find((p) => p.key === access)?.summary}
                    {(CAPABILITY_BUNDLES[access] as readonly string[]).includes(Capability.CredentialReveal) && <span className="text-destructive"> Includes reading secrets in plaintext.</span>}
                  </p>
                </div>
              ) : (
                <p className="text-label text-muted-foreground">What a new {ROLE_LABEL[role]} gets on this server. Change single abilities later in their panel.</p>
              )}
            </fieldset>

            {status === "error" && error && (
              <p className="text-body text-destructive">{error}</p>
            )}

            <div className="flex justify-end gap-2">
              {/* dismiss(), not setOpen(false): a controlled setOpen does
                  NOT fire onOpenChange, so closing this way would skip the
                  one refresh the parent is owed. */}
              <Button type="button" variant="outline" onClick={() => dismiss("close")}>
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
