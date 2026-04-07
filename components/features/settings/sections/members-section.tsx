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
import { cn } from "@/lib/utils"

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

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
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const roleCls: Record<string, string> = {
  OWNER: "bg-amber-500/20 text-amber-400 border-amber-500/30",
  ADMIN: "bg-blue-500/20 text-blue-400 border-blue-500/30",
  MANAGER: "bg-teal-500/20 text-teal-400 border-teal-500/30",
  MEMBER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
  VIEWER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
}

const roleColors: Record<string, string> = {
  Owner: "text-amber-400",
  Admin: "text-blue-400",
  Manager: "text-teal-400",
}

const roleSummaries: { role: string; summary: string }[] = [
  { role: "Owner", summary: "All permissions" },
  { role: "Admin", summary: "All permissions except billing transfer" },
  { role: "Manager", summary: "Crew-level access, create agents, manage credentials" },
  { role: "Member", summary: "Own resource access only" },
  { role: "Viewer", summary: "Read only" },
]

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function relativeTime(dateStr: string): string {
  const now = Date.now()
  const then = new Date(dateStr).getTime()
  const diffMs = now - then
  const diffSec = Math.floor(diffMs / 1000)
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

// ---------------------------------------------------------------------------
// Row component
// ---------------------------------------------------------------------------

function Row({ label, description, children, border = true }: {
  label?: string; description?: string; children: React.ReactNode; border?: boolean
}) {
  return (
    <div className={cn(
      "flex items-center justify-between gap-4 px-5 py-3.5 min-h-[48px]",
      border && "border-b border-white/[0.04] last:border-b-0",
    )}>
      {label ? (
        <div className="shrink-0">
          <div className="text-[13px] text-foreground">{label}</div>
          {description && <div className="text-[11px] text-muted-foreground/30 mt-0.5">{description}</div>}
        </div>
      ) : (
        <div className="min-w-0 flex-1" />
      )}
      <div className="flex items-center gap-2 min-w-0 justify-end">{children}</div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function MembersSection({
  members,
  workspaceId,
  currentUserId,
  canInvite,
  onRefresh,
}: MembersSectionProps) {
  const [removingId, setRemovingId] = useState<string | null>(null)
  const [rolesOpen, setRolesOpen] = useState(false)

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
    <div className="space-y-6">
      {/* ------------------------------------------------------------------ */}
      {/* Members                                                             */}
      {/* ------------------------------------------------------------------ */}
      <div>
        {/* Section title above card */}
        <div className="flex items-center justify-between mb-3">
          <div className="flex items-center gap-3">
            <h4 className="text-[13px] font-medium text-foreground">Members</h4>
            <span className="font-mono text-[11px] text-muted-foreground/40 tabular-nums">
              {members.length}
            </span>
          </div>
          {canInvite && (
            <InviteMemberDialog workspaceId={workspaceId} onInvited={onRefresh} />
          )}
        </div>

        {/* Members card */}
        <div className="bg-card border border-white/[0.06] rounded-lg">
          {members.map((member, idx) => {
            const isSelf = currentUserId === member.user.id
            const isOwner = member.role === "OWNER"
            const isLast = idx === members.length - 1

            return (
              <div
                key={member.id}
                className={cn(
                  "flex items-center justify-between gap-4 px-5 py-3.5 min-h-[48px]",
                  !isLast && "border-b border-white/[0.04]",
                )}
              >
                {/* Left: avatar + name + email */}
                <div className="flex items-center gap-3 min-w-0">
                  <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/80">
                    <span className="text-[8px] font-semibold text-primary-foreground leading-none">
                      {initials(member.user.full_name, member.user.email)}
                    </span>
                  </div>
                  <div className="min-w-0">
                    <div className="text-[13px] text-foreground truncate">
                      {member.user.full_name ?? member.user.email}
                    </div>
                    {member.user.full_name && (
                      <div className="text-[11px] text-muted-foreground/30 font-mono truncate mt-0.5">
                        {member.user.email}
                      </div>
                    )}
                  </div>
                </div>

                {/* Right: role badge + joined + remove */}
                <div className="flex items-center gap-3 shrink-0">
                  <Badge
                    variant="outline"
                    className={cn(
                      "text-[10px] font-medium",
                      roleCls[member.role] ?? "",
                    )}
                  >
                    {member.role}
                  </Badge>
                  <span className="text-[11px] text-muted-foreground/40 font-mono tabular-nums w-[52px] text-right">
                    {relativeTime(member.created_at)}
                  </span>
                  <div className="w-7 flex justify-center">
                    {!isOwner && !isSelf ? (
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7 text-muted-foreground/20 hover:text-red-400 hover:bg-red-500/10"
                            disabled={removingId === member.id}
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                            <span className="sr-only">Remove member</span>
                          </Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent size="sm">
                          <AlertDialogHeader>
                            <AlertDialogTitle>Remove member</AlertDialogTitle>
                            <AlertDialogDescription>
                              Are you sure you want to remove{" "}
                              <span className="font-medium text-foreground">
                                {member.user.full_name ?? member.user.email}
                              </span>{" "}
                              from this workspace? This action cannot be undone.
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>Cancel</AlertDialogCancel>
                            <AlertDialogAction
                              variant="destructive"
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
        </div>
      </div>

      {/* ------------------------------------------------------------------ */}
      {/* Roles & Permissions (collapsible)                                   */}
      {/* ------------------------------------------------------------------ */}
      <div>
        <Collapsible open={rolesOpen} onOpenChange={setRolesOpen}>
          {/* Section title above card */}
          <CollapsibleTrigger asChild>
            <button
              type="button"
              className="flex items-center gap-2 mb-3 group"
            >
              <motion.div
                animate={{ rotate: rolesOpen ? 90 : 0 }}
                transition={{ duration: 0.15 }}
              >
                <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/40" />
              </motion.div>
              <h4 className="text-[13px] font-medium text-muted-foreground/60 group-hover:text-muted-foreground transition-colors">
                Roles &amp; Permissions
              </h4>
            </button>
          </CollapsibleTrigger>

          <CollapsibleContent>
            <div className="bg-card border border-white/[0.06] rounded-lg">
              {roleSummaries.map((item, idx) => (
                <Row
                  key={item.role}
                  label={item.role}
                  border={idx < roleSummaries.length - 1}
                >
                  <span
                    className={cn(
                      "text-[12px] font-mono",
                      roleColors[item.role] ?? "text-muted-foreground/40",
                    )}
                  >
                    {item.summary}
                  </span>
                </Row>
              ))}
            </div>
          </CollapsibleContent>
        </Collapsible>
      </div>
    </div>
  )
}
