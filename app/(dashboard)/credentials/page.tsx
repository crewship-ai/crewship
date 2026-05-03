"use client"

import * as React from "react"
import {
  Key, Plus, Pencil, Trash2,
  Bot, Lock, Terminal, CheckCircle, AlertTriangle, Clock, XCircle, ExternalLink,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageShell } from "@/components/layout/page-shell"
import { EmptyState } from "@/components/layout/empty-state"
import { Card } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusBadge } from "@/components/ui/status-badge"
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
import { formatDate } from "@/lib/time"
import { useAbilities } from "@/hooks/use-abilities"
import {
  CREDENTIAL_TYPE_ICON_COLOR,
  PROVIDER_ICON_COLOR,
} from "@/lib/colors"
import { cn } from "@/lib/utils"

interface Credential {
  id: string
  name: string
  description: string | null
  type: "AI_CLI_TOKEN" | "API_KEY" | "CLI_TOKEN" | "SECRET" | "OAUTH2"
  provider: "ANTHROPIC" | "OPENAI" | "GOOGLE" | "CURSOR" | "FACTORY" | "GITHUB" | "GITLAB" | "VERCEL" | "AWS" | "CUSTOM_CLI" | "NONE"
  status: "ACTIVE" | "EXPIRED" | "RATE_LIMITED" | "REVOKED" | "ERROR" | "PENDING"
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
  agent_names: string[]
  mcp_used: boolean
}

interface Org {
  id: string
  name: string
}

// Icon + label maps (colors live in lib/colors.ts — CREDENTIAL_TYPE_ICON_COLOR)
const TYPE_CONFIG: Record<
  Credential["type"],
  { icon: React.ElementType; label: string }
> = {
  AI_CLI_TOKEN: { icon: Bot, label: "AI CLI Token" },
  API_KEY: { icon: Key, label: "API Key" },
  CLI_TOKEN: { icon: Terminal, label: "CLI Token" },
  SECRET: { icon: Lock, label: "Secret" },
  OAUTH2: { icon: ExternalLink, label: "OAuth 2.0" },
}

const PROVIDER_LABELS: Record<string, string> = {
  ANTHROPIC: "Anthropic",
  OPENAI: "OpenAI",
  GOOGLE: "Google",
  CURSOR: "Cursor",
  FACTORY: "Factory",
  GITHUB: "GitHub",
  GITLAB: "GitLab",
  VERCEL: "Vercel",
  AWS: "AWS",
  CUSTOM_CLI: "Custom CLI",
  NONE: "--",
}

// Map raw credential status → canonical status key used by StatusBadge.
// Falls back to PENDING for anything we don't style explicitly.
const STATUS_KEY: Record<Credential["status"], string> = {
  ACTIVE: "COMPLETED",
  RATE_LIMITED: "BLOCKED",
  EXPIRED: "FAILED",
  REVOKED: "FAILED",
  ERROR: "FAILED",
  PENDING: "PENDING",
}

const STATUS_LABEL: Record<Credential["status"], string> = {
  ACTIVE: "Active",
  RATE_LIMITED: "Rate Limited",
  EXPIRED: "Expired",
  REVOKED: "Revoked",
  ERROR: "Error",
  PENDING: "Pending",
}

const STATUS_ICON: Record<Credential["status"], React.ElementType> = {
  ACTIVE: CheckCircle,
  RATE_LIMITED: Clock,
  EXPIRED: AlertTriangle,
  REVOKED: XCircle,
  ERROR: AlertTriangle,
  PENDING: Clock,
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

  const headerActions = canManage ? (
    <Button onClick={() => setAddOpen(true)}>
      <Plus className="mr-2 h-4 w-4" />
      Add Credential
    </Button>
  ) : null

  if (loading) {
    return (
      <PageShell
        title="Credentials"
        description="Shared secrets, API keys, and CLI tokens"
        actions={headerActions}
      >
        <div className="space-y-3">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      </PageShell>
    )
  }

  return (
    <PageShell
      title="Credentials"
      description="Shared secrets, API keys, and CLI tokens for your agents"
      actions={headerActions}
    >
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
        <Card className="overflow-hidden p-0">
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
                const typeColor = CREDENTIAL_TYPE_ICON_COLOR[cred.type] ?? "text-muted-foreground"
                const providerColor = PROVIDER_ICON_COLOR[cred.provider] ?? "text-muted-foreground"
                const StatusIcon = STATUS_ICON[cred.status]
                const showStatus = cred.type !== "SECRET"

                return (
                  <TableRow key={cred.id}>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <TypeIcon className={cn("h-4 w-4 shrink-0", typeColor)} />
                        <div className="min-w-0">
                          <p className="font-medium font-mono text-body">{cred.name}</p>
                          {cred.account_label && (
                            <p className="text-label text-muted-foreground">{cred.account_label}</p>
                          )}
                          {!cred.account_label && cred.description && (
                            <p className="text-label text-muted-foreground truncate max-w-48">{cred.description}</p>
                          )}
                        </div>
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline" className="text-label font-normal">
                        {typeConfig.label}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <span className={cn("text-body inline-flex items-center gap-1.5", providerColor)}>
                        {PROVIDER_LABELS[cred.provider]}
                      </span>
                    </TableCell>
                    <TableCell>
                      {showStatus ? (
                        <StatusBadge
                          status={STATUS_KEY[cred.status]}
                          label={
                            <span className="inline-flex items-center gap-1.5">
                              <StatusIcon className="h-3 w-3" />
                              {STATUS_LABEL[cred.status]}
                            </span>
                          }
                        />
                      ) : (
                        <span className="text-label text-muted-foreground">--</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        {cred.agent_names?.length > 0 ? (
                          <span className="text-body text-muted-foreground" title={cred.agent_names.join(", ")}>
                            {cred.agent_names.slice(0, 3).join(", ")}
                            {cred.agent_names.length > 3 && ` +${cred.agent_names.length - 3}`}
                          </span>
                        ) : (
                          <span className="text-body text-muted-foreground">
                            {cred._count_agent_credentials ?? 0} {(cred._count_agent_credentials ?? 0) === 1 ? "agent" : "agents"}
                          </span>
                        )}
                        {cred.mcp_used && (
                          <Badge variant="outline" className="text-label font-normal">
                            MCP
                          </Badge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell>
                      <span className="text-body text-muted-foreground">{formatDate(cred.created_at)}</span>
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
        </Card>
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
    </PageShell>
  )
}
