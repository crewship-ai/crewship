"use client"

import { useState } from "react"
import { ChevronRight, HelpCircle, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import {
  Collapsible, CollapsibleContent, CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  Popover, PopoverContent, PopoverTrigger,
} from "@/components/ui/popover"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import {
  BulkPresetAction,
  CapabilityPips,
  MemberCapabilityToggles,
  useMemberCapabilities,
} from "@/components/admin/member-capabilities"
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
  /** Caller's workspace role. Surfaces the per-member capabilities
   *  (PRD-SLASH-CAPABILITIES-2026 §6.7), the invite control and the
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

/** What a single role grants, keyed by the wire value. Same sentences as
 *  the legend — the legend answers "what are the roles?", this answers
 *  "what does THIS person's role mean?" next to the control that changes
 *  it, which is the half that used to be reachable only through a popover. */
const roleSummaryByRole: Record<string, string> = Object.fromEntries(
  roleSummaries.map((r) => [r.role.toUpperCase(), r.summary]),
)

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
 * It is the whole ladder — five roles, identical in every workspace,
 * forever — so it is reference material you consult when *choosing* a role,
 * and it earns a help affordance rather than permanent screen space. The
 * per-person half of it is not hidden here: each expanded row states what
 * that member's own role grants, inline.
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
      // apiFetch resolves on 4xx/5xx — only a network failure rejects, so a
      // bare `await` would toast every 403 as a successful role change.
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

// ── One row per person ───────────────────────────────────────────────

/**
 * A member row. Collapsed it answers "who is here" — avatar, name, role,
 * a fixed-column pip strip for the capability grants, when they joined.
 * Expanded it answers "what may they do" — the role control with the
 * sentence that role implies, and the eight per-person grants.
 *
 * Before #1517 those two answers lived in two lists keyed by the same
 * identity, and reconciling them was the reader's job.
 *
 * Only the identity block is the disclosure trigger: the role select and
 * the remove button are interactive in their own right, and nesting them
 * inside a <button> would be invalid markup and unreachable by keyboard.
 */
function MemberRow({
  member,
  workspaceId,
  currentUserId,
  callerRole,
  isAdmin,
  granted,
  capsLoading,
  onRefresh,
  onRemove,
  removing,
  isLast,
}: {
  member: Member
  workspaceId: string
  currentUserId?: string
  callerRole?: string
  isAdmin: boolean
  granted: string[]
  capsLoading: boolean
  onRefresh: () => void
  onRemove: (memberId: string) => void
  removing: boolean
  isLast: boolean
}) {
  const [open, setOpen] = useState(false)
  const isSelf = currentUserId === member.user.id
  const isOwner = member.role === "OWNER"
  const label = personLabel(member.user.full_name, member.user.email)
  // Capabilities are an ADMIN+ read (the bulk endpoint 403s otherwise), so
  // non-admins expand to the role explanation alone.
  const showCaps = isAdmin && Boolean(currentUserId)

  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className={cn(!isLast && "border-b border-border/40")}
    >
      <div
        className={cn(
          "flex items-center gap-2.5 pl-1.5 pr-3",
          open && "bg-muted/30",
        )}
      >
        <CollapsibleTrigger asChild>
          <button
            type="button"
            aria-label={`${open ? "Collapse" : "Expand"} permissions for ${label}`}
            className="flex min-w-0 flex-1 items-center gap-2 rounded-md py-2 pl-1 pr-2 text-left transition-colors hover:bg-muted/40"
          >
            <ChevronRight
              aria-hidden="true"
              className={cn(
                "size-3.5 shrink-0 text-muted-foreground transition-transform",
                open && "rotate-90",
              )}
            />
            <UserAvatar
              name={member.user.full_name}
              email={member.user.email}
              src={member.user.avatar_url}
              className="h-7 w-7"
              textClassName="text-[10px]"
            />
            <span className="min-w-0">
              <span className="block truncate text-xs text-foreground">{label}</span>
              {(member.user.full_name ?? "").trim() && (
                <span className="mt-0.5 block truncate font-mono text-[10px] text-muted-foreground/80">
                  {member.user.email}
                </span>
              )}
            </span>
          </button>
        </CollapsibleTrigger>

        <div className="flex shrink-0 items-center gap-2.5">
          <MemberRoleControl
            member={member}
            workspaceId={workspaceId}
            callerRole={callerRole}
            isSelf={isSelf}
            onRefresh={onRefresh}
          />
          {showCaps && (
            <CapabilityPips granted={granted} isOwner={isOwner} label={label} />
          )}
          <span className="w-[52px] text-right font-mono text-[10px] tabular-nums text-muted-foreground">
            {relativeTime(member.created_at)}
          </span>
          <div className="flex w-6 justify-center">
            {isAdmin && !isOwner && !isSelf ? (
              <AlertDialog>
                <AlertDialogTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-6 w-6 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                    disabled={removing}
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
                      <span className="font-medium text-foreground">{label}</span>{" "}
                      from this workspace? This action cannot be undone.
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
                    <AlertDialogAction
                      className="h-7 text-xs bg-destructive text-destructive-foreground hover:bg-destructive/90"
                      onClick={() => onRemove(member.id)}
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

      <CollapsibleContent>
        <div className="bg-muted/20 px-4 pb-3.5 pt-1 pl-9">
          <div className="mb-1.5 mt-2 flex items-baseline gap-2">
            <span className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
              Role
            </span>
            <span className="text-[10px] text-muted-foreground/80">
              what the tier grants before any per-person capability
            </span>
          </div>
          <p className="text-[11px] text-muted-foreground">
            <span className="font-mono text-foreground">{member.role}</span>
            {" — "}
            {roleSummaryByRole[member.role] ?? "Permissions are defined by this role."}
            {isOwner && " The owner's role and capabilities are immutable."}
          </p>

          {showCaps && (
            <>
              <div className="mb-1 mt-3.5 flex items-baseline gap-2">
                <span className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
                  Capabilities
                </span>
                <span className="text-[10px] text-muted-foreground/80">
                  granted individually on top of the role · applies immediately
                </span>
              </div>
              <MemberCapabilityToggles
                member={member}
                workspaceId={workspaceId}
                currentUserId={currentUserId as string}
                granted={granted}
                isLoading={capsLoading}
              />
            </>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
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
  // isAdmin gates invite, remove AND the per-member capabilities — all three
  // map to `roleManage` routes. isManager is strictly wider (also true for
  // MANAGER) and only used for the muted copy below; the role-change control
  // itself is gated inside MemberRoleControl.
  const isAdmin = isAdminTier(callerRole)
  const isManager = isManagerTier(callerRole)
  const capsEnabled = isAdmin && Boolean(currentUserId) && members.length > 0

  // One round-trip for the whole roster, whether or not any row is expanded:
  // the collapsed pips need it too, and 500 rows must not mean 500 requests.
  const { data: capsByUser, isLoading: capsLoading } = useMemberCapabilities(
    workspaceId,
    capsEnabled,
  )

  async function handleRemove(memberId: string) {
    setRemovingId(memberId)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/members/${memberId}?workspace_id=${workspaceId}`,
        { method: "DELETE" },
      )
      // apiFetch resolves on 4xx/5xx — only a network failure rejects.
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
      {/* ── Members ──
          One row per person. The roster used to be this card plus a second
          "Per-member capabilities" table listing the same people again;
          answering "what can this person do?" meant reading both and
          reconciling them (#1517). The grid folded into the row. */}
      <SettingsCard
        title="Members"
        description={
          isAdmin
            ? `${members.length} member${members.length === 1 ? "" : "s"} — the dots summarise each person's capabilities; expand a row to change them`
            : `${members.length} member${members.length === 1 ? "" : "s"} in this workspace`
        }
        actions={
          <>
            <RoleLegend />
            {isAdmin && currentUserId && (
              <BulkPresetAction
                members={members}
                workspaceId={workspaceId}
                currentUserId={currentUserId}
                capsByUser={capsByUser}
              />
            )}
            {isAdmin && <InviteMemberDialog workspaceId={workspaceId} onInvited={onRefresh} />}
          </>
        }
      >
        {members.map((member, idx) => (
          <MemberRow
            key={member.id}
            member={member}
            workspaceId={workspaceId}
            currentUserId={currentUserId}
            callerRole={callerRole}
            isAdmin={isAdmin}
            granted={capsByUser?.[member.user.id] ?? []}
            capsLoading={capsLoading}
            onRefresh={onRefresh}
            onRemove={handleRemove}
            removing={removingId === member.id}
            isLast={idx === members.length - 1}
          />
        ))}
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
    </div>
  )
}
