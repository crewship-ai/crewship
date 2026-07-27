"use client"

import { useState } from "react"
import { HelpCircle, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import {
  Popover, PopoverContent, PopoverTrigger,
} from "@/components/ui/popover"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { CapabilityGrid } from "@/components/admin/capability-grid"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier, isManagerTier } from "@/lib/permissions/tiers"
import { SettingsCard, SettingsRow } from "../shared"

// ── Types ────────────────────────────────────────────────────────────

interface Member {
  id: string
  role: string
  created_at: string
  user: {
    id: string
    email: string
    full_name: string | null
    avatar_url: string | null
  }
}

interface MembersSectionProps {
  members: Member[]
  workspaceId: string
  currentUserId?: string
  // There used to be a `canInvite` prop fed by CASL's
  // `abilities.can("create", "Member")`. It happened to equal the right
  // answer, but only because OWNER/ADMIN get it from the blanket "manage
  // all" rule — nothing tied it to `POST /members`'s actual roleManage
  // tier. The gate is derived from `callerRole` below instead; the prop is
  // gone rather than left declared-and-ignored for the next reader to trip
  // over. See lib/permissions/tiers.ts.
  onRefresh: () => void
  /** Caller's workspace role. Surfaces the per-member capability
   *  grid (PRD-SLASH-CAPABILITIES-2026 §6.7), the invite control and the
   *  remove control only for ADMIN+ (`isAdminTier`); role-change stays
   *  gated separately in `MemberRoleControl` at MANAGER+ (`isManagerTier`). */
  callerRole?: string
}

// ── Constants ────────────────────────────────────────────────────────

// Role badges all use the same muted treatment — differentiation comes
// from the label itself, not the color, matching orchestration's style.
const roleCls: Record<string, string> = {
  OWNER: "bg-muted text-foreground border-border",
  ADMIN: "bg-muted text-foreground border-border",
  MANAGER: "bg-muted text-foreground border-border",
  MEMBER: "bg-muted text-muted-foreground border-border",
  VIEWER: "bg-muted text-muted-foreground border-border",
}

// Role rank mirrors the backend `roleRank` (internal/api/helpers.go). Used
// only to decide which members the caller may edit and which roles the
// dropdown may offer; the server re-enforces the full ladder.
const roleRank: Record<string, number> = {
  VIEWER: 1, MEMBER: 2, MANAGER: 3, ADMIN: 4, OWNER: 5,
}
const ROLE_ORDER = ["VIEWER", "MEMBER", "MANAGER", "ADMIN", "OWNER"]

const roleSummaries: { role: string; summary: string }[] = [
  { role: "Owner", summary: "All permissions" },
  { role: "Admin", summary: "All permissions except billing transfer" },
  { role: "Manager", summary: "Crew-level access, create agents, manage credentials" },
  { role: "Member", summary: "Own resource access only" },
  { role: "Viewer", summary: "Read only" },
]

// ── Helpers ──────────────────────────────────────────────────────────

function relativeTime(dateStr: string): string {
  const now = Date.now()
  const then = new Date(dateStr).getTime()
  const diffSec = Math.floor((now - then) / 1000)
  if (diffSec < 60) return "just now"
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  const diffDay = Math.floor(diffHr / 24)
  if (diffDay < 30) return `${diffDay}d ago`
  const diffMon = Math.floor(diffDay / 30)
  if (diffMon < 12) return `${diffMon}mo ago`
  const diffYr = Math.floor(diffMon / 12)
  return `${diffYr}y ago`
}


/**
 * The role legend, as help rather than furniture.
 *
 * It used to be a permanently-present accordion. The content is static —
 * identical in every workspace, forever — so it is reference material, and
 * reference material belongs behind a help affordance next to the thing it
 * explains, not competing for space with the live roster. The trigger sits
 * in the Members card header because roles apply to the whole list; a `?`
 * repeated on every row would be noise.
 */
function RoleLegend() {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className="h-7 px-2 gap-1.5 text-[11px] text-muted-foreground"
          aria-label="What do the roles mean?"
        >
          <HelpCircle className="size-3.5" />
          <span className="hidden sm:inline">Roles</span>
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[22rem] p-0">
        <div className="px-3 py-2 border-b border-border/60">
          <div className="text-xs font-medium">Roles &amp; permissions</div>
        </div>
        {roleSummaries.map((item, idx) => (
          <SettingsRow key={item.role} label={item.role} border={idx < roleSummaries.length - 1}>
            <span className="text-[11px] text-muted-foreground text-right">{item.summary}</span>
          </SettingsRow>
        ))}
      </PopoverContent>
    </Popover>
  )
}

// ── Member role control ──────────────────────────────────────────────

// Renders a role dropdown when the caller may change this member's role,
// otherwise a static badge. The caller may edit a member only when they
// strictly outrank them (and it isn't their own row); the offered roles
// are those strictly below the caller's own — matching the server-side
// ladder (grant-below-own, no-modify-superior). A confirm dialog gates
// the actual PATCH.
function MemberRoleControl({
  member,
  workspaceId,
  callerRole,
  isSelf,
  onRefresh,
}: {
  member: Member
  workspaceId: string
  callerRole?: string
  isSelf: boolean
  onRefresh: () => void
}) {
  const [pendingRole, setPendingRole] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const callerRank = callerRole ? roleRank[callerRole] ?? 0 : 0
  // Role-change is `roleCreate` server-side (MANAGER+) — a plain rank
  // comparison alone would let e.g. a MEMBER "edit" a VIEWER's role, which
  // the server would 403. Gate on the tier first, then the ladder.
  const canEdit = isManagerTier(callerRole) && !isSelf && callerRank > (roleRank[member.role] ?? 0)
  // Grantable roles: strictly below the caller's own rank.
  const options = ROLE_ORDER.filter((r) => (roleRank[r] ?? 0) < callerRank)

  const staticBadge = (
    <Badge
      variant="outline"
      className={cn("text-[10px] font-medium", roleCls[member.role] ?? "")}
    >
      {member.role}
    </Badge>
  )

  if (!canEdit || options.length === 0) return staticBadge

  async function applyRole(role: string) {
    setSaving(true)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/members/${member.id}?workspace_id=${workspaceId}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ role }),
        },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        // The role endpoint returns RFC 7807 problems (`detail`); fall back
        // to the legacy `error` shape for older responses.
        const raw = body?.detail ?? body?.error
        toast.error(typeof raw === "string" ? raw : "Failed to change role")
        return
      }
      toast.success(`Role changed to ${role}`)
      onRefresh()
    } catch {
      toast.error("Failed to change role")
    } finally {
      setSaving(false)
      setPendingRole(null)
    }
  }

  return (
    <>
      <Select
        value={member.role}
        onValueChange={(v) => { if (v !== member.role) setPendingRole(v) }}
        disabled={saving}
      >
        <SelectTrigger
          className="h-6 w-[104px] text-[10px] px-2"
          aria-label={`Change role for ${personLabel(member.user.full_name, member.user.email)}`}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((r) => (
            <SelectItem key={r} value={r} className="text-xs">{r}</SelectItem>
          ))}
        </SelectContent>
      </Select>

      <AlertDialog open={pendingRole !== null} onOpenChange={(o) => { if (!o) setPendingRole(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="text-sm">Change member role</AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              Change{" "}
              <span className="font-medium text-foreground">
                {personLabel(member.user.full_name, member.user.email)}
              </span>{" "}
              from <span className="font-medium">{member.role}</span> to{" "}
              <span className="font-medium">{pendingRole}</span>?
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs" disabled={saving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="h-7 text-xs"
              disabled={saving}
              onClick={(e) => {
                // Keep the dialog open while the PATCH is in flight; a
                // second click can't fire a duplicate request.
                e.preventDefault()
                if (pendingRole && !saving) void applyRole(pendingRole)
              }}
            >
              Change role
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

// ── Component ────────────────────────────────────────────────────────

export function MembersSection({
  members,
  workspaceId,
  currentUserId,
  onRefresh,
  callerRole,
}: MembersSectionProps) {
  const [removingId, setRemovingId] = useState<string | null>(null)
  // isAdmin gates invite, remove AND the per-member capability grid — all
  // three map to `roleManage` routes. isManager is strictly wider (also
  // true for MANAGER) and only used for the muted copy below; the
  // role-change control itself is gated inside MemberRoleControl.
  const isAdmin = isAdminTier(callerRole)
  const isManager = isManagerTier(callerRole)

  async function handleRemove(memberId: string) {
    setRemovingId(memberId)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/members/${memberId}?workspace_id=${workspaceId}`,
        { method: "DELETE" },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        const msg = typeof body?.error === "string" ? body.error : "Failed to remove member"
        toast.error(msg)
        return
      }
      toast.success("Member removed")
      onRefresh()
    } catch {
      toast.error("Failed to remove member")
    } finally {
      setRemovingId(null)
    }
  }

  return (
    <div className="space-y-5">
      {/* ── Members ── */}
      <SettingsCard
        title="Members"
        description={`${members.length} member${members.length === 1 ? "" : "s"} in this workspace`}
        actions={
          <>
            <RoleLegend />
            {isAdmin && <InviteMemberDialog workspaceId={workspaceId} onInvited={onRefresh} />}
          </>
        }
      >
        {members.map((member, idx) => {
          const isSelf = currentUserId === member.user.id
          const isOwner = member.role === "OWNER"
          const isLast = idx === members.length - 1
          return (
            <div
              key={member.id}
              className={cn(
                "flex items-center justify-between gap-4 px-4 py-2.5",
                !isLast && "border-b border-border/40",
              )}
            >
              {/* Left: avatar + name + email */}
              <div className="flex items-center gap-2.5 min-w-0">
                <UserAvatar
                  name={member.user.full_name}
                  email={member.user.email}
                  src={member.user.avatar_url}
                  className="h-7 w-7"
                  textClassName="text-[10px]"
                />
                <div className="min-w-0">
                  <div className="text-xs text-foreground truncate">
                    {personLabel(member.user.full_name, member.user.email)}
                  </div>
                  {(member.user.full_name ?? "").trim() && (
                    <div className="text-[10px] text-muted-foreground/80 font-mono truncate mt-0.5">
                      {member.user.email}
                    </div>
                  )}
                </div>
              </div>

              {/* Right: role control + joined + remove */}
              <div className="flex items-center gap-2.5 shrink-0">
                <MemberRoleControl
                  member={member}
                  workspaceId={workspaceId}
                  callerRole={callerRole}
                  isSelf={isSelf}
                  onRefresh={onRefresh}
                />
                <span className="text-[10px] text-muted-foreground font-mono tabular-nums w-[52px] text-right">
                  {relativeTime(member.created_at)}
                </span>
                <div className="w-6 flex justify-center">
                  {isAdmin && !isOwner && !isSelf ? (
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-6 w-6 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                          disabled={removingId === member.id}
                        >
                          <Trash2 className="h-3 w-3" />
                          <span className="sr-only">Remove member</span>
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle className="text-sm">Remove member</AlertDialogTitle>
                          <AlertDialogDescription className="text-xs">
                            Are you sure you want to remove{" "}
                            <span className="font-medium text-foreground">
                              {personLabel(member.user.full_name, member.user.email)}
                            </span>{" "}
                            from this workspace? This action cannot be undone.
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
                          <AlertDialogAction
                            className="h-7 text-xs bg-destructive text-destructive-foreground hover:bg-destructive/90"
                            onClick={() => handleRemove(member.id)}
                          >
                            Remove
                          </AlertDialogAction>
                        </AlertDialogFooter>
                      </AlertDialogContent>
                    </AlertDialog>
                  ) : null}
                </div>
              </div>
            </div>
          )
        })}
      </SettingsCard>

      {/* The roster above stays visible to everyone; only the mutating
          controls are tier-gated. Say so once, quietly — this is a normal
          state for MANAGER/MEMBER/VIEWER, not an error. */}
      {!isAdmin && (
        <p className="text-[11px] text-muted-foreground px-1">
          {isManager
            ? "Only admins can invite or remove members."
            : "Only managers and admins can make changes here."}
        </p>
      )}

      {/* ── Per-member capabilities ──
          Deliberately NOT behind a disclosure. Progressive disclosure is for
          reference material; this is live state whose checkboxes apply
          immediately. Hiding it meant "who can do what here?" — the question
          this screen exists to answer — cost an extra click. */}
      {isAdmin && currentUserId && (
        <div>
          <div className="flex items-baseline gap-2 mb-2.5">
            <span className="text-body font-medium text-foreground/80 leading-none">
              Per-member capabilities
            </span>
            <span className="text-[10px] text-muted-foreground leading-none">
              grant individual high-value actions without promoting role
            </span>
          </div>
          <div className="rounded-xl border border-border/60 bg-card p-3">
            <CapabilityGrid
                members={members}
                workspaceId={workspaceId}
                currentUserId={currentUserId}
              />
          </div>
        </div>
      )}
    </div>
  )
}
