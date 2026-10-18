"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { toast } from "@/lib/toast"
import { Copy, Check, Building2, User } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { useAuth } from "@/hooks/use-auth"
import { Spinner } from "@/components/ui/spinner"
import { StatusPill } from "@/components/ui/status-pill"
import { formatStatus } from "@/lib/format-status"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { SettingsCard, SettingsRow, formatSettingsDate, settingsControl } from "../shared"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import { SaveFooter } from "@/components/ui/save-footer"
import { ChangePasswordDialog } from "./change-password-dialog"

// Browser sessions and CLI tokens used to be the last card here. They are
// their own section now (sessions-section.tsx, Settings › Sessions & tokens),
// so this file is the profile alone: identity, password and workspace role.

// ── Helpers ─────────────────────────────────────────────────────────

// Owner and admin carry the workspace; everyone else is a plain member.
const ROLE_TONE: Record<string, "blue" | "muted"> = { OWNER: "blue", ADMIN: "blue", MANAGER: "blue" }

function CopyableText({ value, mono }: { value: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false)
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            aria-label={`Copy ${value}`}
            onClick={() => {
              navigator.clipboard.writeText(value).then(
                () => toast.success("Email copied"),
                () => toast.error("Couldn’t copy the email", { duration: 6000 }),
              )
              setCopied(true)
              setTimeout(() => setCopied(false), 1500)
            }}
            className={cn("text-control text-primary-hover hover:underline underline-offset-2 truncate text-right", mono && "font-mono")}
          >
            {value}
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" className="text-label">
          {copied ? <span className="flex items-center gap-1"><Check className="h-3 w-3" /> Copied</span> : <span className="flex items-center gap-1"><Copy className="h-3 w-3" /> Copy</span>}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

// ── Props ───────────────────────────────────────────────────────────

interface ProfileSectionProps {
  userName?: string | null
  userEmail?: string | null
  userAvatarUrl?: string | null
  role?: string | null
  workspaceName?: string | null
  joinedAt?: string | null
  /** A restricted session (#2861): only account security — password, sessions
   *  and revoking CLI tokens — is on its allowlist, so profile edits and the
   *  workspace card are not offered. Sessions and tokens render beside this
   *  section (SessionsSection), not inside it. */
  restricted?: boolean
}

// Client-side avatar guardrails — mirror the server (#889) so the user gets
// instant feedback instead of a round-trip rejection.
const AVATAR_MAX_BYTES = 2 * 1024 * 1024
const AVATAR_ACCEPT = "image/png,image/jpeg,image/webp"

// ── Component ───────────────────────────────────────────────────────

export function ProfileSection({
  userName, userEmail, userAvatarUrl, role, workspaceName, joinedAt, restricted = false,
}: ProfileSectionProps) {
  // The avatar and display name also live in the global session, which the
  // top bar renders from. Writing them without re-pulling it is what made an
  // upload look like it had done nothing outside this one card.
  const { refresh: refreshSession } = useAuth()

  // ── Editable profile (name) state (#867.1) ──
  const [displayName, setDisplayName] = useState<string | null | undefined>(userName)
  useEffect(() => { setDisplayName(userName) }, [userName])

  // ── Avatar upload state (#889) ──
  const [avatarUrl, setAvatarUrl] = useState<string | null | undefined>(userAvatarUrl)
  useEffect(() => { setAvatarUrl(userAvatarUrl) }, [userAvatarUrl])
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [avatarUploading, setAvatarUploading] = useState(false)
  const [avatarError, setAvatarError] = useState<string | null>(null)

  const onPickAvatar = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    // Reset the input so re-selecting the same file re-triggers onChange.
    e.target.value = ""
    if (!file) return
    setAvatarError(null)
    if (!["image/png", "image/jpeg", "image/webp"].includes(file.type)) {
      setAvatarError("Must be a PNG, JPEG, or WebP image")
      return
    }
    if (file.size > AVATAR_MAX_BYTES) {
      setAvatarError("Image must be 2MB or smaller")
      return
    }
    setAvatarUploading(true)
    try {
      const form = new FormData()
      form.append("file", file)
      // No Content-Type header — the browser sets the multipart boundary.
      const res = await apiFetch("/api/v1/users/me/avatar", { method: "POST", body: form })
      const body = await res.json().catch(() => null)
      if (!res.ok) {
        setAvatarError((body?.detail ?? body?.error) || "Upload failed")
        return
      }
      setAvatarUrl(body?.avatar_url ?? null)
      // The complaint this fixes: "I set a profile picture and nothing
      // seemed to happen" — the row updates, but that's easy to miss.
      // Failures stay inline-only (avatarError above): a toast would just
      // repeat what's already sitting right next to the button.
      toast.success("Profile picture updated")
      void refreshSession()
    } catch {
      setAvatarError("Upload failed")
    } finally {
      setAvatarUploading(false)
    }
  }, [refreshSession])

  const removeAvatar = useCallback(async () => {
    setAvatarError(null)
    setAvatarUploading(true)
    try {
      const res = await apiFetch("/api/v1/users/me/avatar", { method: "DELETE" })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        setAvatarError((body?.detail ?? body?.error) || "Could not remove avatar")
        return
      }
      setAvatarUrl(null)
      toast.success("Profile picture removed")
      void refreshSession()
    } catch {
      setAvatarError("Could not remove avatar")
    } finally {
      setAvatarUploading(false)
    }
  }, [refreshSession])
  // The name is a plain field: an edit joins the page's floating Save bar,
  // and a rejected write is a corner toast that keeps what was typed.
  const nameForm = useDirtyForm({ name: displayName ?? "" })
  const nameTrimmed = nameForm.draft.name.trim()
  const nameValid = nameTrimmed.length >= 1 && nameTrimmed.length <= 100

  // ── Change-password dialog (#867.1; its own form, change-password-dialog) ──
  const [pwOpen, setPwOpen] = useState(false)

  const initials = (displayName ?? "U").split(" ").map((n) => n[0]).join("").slice(0, 2).toUpperCase()

  const saveName = useCallback(() => nameForm.submit(async ({ name }) => {
    const trimmed = name.trim()
    const res = await apiFetch("/api/v1/users/me", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ full_name: trimmed }),
    })
    if (!res.ok) {
      const body = await res.json().catch(() => null)
      throw new Error(typeof body?.error === "string" ? body.error : "Failed to save name")
    }
    setDisplayName(trimmed)
    void refreshSession()
  }), [nameForm, refreshSession])

  return (
    <div className="space-y-5">
      {/* ── Account ── */}
      <SettingsCard icon={User} title="Account" description="Your identity on this instance">
        <SettingsRow
          label="Profile picture"
          // The rule — or why the last pick was refused — under the label: an
          // upload saves at once, so there is nothing for the save bar.
          description={restricted ? undefined : avatarError
            ? <span role="alert" className="text-destructive">{avatarError}</span>
            : "PNG, JPEG, or WebP · max 2MB · saves at once"}
        >
          {avatarUrl ? (
            <img
              src={avatarUrl}
              alt="Your avatar"
              className="h-10 w-10 rounded-full object-cover ring-2 ring-border"
            />
          ) : (
            <div className="h-10 w-10 rounded-full bg-primary ring-2 ring-border flex items-center justify-center text-primary-foreground text-xs font-semibold">
              {initials}
            </div>
          )}
          {!restricted && (<>
          <input
            ref={fileInputRef}
            type="file"
            accept={AVATAR_ACCEPT}
            className="hidden"
            onChange={onPickAvatar}
            aria-label="Upload profile picture"
          />
          <Button
            size="sm"
            variant="outline"
            onClick={() => fileInputRef.current?.click()}
            disabled={avatarUploading}
          >
            {avatarUploading ? <Spinner className="h-3 w-3" /> : (avatarUrl ? "Change" : "Upload")}
          </Button>
          {avatarUrl && (
            <Button
              size="sm"
              variant="ghost"
              className="text-muted-foreground hover:text-destructive"
              onClick={() => void removeAvatar()}
              disabled={avatarUploading}
            >
              Remove
            </Button>
          )}
          </>)}
        </SettingsRow>
        <SettingsRow label="Email" description={userEmail ? "Click to copy" : undefined}>
          {userEmail ? <CopyableText value={userEmail} mono /> : <span className="text-control text-muted-foreground">Not set</span>}
        </SettingsRow>
        {restricted ? (
          <SettingsRow label="Full name">
            <span className="text-control text-muted-foreground">{userName || "Not set"}</span>
          </SettingsRow>
        ) : (<>
        <SettingsRow
          label="Full name"
          // A format problem belongs under its field; a failed write goes to the corner.
          description={nameForm.isDirty && !nameValid
            ? <span className="text-destructive">Name must be 1-100 characters</span>
            : "1–100 characters"}
        >
          <Input
            value={nameForm.draft.name}
            onChange={(e) => nameForm.set("name", e.target.value)}
            maxLength={100}
            aria-label="Full name"
            aria-invalid={(nameForm.isDirty && !nameValid) || undefined}
            placeholder="Not set"
            className={settingsControl}
            disabled={nameForm.status === "saving"}
          />
        </SettingsRow>
        <SaveFooter
          dirty={nameForm.isDirty}
          count={nameForm.dirtyCount}
          status={nameForm.status}
          error={nameForm.error}
          canSave={nameValid}
          onCancel={nameForm.reset}
          onSave={saveName}
        />
        </>)}
        <SettingsRow label="Password" description="Changing it signs out your other sessions">
          <span className="font-mono text-control tracking-[0.2em] text-muted-foreground" aria-hidden>••••••••</span>
          <Button size="sm" variant="outline" onClick={() => setPwOpen(true)}>
            Change
          </Button>
        </SettingsRow>
      </SettingsCard>

      <ChangePasswordDialog open={pwOpen} onOpenChange={setPwOpen} />

      {!restricted && (<>
      {/* ── Workspace ── */}
      <SettingsCard icon={Building2} title="Workspace" description="Your current organization and role">
        <SettingsRow label="Role">
          {role ? (
            <StatusPill tone={ROLE_TONE[role] ?? "muted"} label={formatStatus(role).label} className="font-sans tracking-normal" />
          ) : (
            <span className="text-control text-muted-foreground">Not assigned</span>
          )}
        </SettingsRow>
        {workspaceName && (
          <SettingsRow label="Organization">
            <span className="text-control text-foreground">{workspaceName}</span>
          </SettingsRow>
        )}
        {joinedAt && (
          <SettingsRow label="Joined">
            <span className="text-control text-foreground tabular-nums">
              {/* v67: "12 Oct 2026". */}
              {formatSettingsDate(joinedAt)}
            </span>
          </SettingsRow>
        )}
      </SettingsCard>
      </>)}
    </div>
  )
}

