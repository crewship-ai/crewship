"use client"

import * as React from "react"
import { Key, Plus, Download, Upload, Pencil, Trash2 } from "lucide-react"
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
  scope: "ORGANIZATION" | "TEAM"
  team_id: string | null
  created_at: string
  updated_at: string
  _count: { agent_credentials: number }
}

interface Org {
  id: string
  name: string
}

export default function CredentialsPage() {
  const { abilities } = useAbilities()
  const [credentials, setCredentials] = React.useState<Credential[]>([])
  const [orgId, setOrgId] = React.useState<string | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [addOpen, setAddOpen] = React.useState(false)
  const [editOpen, setEditOpen] = React.useState(false)
  const [editCredential, setEditCredential] = React.useState<CredentialData | null>(null)
  const canManage = abilities.can("create", "Credential")

  const fetchOrg = React.useCallback(async () => {
    try {
      const res = await fetch("/api/v1/orgs")
      if (!res.ok) return null
      const orgs: Org[] = await res.json()
      if (orgs.length > 0) {
        setOrgId(orgs[0].id)
        return orgs[0].id
      }
    } catch {
      // silently fail
    }
    return null
  }, [])

  const fetchCredentials = React.useCallback(async (oid: string) => {
    try {
      const res = await fetch(`/api/v1/credentials?org_id=${oid}`)
      if (!res.ok) return
      const data: Credential[] = await res.json()
      setCredentials(data)
    } catch {
      // silently fail
    }
  }, [])

  const loadData = React.useCallback(async () => {
    setLoading(true)
    let oid = orgId
    if (!oid) {
      oid = await fetchOrg()
    }
    if (oid) {
      await fetchCredentials(oid)
    }
    setLoading(false)
  }, [orgId, fetchOrg, fetchCredentials])

  React.useEffect(() => {
    loadData()
  }, [loadData])

  function handleRefresh() {
    if (orgId) {
      fetchCredentials(orgId)
    }
  }

  function handleEdit(credential: Credential) {
    setEditCredential({
      id: credential.id,
      name: credential.name,
      description: credential.description,
      scope: credential.scope,
      team_id: credential.team_id,
    })
    setEditOpen(true)
  }

  async function handleDelete(credential: Credential) {
    const confirmed = window.confirm(
      `Are you sure you want to delete "${credential.name}"? This action cannot be undone.`
    )
    if (!confirmed || !orgId) return

    try {
      const res = await fetch(`/api/v1/credentials/${credential.id}?org_id=${orgId}`, {
        method: "DELETE",
      })
      if (res.ok) {
        handleRefresh()
      }
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
        <PageHeader title="Credentials" description="Manage API keys and secrets for your agents" />
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
      <PageHeader title="Credentials" description="Manage API keys and secrets for your agents">
        <Button variant="outline" size="sm">
          <Download className="mr-2 h-4 w-4" />
          Export JSON
        </Button>
        <Button variant="outline" size="sm">
          <Upload className="mr-2 h-4 w-4" />
          Import JSON
        </Button>
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
          description="Add API keys and secrets that your agents will use. All credentials are encrypted with AES-256-GCM."
        >
          <Button className="mt-4" onClick={() => setAddOpen(true)}>
            <Plus className="mr-2 h-4 w-4" />
            Add First Credential
          </Button>
        </EmptyState>
      ) : (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Scope</TableHead>
                <TableHead>Used by</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {credentials.map((cred) => (
                <TableRow key={cred.id}>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <Key className="h-4 w-4 text-muted-foreground shrink-0" />
                      <div>
                        <p className="font-medium">{cred.name}</p>
                        {cred.description && (
                          <p className="text-xs text-muted-foreground">{cred.description}</p>
                        )}
                      </div>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={cred.scope === "ORGANIZATION" ? "secondary" : "outline"}>
                      {cred.scope === "ORGANIZATION" ? "Organization" : "Team"}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <span className="text-muted-foreground">
                      {cred._count.agent_credentials} {cred._count.agent_credentials === 1 ? "agent" : "agents"}
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
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {orgId && (
        <AddCredentialDialog
          orgId={orgId}
          open={addOpen}
          onOpenChange={setAddOpen}
          onSuccess={handleRefresh}
        />
      )}

      {orgId && editCredential && (
        <EditCredentialDialog
          orgId={orgId}
          credential={editCredential}
          open={editOpen}
          onOpenChange={setEditOpen}
          onSuccess={handleRefresh}
        />
      )}
    </div>
  )
}
