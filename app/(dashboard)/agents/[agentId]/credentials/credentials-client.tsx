"use client"

import { useParams } from "next/navigation"

import { use, useState, useEffect, useCallback } from "react"
import { ShieldCheck, AlertCircle, Inbox, Plus, Trash2, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { AssignCredentialDialog } from "@/components/features/credentials/assign-credential-dialog"

interface CredentialData {
  id: string
  name: string
  description: string | null
  scope: string
}

interface AgentCredential {
  id: string
  agent_id: string
  credential_id: string
  env_var_name: string
  priority: number
  credential: CredentialData
}

const SCOPE_STYLES: Record<string, string> = {
  WORKSPACE: "bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-400",
  TEAM: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400",
}

export function CredentialsPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [credentials, setCredentials] = useState<AgentCredential[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [assignOpen, setAssignOpen] = useState(false)
  const [removingId, setRemovingId] = useState<string | null>(null)

  const fetchCredentials = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/credentials?workspace_id=${workspaceId}`)
      if (!res.ok) {
        setError("Failed to load credentials")
        return
      }
      const data: AgentCredential[] = await res.json()
      setCredentials(data)
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    if (!workspaceId) return
    fetchCredentials()
  }, [workspaceId, fetchCredentials])

  const handleRemove = useCallback(async (assignmentId: string) => {
    if (!workspaceId) return
    setRemovingId(assignmentId)
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/credentials/${assignmentId}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (res.ok) {
        setCredentials((prev) => prev.filter((c) => c.id !== assignmentId))
      }
    } catch {
      // silently fail
    } finally {
      setRemovingId(null)
    }
  }, [agentId, workspaceId])

  if (wsLoading || loading) {
    return <CredentialsSkeleton />
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-sm">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <ShieldCheck className="h-4 w-4 text-muted-foreground" />
          <p className="text-sm text-muted-foreground">
            {credentials.length} credential{credentials.length !== 1 ? "s" : ""} assigned · AES-256-GCM encrypted
          </p>
        </div>
        <Button size="sm" className="gap-1.5" onClick={() => setAssignOpen(true)}>
          <Plus className="h-4 w-4" />
          Assign Credential
        </Button>
      </div>

      {credentials.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <Inbox className="h-10 w-10 text-muted-foreground/50 mb-3" />
          <p className="text-sm font-medium text-muted-foreground">No credentials assigned</p>
          <p className="text-xs text-muted-foreground mt-1">Assign credentials so the agent can access external services.</p>
        </div>
      ) : (
        <div className="border rounded-lg overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b bg-muted/50 text-xs text-muted-foreground uppercase tracking-wide">
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Name</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Env Variable</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Priority</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Scope</th>
                <th className="text-right px-4 sm:px-6 py-3 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {credentials.map((c) => (
                <tr key={c.id} className="hover:bg-muted/50">
                  <td className="px-4 sm:px-6 py-3">
                    <div>
                      <span className="font-medium">{c.credential.name}</span>
                      {c.credential.description && (
                        <p className="text-xs text-muted-foreground mt-0.5 truncate max-w-[200px]">{c.credential.description}</p>
                      )}
                    </div>
                  </td>
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs">{c.env_var_name}</td>
                  <td className="px-4 sm:px-6 py-3 text-center font-mono text-xs">{c.priority}</td>
                  <td className="px-4 sm:px-6 py-3 hidden sm:table-cell">
                    <Badge variant="secondary" className={`text-xs ${SCOPE_STYLES[c.credential.scope] ?? ""}`}>
                      {c.credential.scope}
                    </Badge>
                  </td>
                  <td className="px-4 sm:px-6 py-3 text-right">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-destructive hover:text-destructive"
                      onClick={() => handleRemove(c.id)}
                      disabled={removingId === c.id}
                    >
                      {removingId === c.id ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <Trash2 className="h-4 w-4" />
                      )}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Info */}
      <p className="text-xs text-muted-foreground">
        Credentials are injected at container start. Priority-based failover rotates keys automatically on rate limit errors.
      </p>

      {workspaceId && (
        <AssignCredentialDialog
          open={assignOpen}
          onOpenChange={setAssignOpen}
          agentId={agentId}
          workspaceId={workspaceId}
          onAssigned={fetchCredentials}
        />
      )}
    </div>
  )
}

function CredentialsSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <Skeleton className="h-5 w-64" />
      <div className="border rounded-lg">
        <div className="border-b bg-muted/50 px-4 sm:px-6 py-3">
          <Skeleton className="h-4 w-full max-w-md" />
        </div>
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="px-4 sm:px-6 py-3 border-b last:border-b-0">
            <Skeleton className="h-5 w-full" />
          </div>
        ))}
      </div>
    </div>
  )
}
