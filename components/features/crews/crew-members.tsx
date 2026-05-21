"use client"

import { useState } from "react"
import { UserPlus, Pencil, Check, Loader2, X } from "lucide-react"
import { Badge } from "@/components/ui/badge"
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
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
import type { CrewMember, CrewMemberRole } from "@/lib/types/crew"

interface CrewMembersProps {
  members: CrewMember[]
  crewId: string
  workspaceId: string
  /** Caller can edit membership (workspace ADMIN/OWNER). Patch M1
   *  restricts PATCH /crews/{id}/members/{memberId} to workspace
   *  ADMIN/OWNER server-side too — per-crew ADMIN cannot reshape
   *  peer permissions because they could ladder up. The UI mirrors
   *  that gate so the role-edit chip only renders for admins. */
  canEdit: boolean
  onMembersChange: (members: CrewMember[]) => void
}

/** Sentinel for the "inherit workspace role" option in the Select.
 *  Distinct from "" because Radix UI's Select treats "" as no value
 *  selected, which would unmount the controlled value. */
const INHERIT_VALUE = "__INHERIT__"

/** Mirrors helpers.go roleRank ordering — top-down highest privilege
 *  first so the dropdown reads as "most powerful at top, inherit at
 *  bottom". OWNER is omitted from the per-crew menu because that
 *  status is workspace-only; a per-crew override never produces a
 *  workspace OWNER. (Backend accepts it but UX is "if you mean
 *  workspace OWNER, change workspace role.") */
const ROLE_OPTIONS: CrewMemberRole[] = ["ADMIN", "MANAGER", "MEMBER", "VIEWER"]

/** Tailwind class for each role's chip — same palette family the
 *  settings/profile section uses for workspace roles so the visual
 *  language stays consistent between the two surfaces. */
const roleCls: Record<CrewMemberRole, string> = {
  OWNER: "bg-muted text-foreground border-border",
  ADMIN: "bg-amber-500/10 text-amber-600 border-amber-500/40 dark:text-amber-400",
  MANAGER: "bg-blue-500/10 text-blue-600 border-blue-500/40 dark:text-blue-400",
  MEMBER: "bg-muted text-muted-foreground border-border",
  VIEWER: "bg-muted text-muted-foreground border-border",
}

export function CrewMembers({ members, crewId, workspaceId, canEdit, onMembersChange }: CrewMembersProps) {
  const [addDialogOpen, setAddDialogOpen] = useState(false)
  const [removingId, setRemovingId] = useState<string | null>(null)
  // Inline-edit state for the role column. Holds the member id
  // currently in edit mode and the pending value the user has
  // picked but not yet saved. null means no row is in edit mode.
  const [editingId, setEditingId] = useState<string | null>(null)
  const [pendingRole, setPendingRole] = useState<CrewMemberRole | null>(null)
  const [savingRole, setSavingRole] = useState(false)

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

  // Save the pending role pick. Empty / inherit clears the override
  // (server interprets empty body.Role as "drop back to workspace
  // role"). Anything else sends the named role. On success we
  // optimistically mutate the local list rather than refetching —
  // the server returns the new value in the response.
  async function handleSaveRole(memberId: string) {
    if (savingRole) return
    setSavingRole(true)
    const isInherit = pendingRole === null || pendingRole === undefined
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/members/${memberId}?workspace_id=${workspaceId}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ role: isInherit ? "" : pendingRole }),
        }
      )
      if (res.ok) {
        const updated = members.map((m) =>
          m.id === memberId ? { ...m, role: isInherit ? null : pendingRole } : m
        )
        onMembersChange(updated)
        toast.success("Role updated")
        setEditingId(null)
        setPendingRole(null)
      } else {
        const body = await res.json().catch(() => ({}))
        toast.error(body.error ?? "Failed to update role")
      }
    } catch {
      toast.error("Network error updating role")
    } finally {
      setSavingRole(false)
    }
  }

  function startEdit(member: CrewMember) {
    setEditingId(member.id)
    setPendingRole(member.role ?? null)
  }

  function cancelEdit() {
    setEditingId(null)
    setPendingRole(null)
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-default font-semibold">Members</h2>
        {canEdit && (
          <Button variant="outline" size="sm" className="gap-2" onClick={() => setAddDialogOpen(true)}>
            <UserPlus className="h-3.5 w-3.5" />
            Add Member
          </Button>
        )}
      </div>

      {members.length === 0 ? (
        <p className="text-body text-muted-foreground">No crew members yet.</p>
      ) : (
        <Card>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Email</TableHead>
                  <TableHead className="w-48">Role in this crew</TableHead>
                  <TableHead>Joined</TableHead>
                  {canEdit && <TableHead className="w-20" />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {members.map((member) => {
                  const isEditing = editingId === member.id
                  return (
                    <TableRow key={member.id} className="group">
                      <TableCell className="text-body font-medium">
                        {member.user.full_name ?? "—"}
                      </TableCell>
                      <TableCell className="text-body text-muted-foreground">
                        {member.user.email}
                      </TableCell>
                      <TableCell>
                        {isEditing ? (
                          <div className="flex items-center gap-1.5">
                            <Select
                              value={pendingRole ?? INHERIT_VALUE}
                              onValueChange={(v) =>
                                setPendingRole(v === INHERIT_VALUE ? null : (v as CrewMemberRole))
                              }
                            >
                              <SelectTrigger className="h-7 text-xs w-32">
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value={INHERIT_VALUE} className="text-xs">
                                  <span className="text-muted-foreground italic">
                                    Inherit workspace
                                  </span>
                                </SelectItem>
                                {ROLE_OPTIONS.map((r) => (
                                  <SelectItem key={r} value={r} className="text-xs">
                                    {r}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-7 w-7 text-emerald-600 hover:text-emerald-700 hover:bg-emerald-500/10"
                              onClick={() => handleSaveRole(member.id)}
                              disabled={savingRole}
                              title="Save"
                            >
                              {savingRole ? <Loader2 className="h-3 w-3 animate-spin" /> : <Check className="h-3 w-3" />}
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-7 w-7 text-muted-foreground hover:text-foreground"
                              onClick={cancelEdit}
                              disabled={savingRole}
                              title="Cancel"
                            >
                              <X className="h-3 w-3" />
                            </Button>
                          </div>
                        ) : (
                          <div className="flex items-center gap-1.5">
                            {member.role ? (
                              <Badge
                                variant="outline"
                                className={`text-[10px] font-medium ${roleCls[member.role]}`}
                              >
                                {member.role}
                              </Badge>
                            ) : (
                              <span className="text-[11px] text-muted-foreground italic">
                                inherits workspace role
                              </span>
                            )}
                            {canEdit && (
                              <Button
                                variant="ghost"
                                size="icon"
                                className="h-6 w-6 text-muted-foreground hover:text-foreground opacity-60 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity"
                                onClick={() => startEdit(member)}
                                title="Edit per-crew role"
                                aria-label="Edit per-crew role"
                              >
                                <Pencil className="h-3 w-3" />
                              </Button>
                            )}
                          </div>
                        )}
                      </TableCell>
                      <TableCell className="text-label text-muted-foreground">
                        {new Date(member.created_at).toLocaleDateString()}
                      </TableCell>
                      {canEdit && (
                        <TableCell>
                          {/* Actions column: Remove only. The role edit
                              affordance lives next to the role badge above
                              (always-visible pencil at opacity-60, full
                              opacity on hover / focus) so users with and
                              without an existing role override see the same
                              control. Hiding Remove during inline edit
                              avoids two competing destructive actions on
                              the same row. */}
                          {!isEditing && (
                            <div className="flex items-center gap-1">
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
                            </div>
                          )}
                        </TableCell>
                      )}
                    </TableRow>
                  )
                })}
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
