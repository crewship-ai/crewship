"use client"

import { Badge } from "@/components/ui/badge"
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { cn } from "@/lib/utils"

const roleCls: Record<string, string> = {
  OWNER: "bg-amber-500/20 text-amber-400 border-amber-500/30",
  ADMIN: "bg-blue-500/20 text-blue-400 border-blue-500/30",
  MANAGER: "bg-teal-500/20 text-teal-400 border-teal-500/30",
  MEMBER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
  VIEWER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
}

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
  canInvite: boolean
  onRefresh: () => void
}

export function MembersSection({ members, workspaceId, canInvite, onRefresh }: MembersSectionProps) {
  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="bg-card border border-white/[0.06] rounded-lg">
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

        {members.length > 0 && (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow className="border-white/[0.06] hover:bg-transparent">
                  <TableHead className="text-[11px] text-muted-foreground/50 uppercase tracking-wider font-semibold">Name</TableHead>
                  <TableHead className="text-[11px] text-muted-foreground/50 uppercase tracking-wider font-semibold">Email</TableHead>
                  <TableHead className="text-[11px] text-muted-foreground/50 uppercase tracking-wider font-semibold">Role</TableHead>
                  <TableHead className="text-[11px] text-muted-foreground/50 uppercase tracking-wider font-semibold">Joined</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {members.map((member) => (
                  <TableRow key={member.id} className="border-white/[0.04] hover:bg-white/[0.02]">
                    <TableCell className="text-[13px] font-medium text-foreground">
                      {member.user.full_name ?? "\u2014"}
                    </TableCell>
                    <TableCell className="text-[12px] text-muted-foreground font-mono">
                      {member.user.email}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className={cn("text-[10px] font-medium", roleCls[member.role] ?? "")}
                      >
                        {member.role}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-[11px] text-muted-foreground/60 font-mono tabular-nums">
                      {new Date(member.created_at).toLocaleDateString()}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
    </div>
  )
}
