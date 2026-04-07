"use client"

import { useState } from "react"
import { AlertTriangle, Loader2 } from "lucide-react"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"

interface DangerSectionProps {
  workspaceId: string
  role: string | null
}

export function DangerSection({ workspaceId, role }: DangerSectionProps) {
  const [isDeleting, setIsDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (role !== "OWNER") {
    return (
      <div className="bg-card border border-white/[0.06] rounded-lg p-8 text-center">
        <p className="text-[13px] text-muted-foreground/50">
          Only workspace owners can access this section.
        </p>
      </div>
    )
  }

  async function handleDelete() {
    if (!workspaceId || isDeleting) return
    setIsDeleting(true)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (res.ok) {
        window.location.href = "/"
      } else {
        const body = await res.json().catch(() => null)
        setError(typeof body?.error === "string" ? body.error : "Failed to delete workspace")
      }
    } catch {
      setError("Failed to delete workspace")
    } finally {
      setIsDeleting(false)
    }
  }

  return (
    <div className="bg-card border border-red-500/20 rounded-lg p-6">
      <div className="flex items-center gap-2 mb-1">
        <AlertTriangle className="h-4 w-4 text-red-400" />
        <h4 className="text-[14px] font-medium text-red-400">Danger Zone</h4>
      </div>
      <p className="text-[12px] text-muted-foreground/50 mb-5">
        These actions are irreversible. Proceed with extreme caution.
      </p>

      {error && <p className="text-[12px] text-red-400 mb-3">{error}</p>}

      <AlertDialog>
        <AlertDialogTrigger asChild>
          <button className="inline-flex items-center gap-1.5 h-[28px] px-3 rounded-[4px] text-[11.5px] font-medium bg-red-500/10 border border-red-500/30 text-red-400 hover:bg-red-500/20 transition-colors">
            Delete Workspace
          </button>
        </AlertDialogTrigger>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Workspace</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete this workspace? All crews, agents, credentials, and
              data will be permanently removed. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={isDeleting}
            >
              {isDeleting && <Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" />}
              {isDeleting ? "Deleting..." : "Delete Workspace"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
