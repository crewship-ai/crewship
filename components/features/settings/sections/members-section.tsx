"use client"

import { useState } from "react"
import { motion } from "motion/react"
import { Check, ChevronRight, Trash2, X } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table"
import {
  Tooltip, TooltipContent, TooltipProvider, TooltipTrigger,
} from "@/components/ui/tooltip"
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

const permMatrix = [
  { role: "Owner", perms: [true, true, true, "All", true, true] },
  { role: "Admin", perms: [true, true, true, "All", true, true] },
  { role: "Manager", perms: [false, true, "Crew", "Crew", false, false] },
  { role: "Member", perms: [false, false, false, "Own", false, false] },
  { role: "Viewer", perms: [false, false, false, false, false, false] },
] as const

const permHeaders = [
  "See all crews",
  "Create agents",
  "Manage creds",
  "Audit access",
  "Manage members",
  "Billing",
]

const roleColors: Record<string, string> = {
  Owner: "text-amber-400",
  Admin: "text-blue-400",
  Manager: "text-teal-400",
}

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
  const [matrixOpen, setMatrixOpen] = useState(false)

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
    <div className="space-y-4">
      {/* ------------------------------------------------------------------ */}
      {/* Members Card                                                        */}
      {/* ------------------------------------------------------------------ */}
      <div className="bg-card border border-white/[0.06] rounded-lg">
        {/* Top bar */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-white/[0.06]">
          <div className="flex items-center gap-3">
            <h4 className="text-[14px] font-medium text-foreground">Members</h4>
            <div className="flex items-center gap-1.5 font-mono text-[11px] text-muted-foreground">
              <div className="w-1.5 h-1.5 rounded-full bg-blue-500" />
              <span className="tabular-nums">{members.length}</span>
            </div>
          </div>
          {canInvite && (
            <InviteMemberDialog workspaceId={workspaceId} onInvited={onRefresh} />
          )}
        </div>

        {/* Members table */}
        {members.length > 0 && (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow className="border-white/[0.06] hover:bg-transparent">
                  <TableHead className="text-[10px] text-muted-foreground/40 uppercase tracking-wider font-semibold">
                    Name
                  </TableHead>
                  <TableHead className="text-[10px] text-muted-foreground/40 uppercase tracking-wider font-semibold">
                    Email
                  </TableHead>
                  <TableHead className="text-[10px] text-muted-foreground/40 uppercase tracking-wider font-semibold">
                    Role
                  </TableHead>
                  <TableHead className="text-[10px] text-muted-foreground/40 uppercase tracking-wider font-semibold">
                    Joined
                  </TableHead>
                  <TableHead className="text-[10px] text-muted-foreground/40 uppercase tracking-wider font-semibold w-[60px]">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {members.map((member) => {
                  const isSelf = currentUserId === member.user.id
                  const isOwner = member.role === "OWNER"

                  return (
                    <TableRow
                      key={member.id}
                      className="border-white/[0.04] hover:bg-white/[0.02]"
                    >
                      {/* Name + avatar */}
                      <TableCell>
                        <div className="flex items-center gap-2.5">
                          <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/80">
                            <span className="text-[9px] font-semibold text-primary-foreground leading-none">
                              {initials(member.user.full_name, member.user.email)}
                            </span>
                          </div>
                          <span className="text-[13px] font-medium text-foreground">
                            {member.user.full_name ?? "\u2014"}
                          </span>
                        </div>
                      </TableCell>

                      {/* Email */}
                      <TableCell className="text-[12px] text-muted-foreground font-mono">
                        {member.user.email}
                      </TableCell>

                      {/* Role badge (disabled select -- P2) */}
                      <TableCell>
                        <TooltipProvider>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <button type="button" disabled className="cursor-not-allowed">
                                <Badge
                                  variant="outline"
                                  className={cn(
                                    "text-[10px] font-medium pointer-events-none",
                                    roleCls[member.role] ?? "",
                                  )}
                                >
                                  {member.role}
                                </Badge>
                              </button>
                            </TooltipTrigger>
                            <TooltipContent side="top" sideOffset={4}>
                              Role editing coming soon (P2)
                            </TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </TableCell>

                      {/* Joined */}
                      <TableCell className="text-[11px] text-muted-foreground/60 font-mono tabular-nums">
                        {relativeTime(member.created_at)}
                      </TableCell>

                      {/* Actions */}
                      <TableCell>
                        {!isOwner && !isSelf && (
                          <AlertDialog>
                            <AlertDialogTrigger asChild>
                              <Button
                                variant="ghost"
                                size="icon"
                                className="h-7 w-7 text-muted-foreground/40 hover:text-red-400 hover:bg-red-500/10"
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
                        )}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </div>

      {/* ------------------------------------------------------------------ */}
      {/* Collapsible Roles & Permissions Matrix                              */}
      {/* ------------------------------------------------------------------ */}
      <Collapsible open={matrixOpen} onOpenChange={setMatrixOpen}>
        <div className="bg-card border border-white/[0.06] rounded-lg">
          <CollapsibleTrigger asChild>
            <button
              type="button"
              className="flex w-full items-center gap-2 px-6 py-3.5 text-left group"
            >
              <motion.div
                animate={{ rotate: matrixOpen ? 90 : 0 }}
                transition={{ duration: 0.15 }}
              >
                <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/40" />
              </motion.div>
              <span className="text-[13px] font-medium text-muted-foreground/60 group-hover:text-muted-foreground transition-colors">
                Roles &amp; Permissions
              </span>
            </button>
          </CollapsibleTrigger>

          <CollapsibleContent>
            <div className="border-t border-white/[0.06] overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow className="border-white/[0.06] hover:bg-transparent">
                    <TableHead className="w-24 text-[10px] text-muted-foreground/40 uppercase tracking-wider font-semibold">
                      Role
                    </TableHead>
                    {permHeaders.map((h) => (
                      <TableHead
                        key={h}
                        className="text-center text-[10px] text-muted-foreground/40 uppercase tracking-wider font-semibold"
                      >
                        {h}
                      </TableHead>
                    ))}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {permMatrix.map((row) => (
                    <TableRow
                      key={row.role}
                      className="border-white/[0.04] hover:bg-white/[0.02]"
                    >
                      <TableCell
                        className={cn(
                          "text-[12px] font-medium",
                          roleColors[row.role] ?? "text-muted-foreground",
                        )}
                      >
                        {row.role}
                      </TableCell>
                      {row.perms.map((v, i) => (
                        <TableCell key={i} className="text-center">
                          {v === true ? (
                            <Check className="h-3.5 w-3.5 text-emerald-400 mx-auto" />
                          ) : v === false ? (
                            <X className="h-3.5 w-3.5 text-muted-foreground/20 mx-auto" />
                          ) : (
                            <span className="text-[10px] text-muted-foreground/60 font-mono">
                              {v}
                            </span>
                          )}
                        </TableCell>
                      ))}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </CollapsibleContent>
        </div>
      </Collapsible>
    </div>
  )
}
