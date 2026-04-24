"use client"

import { useAgentId } from "@/hooks/use-agent-id"

import { useState, useEffect, useCallback } from "react"
import { ShieldCheck, AlertCircle, Inbox, Plus, Trash2, Loader2, RotateCcw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { SectionCard } from "@/components/ui/section-card"
import { StatusBadge } from "@/components/ui/status-badge"
import { EmptyState } from "@/components/layout/empty-state"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { PROVIDER_ICONS } from "@/components/icons/provider-icons"
import { AssignCredentialDialog } from "@/components/features/credentials/assign-credential-dialog"
import { PROVIDER_ICON_COLOR, CREDENTIAL_TYPE_ICON_COLOR } from "@/lib/colors"
import { cn } from "@/lib/utils"

interface AgentCredential {
  id: string
  agent_id: string
  credential_id: string
  credential_name: string
  credential_type: string
  credential_provider: string
  credential_status: string
  env_var_name: string
  priority: number
  created_at: string
}

export function CredentialsPageClient() {
  const agentId = useAgentId()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [credentials, setCredentials] = useState<AgentCredential[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [assignOpen, setAssignOpen] = useState(false)
  const [removingId, setRemovingId] = useState<string | null>(null)

  const fetchCredentials = useCallback(async () => {
    if (!workspaceId || !agentId) return
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/credentials?workspace_id=${workspaceId}`)
      if (!res.ok) {
        setError("Failed to load credentials")
        return
      }
      const data = await res.json()
      setCredentials(Array.isArray(data) ? data : [])
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setLoading(false)
    }
  }, [agentId, workspaceId])

  useEffect(() => {
    if (!workspaceId || !agentId) return
    fetchCredentials()
  }, [workspaceId, agentId, fetchCredentials])

  // Real-time: refresh when agent status changes (auto-assign may add credentials)
  useRealtimeEvent("agent.status", useCallback(() => { fetchCredentials() }, [fetchCredentials]))

  const handleRemove = useCallback(async (assignmentId: string) => {
    if (!workspaceId || !agentId) return
    setRemovingId(assignmentId)
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/credentials/${assignmentId}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (res.ok) {
        setCredentials((prev) => prev.filter((c) => c.id !== assignmentId))
      } else {
        setError("Failed to remove credential. Please try again.")
      }
    } catch {
      setError("Failed to remove credential. Please try again.")
    } finally {
      setRemovingId(null)
    }
  }, [agentId, workspaceId])

  if (wsLoading || loading) {
    return <CredentialsSkeleton />
  }

  if (error) {
    return (
      <div className="p-6">
        <div className="flex items-center gap-3">
          <AlertCircle className="h-5 w-5 text-destructive shrink-0" />
          <p className="text-body text-destructive flex-1">{error}</p>
          <Button variant="outline" size="sm" onClick={() => { setError(null); fetchCredentials() }} className="gap-2 shrink-0">
            <RotateCcw className="h-3.5 w-3.5" />
            Try Again
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="p-6 space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between gap-3">
        <div>
          <h2 className="text-title font-semibold">Credentials</h2>
          <div className="flex items-center gap-2 mt-1">
            <ShieldCheck className="h-3.5 w-3.5 text-muted-foreground" />
            <p className="text-body text-muted-foreground">
              {credentials.length} credential{credentials.length !== 1 ? "s" : ""} assigned · AES-256-GCM encrypted
            </p>
          </div>
        </div>
        <Button size="sm" className="gap-1.5" onClick={() => setAssignOpen(true)}>
          <Plus className="h-4 w-4" />
          Assign Credential
        </Button>
      </div>

      {credentials.length === 0 ? (
        <EmptyState
          icon={Inbox}
          title="No credentials assigned"
          description="Assign credentials so the agent can access external services."
        >
          <Button size="sm" className="gap-1.5 mt-4" onClick={() => setAssignOpen(true)}>
            <Plus className="h-4 w-4" />
            Assign Credential
          </Button>
        </EmptyState>
      ) : (
        <SectionCard bare>
          <ul className="divide-y divide-border">
            {credentials.map((c) => {
              const Icon = PROVIDER_ICONS[c.credential_provider]
              const providerClass = PROVIDER_ICON_COLOR[c.credential_provider] ?? "text-muted-foreground"
              const typeClass = CREDENTIAL_TYPE_ICON_COLOR[c.credential_type] ?? "text-muted-foreground"
              return (
                <li
                  key={c.id}
                  className="flex items-center gap-4 px-4 sm:px-6 py-3 hover:bg-muted/40 transition-colors"
                >
                  <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-muted/60">
                    {Icon ? (
                      <Icon className={cn("h-4 w-4", providerClass)} />
                    ) : (
                      <ShieldCheck className={cn("h-4 w-4", providerClass)} />
                    )}
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="text-body font-medium truncate">{c.credential_name}</span>
                      <StatusBadge status={c.credential_status} className="text-micro" />
                    </div>
                    <div className="flex items-center gap-3 mt-0.5 text-label text-muted-foreground">
                      <code className="font-mono text-micro">{c.env_var_name}</code>
                      <span aria-hidden>·</span>
                      <span className={cn("text-micro font-medium", typeClass)}>{c.credential_type}</span>
                      <span aria-hidden>·</span>
                      <span className="text-micro">priority {c.priority}</span>
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={`Remove ${c.credential_name}`}
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
                </li>
              )
            })}
          </ul>
        </SectionCard>
      )}

      {/* Info */}
      <p className="text-label text-muted-foreground">
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
    <div className="p-6 space-y-6">
      <div className="space-y-2">
        <Skeleton className="h-7 w-40" />
        <Skeleton className="h-4 w-64" />
      </div>
      <SectionCard bare>
        <div className="divide-y divide-border">
          {Array.from({ length: 3 }).map((_, i) => (
            <div key={i} className="flex items-center gap-4 px-4 sm:px-6 py-3">
              <Skeleton className="h-9 w-9 rounded-lg" />
              <div className="flex-1 space-y-2">
                <Skeleton className="h-4 w-48" />
                <Skeleton className="h-3 w-64" />
              </div>
              <Skeleton className="h-8 w-8 rounded" />
            </div>
          ))}
        </div>
      </SectionCard>
    </div>
  )
}
