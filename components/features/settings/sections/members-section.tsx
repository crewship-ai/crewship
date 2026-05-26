"use client"

import { useState } from "react"
import { motion } from "motion/react"
import { ChevronRight, Trash2 } from "lucide-react"
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
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { CapabilityGrid } from "@/components/admin/capability-grid"
import { cn } from "@/lib/utils"
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
  canInvite: boolean
  onRefresh: () => void
  /** Caller's workspace role. Surfaces the per-member capability
   *  grid (PRD-SLASH-CAPABILITIES-2026 §6.7) only for ADMIN+. */
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

function initials(name: string | null, email: string): string {
  if (name) {
    const parts = name.trim().split(/\s+/)
    if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
    return name.slice(0, 2).toUpperCase()
  }
  return email.slice(0, 2).toUpperCase()
}

// ── Component ────────────────────────────────────────────────────────

export function MembersSection({
  members,
  workspaceId,
  currentUserId,
  canInvite,
  onRefresh,
  callerRole,
}: MembersSectionProps) {
  const [removingId, setRemovingId] = useState<string | null>(null)
  const [rolesOpen, setRolesOpen] = useState(false)
  const [capsOpen, setCapsOpen] = useState(false)
  const isAdmin = callerRole === "ADMIN" || callerRole === "OWNER"

  async function handleRemove(memberId: string) {
    setRemovingId(memberId)
    try {
      const res = await fetch(
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
        actions={canInvite ? <InviteMemberDialog workspaceId={workspaceId} onInvited={onRefresh} /> : undefined}
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
                <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/80">
                  <span className="text-[10px] font-semibold text-primary-foreground leading-none">
                    {initials(member.user.full_name, member.user.email)}
                  </span>
                </div>
                <div className="min-w-0">
                  <div className="text-xs text-foreground truncate">
                    {member.user.full_name ?? member.user.email}
                  </div>
                  {member.user.full_name && (
                    <div className="text-[10px] text-muted-foreground/80 font-mono truncate mt-0.5">
                      {member.user.email}
                    </div>
                  )}
                </div>
              </div>

              {/* Right: role badge + joined + remove */}
              <div className="flex items-center gap-2.5 shrink-0">
                <Badge
                  variant="outline"
                  className={cn("text-[10px] font-medium", roleCls[member.role] ?? "")}
                >
                  {member.role}
                </Badge>
                <span className="text-[10px] text-muted-foreground font-mono tabular-nums w-[52px] text-right">
                  {relativeTime(member.created_at)}
                </span>
                <div className="w-6 flex justify-center">
                  {!isOwner && !isSelf ? (
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
                              {member.user.full_name ?? member.user.email}
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

      {/* ── Roles & Permissions (collapsible) ── */}
      <Collapsible open={rolesOpen} onOpenChange={setRolesOpen}>
        <CollapsibleTrigger asChild>
          <button
            type="button"
            className="flex items-center gap-2 mb-2.5 group"
          >
            <motion.div animate={{ rotate: rolesOpen ? 90 : 0 }} transition={{ duration: 0.15 }}>
              <ChevronRight className="h-3 w-3 text-muted-foreground" />
            </motion.div>
            <span className="text-body font-medium text-muted-foreground group-hover:text-foreground/80 transition-colors leading-none">
              Roles &amp; Permissions
            </span>
          </button>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
            {roleSummaries.map((item, idx) => (
              <SettingsRow
                key={item.role}
                label={item.role}
                border={idx < roleSummaries.length - 1}
              >
                <span className="text-[11px] text-muted-foreground">
                  {item.summary}
                </span>
              </SettingsRow>
            ))}
          </div>
        </CollapsibleContent>
      </Collapsible>

      {/* ── Per-member capabilities (admin-only, PRD-SLASH-CAPABILITIES-2026 §6.7) ── */}
      {isAdmin && currentUserId && (
        <Collapsible open={capsOpen} onOpenChange={setCapsOpen}>
          <CollapsibleTrigger asChild>
            <button
              type="button"
              className="flex items-center gap-2 mb-2.5 group"
            >
              <motion.div animate={{ rotate: capsOpen ? 90 : 0 }} transition={{ duration: 0.15 }}>
                <ChevronRight className="h-3 w-3 text-muted-foreground" />
              </motion.div>
              <span className="text-body font-medium text-muted-foreground group-hover:text-foreground/80 transition-colors leading-none">
                Per-member capabilities
              </span>
              <span className="text-[10px] text-muted-foreground/60 leading-none">
                grant individual high-value actions without promoting role
              </span>
            </button>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="rounded-xl border border-border/60 bg-card p-3">
              <CapabilityGrid
                members={members}
                workspaceId={workspaceId}
                currentUserId={currentUserId}
              />
            </div>
          </CollapsibleContent>
        </Collapsible>
      )}
    </div>
  )
}
