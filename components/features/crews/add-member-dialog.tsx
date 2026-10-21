"use client"

import { useState, useEffect } from "react"
import { Spinner } from "@/components/ui/spinner"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { toast } from "@/lib/toast"
import type { CrewMember } from "@/lib/types/crew"
import { apiFetch } from "@/lib/api-fetch"

interface WorkspaceUser {
  id: string
  email: string
  full_name: string | null
}

interface AddMemberDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  crewId: string
  workspaceId: string
  existingMemberIds: string[]
  onMemberAdded: (member: CrewMember) => void
}

export function AddMemberDialog({
  open,
  onOpenChange,
  crewId,
  workspaceId,
  existingMemberIds,
  onMemberAdded,
}: AddMemberDialogProps) {
  const [users, setUsers] = useState<WorkspaceUser[]>([])
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [selectedUserId, setSelectedUserId] = useState<string>("")

  useEffect(() => {
    if (!open) return

    setLoading(true)
    setSelectedUserId("")

    apiFetch(`/api/v1/workspaces/${workspaceId}/members`)
      .then((res) => {
        if (!res.ok) throw new Error("Failed to fetch members")
        return res.json()
      })
      .then((members: { user: WorkspaceUser }[]) => {
        const available = members
          .map((m) => m.user)
          .filter((u) => !existingMemberIds.includes(u.id))
        setUsers(available)
      })
      .catch(() => {
        toast.error("Failed to load workspace members")
        setUsers([])
      })
      .finally(() => setLoading(false))
  }, [open, workspaceId, existingMemberIds])

  async function handleSubmit() {
    if (!selectedUserId) return

    setSubmitting(true)
    try {
      const res = await apiFetch(
        `/api/v1/crews/${crewId}/members?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ user_id: selectedUserId }),
        }
      )

      if (!res.ok) {
        const data = await res.json().catch(() => null)
        toast.error(typeof data?.error === "string" ? data.error : "Failed to add member")
        return
      }

      const member = (await res.json()) as CrewMember
      onMemberAdded(member)
      onOpenChange(false)
    } catch {
      toast.error("Failed to add member")
    } finally {
      setSubmitting(false)
    }
  }

  const canSubmit = !!selectedUserId && !submitting && !loading

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="sm"
      onSubmit={() => { if (canSubmit) void handleSubmit() }}
    >
      <CreateSurfaceHeader
        concept="crews"
        title="Add Member to Crew"
        description="Select a workspace member to add to this crew."
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody>
        {loading ? (
          <div className="flex items-center justify-center py-6">
            <Spinner className="h-5 w-5 text-muted-foreground" />
          </div>
        ) : users.length === 0 ? (
          <p className="text-body text-muted-foreground py-4 text-center">
            All workspace members are already in this crew.
          </p>
        ) : (
          <Select value={selectedUserId} onValueChange={setSelectedUserId}>
            <SelectTrigger aria-label="Select workspace member">
              <SelectValue placeholder="Select a member..." />
            </SelectTrigger>
            <SelectContent>
              {users.map((user) => (
                <SelectItem key={user.id} value={user.id}>
                  {user.full_name ? `${user.full_name} (${user.email})` : user.email}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={submitting ? "Adding..." : "Add Member"}
        onPrimary={() => void handleSubmit()}
        primaryDisabled={!selectedUserId || loading}
        busy={submitting}
      />
    </CreateSurface>
  )
}
