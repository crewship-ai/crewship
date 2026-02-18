"use client"

import { useState } from "react"
import { UserPlus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { toast } from "sonner"
import { AddMemberDialog } from "./add-member-dialog"
import type { CrewMember } from "@/lib/types/crew"

interface CrewMembersProps {
  members: CrewMember[]
  crewId: string
  workspaceId: string
  canEdit: boolean
  onMembersChange: (members: CrewMember[]) => void
}

export function CrewMembers({ members, crewId, workspaceId, canEdit, onMembersChange }: CrewMembersProps) {
  const [addDialogOpen, setAddDialogOpen] = useState(false)
  const [removingId, setRemovingId] = useState<string | null>(null)

  async function handleRemoveMember(memberId: string, memberName: string) {
    setRemovingId(memberId)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/members/${memberId}?workspace_id=${workspaceId}`,
        { method: "DELETE" }
      )
      if (res.ok) {
        onMembersChange(members.filter((m) => m.id !== memberId))
        toast.success(`${memberName} removed from crew`)
      } else {
        toast.error("Failed to remove member")
      }
    } catch {
      toast.error("Failed to remove member")
    } finally {
      setRemovingId(null)
    }
  }

  function handleMemberAdded(member: CrewMember) {
    onMembersChange([...members, member])
    toast.success(`${member.user.full_name || member.user.email} added to crew`)
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-base font-semibold">Members</h2>
        {canEdit && (
          <Button variant="outline" size="sm" className="gap-2" onClick={() => setAddDialogOpen(true)}>
            <UserPlus className="h-3.5 w-3.5" />
            Add Member
          </Button>
        )}
      </div>

      {members.length === 0 ? (
        <p className="text-sm text-muted-foreground">No crew members yet.</p>
      ) : (
        <Card>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Email</TableHead>
                  <TableHead>Joined</TableHead>
                  {canEdit && <TableHead className="w-20" />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {members.map((member) => (
                  <TableRow key={member.id}>
                    <TableCell className="text-sm font-medium">
                      {member.user.full_name ?? "—"}
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {member.user.email}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {new Date(member.created_at).toLocaleDateString()}
                    </TableCell>
                    {canEdit && (
                      <TableCell>
                        <AlertDialog>
                          <AlertDialogTrigger asChild>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="text-destructive hover:text-destructive"
                            >
                              Remove
                            </Button>
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>Remove member</AlertDialogTitle>
                              <AlertDialogDescription>
                                Are you sure you want to remove {member.user.full_name || member.user.email} from this crew?
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>Cancel</AlertDialogCancel>
                              <AlertDialogAction
                                onClick={() => handleRemoveMember(member.id, member.user.full_name || member.user.email)}
                                variant="destructive"
                                disabled={removingId === member.id}
                              >
                                {removingId === member.id ? "Removing..." : "Remove"}
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      </TableCell>
                    )}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      <AddMemberDialog
        open={addDialogOpen}
        onOpenChange={setAddDialogOpen}
        crewId={crewId}
        workspaceId={workspaceId}
        existingMemberIds={members.map((m) => m.user_id)}
        onMemberAdded={handleMemberAdded}
      />
    </div>
  )
}
