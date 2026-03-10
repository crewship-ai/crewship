"use client"

import * as React from "react"
import {
  Key, Plus, Pencil, Trash2,
  Bot, Lock, Terminal, CheckCircle, AlertTriangle, Clock, XCircle,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { AddCredentialDialog } from "@/components/features/credentials/add-credential-dialog"
import { EditCredentialDialog } from "@/components/features/credentials/edit-credential-dialog"
import type { CredentialData } from "@/components/features/credentials/edit-credential-dialog"
import { useAbilities } from "@/hooks/use-abilities"

interface Credential {
  id: string
  name: string
  description: string | null
  type: "AI_CLI_TOKEN" | "API_KEY" | "CLI_TOKEN" | "SECRET"
  provider: "ANTHROPIC" | "OPENAI" | "GOOGLE" | "GITHUB" | "GITLAB" | "VERCEL" | "AWS" | "CUSTOM_CLI" | "NONE"
  status: "ACTIVE" | "EXPIRED" | "RATE_LIMITED" | "REVOKED" | "ERROR"
  scope: "WORKSPACE" | "CREW"
  crew_id: string | null
  crew_ids: string[]
  account_label: string | null
  account_email: string | null
  token_expires_at: string | null
  last_checked_at: string | null
  last_error: string | null
  created_at: string
  updated_at: string
  _count_agent_credentials: number
}

interface Org {
  id: string
  name: string
}

const TYPE_CONFIG = {
  AI_CLI_TOKEN: { icon: Bot, label: "AI CLI Token", color: "text-violet-600" },
  API_KEY: { icon: Key, label: "API Key", color: "text-amber-600" },
  CLI_TOKEN: { icon: Terminal, label: "CLI Token", color: "text-blue-600" },
  SECRET: { icon: Lock, label: "Secret", color: "text-muted-foreground" },
} as const

const PROVIDER_LABELS: Record<string, string> = {
  ANTHROPIC: "Anthropic",
  OPENAI: "OpenAI",
  GOOGLE: "Google",
  GITHUB: "GitHub",
  GITLAB: "GitLab",
  VERCEL: "Vercel",
  AWS: "AWS",
  CUSTOM_CLI: "Custom CLI",
  NONE: "--",
}

const STATUS_CONFIG = {
  ACTIVE: { icon: CheckCircle, label: "Active", variant: "default" as const, color: "text-green-600" },
  RATE_LIMITED: { icon: Clock, label: "Rate Limited", variant: "secondary" as const, color: "text-yellow-600" },
  EXPIRED: { icon: AlertTriangle, label: "Expired", variant: "destructive" as const, color: "text-orange-600" },
  REVOKED: { icon: XCircle, label: "Revoked", variant: "destructive" as const, color: "text-red-600" },
  ERROR: { icon: AlertTriangle, label: "Error", variant: "destructive" as const, color: "text-red-600" },
}

export default function CredentialsPage() {
  const { abilities } = useAbilities()
  const [credentials, setCredentials] = React.useState<Credential[]>([])
  const [workspaceId, setWorkspaceId] = React.useState<string | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [addOpen, setAddOpen] = React.useState(false)
  const [editOpen, setEditOpen] = React.useState(false)
  const [editCredential, setEditCredential] = React.useState<CredentialData | null>(null)
  const canManage = abilities.can("create", "Credential")

  const fetchWorkspace = React.useCallback(async () => {
    try {
      const res = await fetch("/api/v1/workspaces")
      if (!res.ok) return null
      const orgs: Org[] = await res.json() ?? []
      if (orgs.length > 0) {
        setWorkspaceId(orgs[0].id)
        return orgs[0].id
      }
    } catch {
      // silently fail
    }
    return null
  }, [])

  const fetchCredentials = React.useCallback(async (oid: string) => {
    try {
      const res = await fetch(`/api/v1/credentials?workspace_id=${oid}`)
      if (!res.ok) return
      const data = await res.json()
      setCredentials(Array.isArray(data) ? data : [])
    } catch {
      // silently fail
    }
  }, [])

  const loadData = React.useCallback(async () => {
    setLoading(true)
    let oid = workspaceId
    if (!oid) {
      oid = await fetchWorkspace()
    }
    if (oid) {
      await fetchCredentials(oid)
    }
    setLoading(false)
  }, [workspaceId, fetchWorkspace, fetchCredentials])

  React.useEffect(() => {
    loadData()
  }, [loadData])

  function handleRefresh() {
    if (workspaceId) fetchCredentials(workspaceId)
  }

  function handleEdit(credential: Credential) {
    setEditCredential({
      id: credential.id,
      name: credential.name,
      description: credential.description,
      type: credential.type,
      provider: credential.provider,
      scope: credential.scope,
      crew_id: credential.crew_id,
      crew_ids: credential.crew_ids?.length > 0 ? credential.crew_ids : (credential.crew_id ? [credential.crew_id] : []),
    })
    setEditOpen(true)
  }

  async function handleDelete(credential: Credential) {
    const confirmed = window.confirm(
      `Are you sure you want to delete "${credential.name}"? This action cannot be undone.`
    )
    if (!confirmed || !workspaceId) return

    try {
      const res = await fetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (res.ok) handleRefresh()
    } catch {
      // silently fail
    }
  }

  function formatDate(dateStr: string): string {
    return new Intl.DateTimeFormat("en-US", {
      month: "short",
      day: "numeric",
      year: "numeric",
    }).format(new Date(dateStr))
  }

  if (loading) {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
        <PageHeader title="Credentials" description="Manage API keys, AI tokens, and secrets" />
        <div className="space-y-3">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Credentials" description="Manage API keys, AI tokens, and secrets for your agents">
        {canManage && (
          <Button onClick={() => setAddOpen(true)}>
            <Plus className="mr-2 h-4 w-4" />
            Add Credential
          </Button>
        )}
      </PageHeader>

      {credentials.length === 0 ? (
        <EmptyState
          icon={Key}
          title="No credentials yet"
          description="Add AI CLI tokens, API keys, or secrets that your agents will use. All values are encrypted with AES-256-GCM."
        >
          {canManage && (
            <Button className="mt-4" onClick={() => setAddOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Add First Credential
            </Button>
          )}
        </EmptyState>
      ) : (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Provider</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Used by</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {credentials.map((cred) => {
                const typeConfig = TYPE_CONFIG[cred.type]
                const TypeIcon = typeConfig.icon
                const statusConfig = STATUS_CONFIG[cred.status]
                const StatusIcon = statusConfig.icon
                const showStatus = cred.type !== "SECRET"

                return (
                  <TableRow key={cred.id}>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <TypeIcon className={`h-4 w-4 shrink-0 ${typeConfig.color}`} />
                        <div className="min-w-0">
                          <p className="font-medium font-mono text-sm">{cred.name}</p>
                          {cred.account_label && (
                            <p className="text-xs text-muted-foreground">{cred.account_label}</p>
                          )}
                          {!cred.account_label && cred.description && (
                            <p className="text-xs text-muted-foreground truncate max-w-48">{cred.description}</p>
                          )}
                        </div>
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline" className="text-xs font-normal">
                        {typeConfig.label}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <span className="text-sm text-muted-foreground">
                        {PROVIDER_LABELS[cred.provider]}
                      </span>
                    </TableCell>
                    <TableCell>
                      {showStatus ? (
                        <div className="flex items-center gap-1.5">
                          <StatusIcon className={`h-3.5 w-3.5 ${statusConfig.color}`} />
                          <span className="text-xs">{statusConfig.label}</span>
                        </div>
                      ) : (
                        <span className="text-xs text-muted-foreground">--</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <span className="text-muted-foreground">
                        {cred._count_agent_credentials ?? 0} {(cred._count_agent_credentials ?? 0) === 1 ? "agent" : "agents"}
                      </span>
                    </TableCell>
                    <TableCell>
                      <span className="text-muted-foreground">{formatDate(cred.created_at)}</span>
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex items-center justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="icon-xs"
                          onClick={() => handleEdit(cred)}
                          title="Edit credential"
                        >
                          <Pencil className="h-3.5 w-3.5" />
                          <span className="sr-only">Edit</span>
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-xs"
                          onClick={() => handleDelete(cred)}
                          title="Delete credential"
                        >
                          <Trash2 className="h-3.5 w-3.5 text-destructive" />
                          <span className="sr-only">Delete</span>
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}

      {workspaceId && (
        <AddCredentialDialog
          workspaceId={workspaceId}
          open={addOpen}
          onOpenChange={setAddOpen}
          onSuccess={handleRefresh}
        />
      )}

      {workspaceId && editCredential && (
        <EditCredentialDialog
          workspaceId={workspaceId}
          credential={editCredential}
          open={editOpen}
          onOpenChange={setEditOpen}
          onSuccess={handleRefresh}
        />
      )}
    </div>
  )
}
