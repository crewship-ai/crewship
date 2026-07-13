"use client"

import * as React from "react"
import Link from "next/link"
import { motion } from "motion/react"
import { toast } from "sonner"
import {
  Key, Plus, Pencil, Trash2, Clock, AlertTriangle,
  ArrowUpDown, RefreshCw,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { SubBar, SubBarPrimary } from "@/components/layout/sub-bar"
import { EmptyState } from "@/components/layout/empty-state"
import { Card } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { SidebarSearch } from "@/components/layout/sidebar-kit"
import { Skeleton } from "@/components/ui/skeleton"
import { TabBar } from "@/components/ui/tab-bar"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { AddSecretSheet } from "@/components/features/credentials/add-secret-sheet"
import { CredentialDetailSheet } from "@/components/features/credentials/credential-detail-sheet"
import { RotationDialog } from "@/components/features/credentials/rotation-dialog"
import { EditCredentialDialog, type CredentialData } from "@/components/features/credentials/edit-credential-dialog"
import { formatRelativeTime } from "@/lib/time"
import { useAbilities } from "@/hooks/use-abilities"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

interface Credential {
  id: string
  name: string
  description: string | null
  type: "AI_CLI_TOKEN" | "API_KEY" | "CLI_TOKEN" | "SECRET" | "OAUTH2"
       | "USERPASS" | "SSH_KEY" | "CERTIFICATE" | "GENERIC_SECRET"
  provider: "ANTHROPIC" | "OPENAI" | "GOOGLE" | "CURSOR" | "FACTORY"
          | "GITHUB" | "GITLAB" | "VERCEL" | "AWS" | "CUSTOM_CLI" | "NONE"
          | "VAULT_USERPASS" | "VAULT_SSH_KEY" | "VAULT_CERTIFICATE" | "VAULT_GENERIC"
  status: "ACTIVE" | "EXPIRED" | "RATE_LIMITED" | "REVOKED" | "ERROR" | "PENDING" | "PENDING_APPROVAL"
  scope: "WORKSPACE" | "CREW"
  crew_id: string | null
  crew_ids: string[]
  account_label: string | null
  account_email: string | null
  // username is cleartext for USERPASS credentials, null otherwise.
  // Backend sets the column to NULL for legacy types so a null-check
  // is the cheapest "is this USERPASS-ish" detector at render time.
  username: string | null
  token_expires_at: string | null
  last_checked_at: string | null
  last_error: string | null
  last_used_at: string | null
  last_used_ips: string[]
  tags: string[]
  created_at: string
  updated_at: string
  _count_agent_credentials: number
  agent_names: string[]
  mcp_used: boolean
}

// 5-state status taxonomy from CONNECTIONS.md §3.4 (Datadog parity), plus
// "Pending" for an agent-proposed credential awaiting human approval.
type DerivedStatus = "Available" | "Detected" | "Connected" | "Error" | "Stale" | "Pending"

const STALE_THRESHOLD_DAYS = 90

function deriveStatus(c: Credential): DerivedStatus {
  // Agent-proposed, not yet approved: not usable by any agent until a human
  // approves the linked escalation. Surfaced as a distinct "Pending" state.
  if (c.status === "PENDING_APPROVAL") return "Pending"
  if (c.status === "EXPIRED" || c.status === "REVOKED" || c.status === "ERROR" || c.status === "RATE_LIMITED") return "Error"
  if (c.token_expires_at) {
    const exp = new Date(c.token_expires_at).getTime()
    if (!Number.isNaN(exp) && exp < Date.now()) return "Error"
  }
  if (c.last_used_at) {
    const last = new Date(c.last_used_at).getTime()
    if (!Number.isNaN(last) && Date.now() - last > STALE_THRESHOLD_DAYS * 24 * 3600 * 1000) {
      return "Stale"
    }
  }
  return "Connected"
}

const STATUS_DOT_COLOR: Record<DerivedStatus, string> = {
  Available: "bg-muted-foreground/40",
  Detected: "bg-blue-400",
  Connected: "bg-emerald-500",
  Error: "bg-red-500",
  Stale: "bg-amber-500",
  Pending: "bg-amber-400",
}

interface Org { id: string; name: string }

const TYPE_LABEL: Record<Credential["type"], string> = {
  AI_CLI_TOKEN: "ai cli",
  API_KEY: "api key",
  CLI_TOKEN: "token",
  SECRET: "secret",
  OAUTH2: "oauth",
  USERPASS: "userpass",
  SSH_KEY: "ssh key",
  CERTIFICATE: "cert",
  GENERIC_SECRET: "secret",
}

type SortKey = "last_used" | "name" | "created"

export default function CredentialsPage() {
  const { abilities } = useAbilities()
  const [credentials, setCredentials] = React.useState<Credential[]>([])
  const [workspaceId, setWorkspaceId] = React.useState<string | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [loadError, setLoadError] = React.useState<string | null>(null)
  const [addOpen, setAddOpen] = React.useState(false)
  const [editOpen, setEditOpen] = React.useState(false)
  const [editCredential, setEditCredential] = React.useState<CredentialData | null>(null)
  const canManage = abilities.can("create", "Credential")
  // Row-action gating mirrors the backend: PATCH allows MANAGER
  // ("update"), DELETE is OWNER/ADMIN only ("manage" → CASL "delete").
  // Hiding what would 403 beats letting users click into dead-ends.
  const canUpdate = abilities.can("update", "Credential")
  const canDelete = abilities.can("delete", "Credential")
  const [activeTab, setActiveTab] = React.useState<"all" | "needs">("all")
  const [search, setSearch] = React.useState("")
  const [filterTag, setFilterTag] = React.useState<string>("all")
  const [filterScope, setFilterScope] = React.useState<string>("all")
  const [sortKey, setSortKey] = React.useState<SortKey>("last_used")
  const [detailCredential, setDetailCredential] = React.useState<Credential | null>(null)
  const [detailOpen, setDetailOpen] = React.useState(false)
  const [rotateCredential, setRotateCredential] = React.useState<Credential | null>(null)
  const [rotateOpen, setRotateOpen] = React.useState(false)
  const [deleteCredential, setDeleteCredential] = React.useState<Credential | null>(null)
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(new Set())
  const [bulkDeleteOpen, setBulkDeleteOpen] = React.useState(false)
  const [bulkDeleting, setBulkDeleting] = React.useState(false)

  // Both fetchers THROW on failure so loadData can surface a real
  // error state — a failed fetch must never render as "no credentials
  // yet" (which invites re-creating secrets that already exist).
  const fetchWorkspace = React.useCallback(async () => {
    let res: Response
    try {
      res = await apiFetch("/api/v1/workspaces")
    } catch {
      throw new Error("Network error while loading the workspace.")
    }
    if (!res.ok) throw new Error(`Loading the workspace failed (HTTP ${res.status}).`)
    const orgs: Org[] = await res.json() ?? []
    if (orgs.length > 0) {
      setWorkspaceId(orgs[0].id)
      return orgs[0].id
    }
    return null
  }, [])

  const fetchCredentials = React.useCallback(async (oid: string) => {
    let res: Response
    try {
      res = await apiFetch(`/api/v1/credentials?workspace_id=${oid}`)
    } catch {
      throw new Error("Network error while loading credentials.")
    }
    if (!res.ok) throw new Error(`Loading credentials failed (HTTP ${res.status}).`)
    const data = await res.json()
    const normalised: Credential[] = (Array.isArray(data) ? data : []).map((c: Credential) => ({
      ...c,
      last_used_at: c.last_used_at ?? null,
      last_used_ips: Array.isArray(c.last_used_ips) ? c.last_used_ips : [],
      tags: Array.isArray(c.tags) ? c.tags : [],
    }))
    setCredentials(normalised)
  }, [])

  const loadData = React.useCallback(async () => {
    setLoading(true)
    setLoadError(null)
    try {
      let oid = workspaceId
      if (!oid) {
        oid = await fetchWorkspace()
      }
      if (oid) {
        await fetchCredentials(oid)
      }
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : "Something went wrong while loading credentials.")
    } finally {
      setLoading(false)
    }
  }, [workspaceId, fetchWorkspace, fetchCredentials])

  React.useEffect(() => { loadData() }, [loadData])

  const handleRefresh = React.useCallback(() => {
    if (!workspaceId) return
    fetchCredentials(workspaceId).catch((err) => {
      setLoadError(err instanceof Error ? err.message : "Something went wrong while loading credentials.")
    })
  }, [workspaceId, fetchCredentials])

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
      tags: credential.tags,
      token_expires_at: credential.token_expires_at,
    })
    setEditOpen(true)
  }

  function handleDelete(credential: Credential) { setDeleteCredential(credential) }

  async function confirmDeleteCredential() {
    if (!deleteCredential || !workspaceId) return
    try {
      const res = await apiFetch(`/api/v1/credentials/${deleteCredential.id}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (res.ok) handleRefresh()
    } catch { /* silently fail */ }
    finally { setDeleteCredential(null) }
  }

  function toggleSelected(id: string) {
    setSelectedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id); else next.add(id)
      return next
    })
  }

  async function bulkDelete() {
    if (!workspaceId) return
    setBulkDeleting(true)
    const ids = Array.from(selectedIds)
    try {
      const results = await Promise.allSettled(
        ids.map((id) =>
          apiFetch(`/api/v1/credentials/${id}?workspace_id=${workspaceId}`, { method: "DELETE" }),
        ),
      )
      const failedIds = ids.filter((_, i) => {
        const r = results[i]
        return r.status === "rejected" || !r.value.ok
      })
      const deleted = ids.length - failedIds.length
      if (failedIds.length === 0) {
        toast.success(`${deleted} credential${deleted === 1 ? "" : "s"} deleted`)
        setSelectedIds(new Set())
      } else {
        // Keep the failures selected so the user can retry the exact
        // remainder in one click instead of re-hunting the rows.
        toast.error(
          `${deleted} deleted, ${failedIds.length} failed — the failed credential${failedIds.length === 1 ? " stays" : "s stay"} selected`,
        )
        setSelectedIds(new Set(failedIds))
      }
      handleRefresh()
      setBulkDeleteOpen(false)
    } finally { setBulkDeleting(false) }
  }

  // KPI counts
  const kpis = React.useMemo(() => {
    let active = 0, errors = 0, expiring = 0, linked = 0
    const now = Date.now()
    const expiringWindow = 30 * 24 * 3600 * 1000
    for (const c of credentials) {
      const s = deriveStatus(c)
      if (s === "Connected") active++
      if (s === "Error") errors++
      if (c.token_expires_at) {
        const exp = new Date(c.token_expires_at).getTime()
        if (!Number.isNaN(exp) && exp > now && exp - now < expiringWindow) expiring++
      }
      if ((c._count_agent_credentials ?? 0) > 0) linked++
    }
    return { active, errors, expiring, linked }
  }, [credentials])

  const needsAttention = React.useMemo(
    () => credentials.filter((c) => {
      const s = deriveStatus(c)
      // Pending = agent-proposed, waiting for the human to approve/reject.
      if (s === "Error" || s === "Stale" || s === "Pending") return true
      if (c.token_expires_at) {
        const exp = new Date(c.token_expires_at).getTime()
        if (!Number.isNaN(exp) && exp - Date.now() < 30 * 24 * 3600 * 1000) return true
      }
      return false
    }),
    [credentials],
  )

  // How many of the needs-attention items are agent-proposed pending approvals
  // (vs true problem states) — drives the banner copy so we don't tell an
  // operator to "rotate/revoke" a credential that just needs approve/reject.
  const pendingCount = React.useMemo(
    () => credentials.filter((c) => deriveStatus(c) === "Pending").length,
    [credentials],
  )

  // Distinct tags from data — drives the filter dropdown so we never
  // show tags the workspace doesn't have.
  const tagsInUse = React.useMemo(() => {
    const set = new Set<string>()
    for (const c of credentials) {
      for (const t of c.tags ?? []) set.add(t)
    }
    return Array.from(set).sort()
  }, [credentials])

  const filtered = React.useMemo(() => {
    const base = activeTab === "needs" ? needsAttention : credentials
    return base.filter((c) => {
      if (filterTag !== "all" && !c.tags.includes(filterTag)) return false
      if (filterScope !== "all" && c.scope !== filterScope) return false
      if (search.trim()) {
        const q = search.toLowerCase()
        const hay = [c.name, c.account_label ?? "", c.description ?? "", ...(c.tags ?? [])].join(" ").toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })
  }, [credentials, needsAttention, activeTab, filterTag, filterScope, search])

  const sorted = React.useMemo(() => {
    const out = [...filtered]
    out.sort((a, b) => {
      // Errors always rank to the top so users see breakage on every
      // sort, regardless of which key they picked.
      const aErr = deriveStatus(a) === "Error" ? 0 : 1
      const bErr = deriveStatus(b) === "Error" ? 0 : 1
      if (aErr !== bErr) return aErr - bErr
      if (sortKey === "name") return a.name.localeCompare(b.name)
      if (sortKey === "created") return new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
      // last_used desc; nulls go to the bottom
      const aT = a.last_used_at ? new Date(a.last_used_at).getTime() : 0
      const bT = b.last_used_at ? new Date(b.last_used_at).getTime() : 0
      return bT - aT
    })
    return out
  }, [filtered, sortKey])

  const headerActions = canManage ? (
    <SubBarPrimary icon={Plus} onClick={() => setAddOpen(true)}>
      Add secret
    </SubBarPrimary>
  ) : null

  // Canonical page chrome: the SubBar (identity + actions) directly under the
  // global top bar, then a scrollable, padded content region — the same shape
  // journal/admin/routines use. SubBar provides no padding of its own, so the
  // content wrapper supplies it.
  const subBar = (
    <SubBar
      icon={Key}
      title="Credentials"
      ariaLabel="Credentials"
      description="Shared secrets, API keys, and CLI tokens for your agents"
      actions={headerActions}
    />
  )

  if (loading) {
    return (
      <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
        {subBar}
        <div className="flex-1 overflow-y-auto">
          <div className="p-4 md:p-6 space-y-3">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {subBar}
      <div className="flex-1 overflow-y-auto">
        <div className="p-4 md:p-6">
      {loadError ? (
        // Load failure — visually and semantically distinct from the
        // empty state: red accent, explicit error copy, and a Retry
        // affordance. Never claims "no credentials yet".
        <Card className="p-12 text-center border-red-500/30 bg-red-500/[0.03]" role="alert">
          <AlertTriangle className="mx-auto h-6 w-6 text-red-400" />
          <h2 className="mt-3 text-sm font-medium text-foreground">Couldn&apos;t load credentials</h2>
          <p className="mt-1 text-xs text-muted-foreground">{loadError}</p>
          <Button size="sm" variant="outline" className="mt-4" onClick={loadData}>
            <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
            Retry
          </Button>
        </Card>
      ) : credentials.length === 0 ? (
        <EmptyState
          icon={Key}
          title="No credentials yet"
          description="Add API keys, tokens, or secrets that your agents will use. All values are encrypted with AES-256-GCM."
        >
          {canManage && (
            <Button className="mt-4" onClick={() => setAddOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Add first secret
            </Button>
          )}
        </EmptyState>
      ) : (
        <div className="space-y-4">
          {/* KPI strip */}
          <div className="grid gap-4 grid-cols-2 sm:grid-cols-4">
            <KpiCard label="Active" value={kpis.active}
              valueColor={kpis.active > 0 ? "rgb(52, 211, 153)" : undefined}
              subtitle={`of ${credentials.length} total`} />
            <KpiCard label="Expiring" value={kpis.expiring}
              valueColor={kpis.expiring > 0 ? "rgb(251, 191, 36)" : undefined}
              subtitle="next 30 days" />
            <KpiCard label="Errors" value={kpis.errors}
              valueColor={kpis.errors > 0 ? "rgb(248, 113, 113)" : undefined}
              subtitle={kpis.errors > 0 ? "needs attention" : "all healthy"} />
            <KpiCard label="Linked agents" value={kpis.linked}
              subtitle={`across ${credentials.length} credential${credentials.length === 1 ? "" : "s"}`} />
          </div>

          {/* Tab strip */}
          <TabBar
            value={activeTab}
            onValueChange={(v) => setActiveTab(v as typeof activeTab)}
            layoutId="credentials-tabs-indicator"
            ariaLabel="Credential filter"
            className="h-9"
          >
            <TabBar.Item value="all">
              <span className="inline-flex items-center gap-1.5">
                All
                <span className="text-[10px] font-mono opacity-60">{credentials.length}</span>
              </span>
            </TabBar.Item>
            <TabBar.Item value="needs">
              <span className="inline-flex items-center gap-1.5">
                Needs attention
                {needsAttention.length > 0 && (
                  <Badge variant="destructive" className="h-4 px-1.5 text-[10px]">{needsAttention.length}</Badge>
                )}
              </span>
            </TabBar.Item>
          </TabBar>

          {/* Banner */}
          {needsAttention.length > 0 && activeTab === "all" && (
            <motion.div
              initial={{ opacity: 0, y: -4 }}
              animate={{ opacity: 1, y: 0 }}
              className="rounded-md border border-amber-500/30 bg-amber-500/[0.05] px-3 py-2.5 text-xs flex items-center gap-2"
            >
              <AlertTriangle className="h-3.5 w-3.5 text-amber-400 shrink-0" />
              <span className="text-foreground/90">
                <strong>{needsAttention.length}</strong> credential{needsAttention.length === 1 ? "" : "s"}
                {" "}need attention &mdash;{" "}
                {pendingCount === needsAttention.length
                  ? "approve or reject the agent-proposed ones."
                  : pendingCount > 0
                    ? "approve the pending ones; rotate, refresh, or revoke the rest before they break agent runs."
                    : "rotate, refresh, or revoke them before they break agent runs."}
              </span>
              <button onClick={() => setActiveTab("needs")} className="ml-auto text-amber-300 hover:text-amber-200 font-medium">
                Review →
              </button>
            </motion.div>
          )}

          {/* Filter row */}
          <div className="flex items-center gap-2 flex-wrap">
            <SidebarSearch
              value={search}
              onValueChange={setSearch}
              placeholder="Search by name, tag, or description…"
              className="min-w-[200px] max-w-md"
            />
            <Select value={filterTag} onValueChange={setFilterTag}>
              <SelectTrigger className="h-8 w-[140px] text-xs">
                <SelectValue placeholder="Tags" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All tags</SelectItem>
                {tagsInUse.map((t) => (
                  <SelectItem key={t} value={t}>{t}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={filterScope} onValueChange={setFilterScope}>
              <SelectTrigger className="h-8 w-[120px] text-xs">
                <SelectValue placeholder="Scope" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All scopes</SelectItem>
                <SelectItem value="WORKSPACE">Workspace</SelectItem>
                <SelectItem value="CREW">Crew</SelectItem>
              </SelectContent>
            </Select>
            <Select value={sortKey} onValueChange={(v) => setSortKey(v as SortKey)}>
              <SelectTrigger className="h-8 w-[150px] text-xs">
                <ArrowUpDown className="h-3 w-3 mr-1.5 opacity-60" />
                <SelectValue placeholder="Sort" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="last_used">Last used</SelectItem>
                <SelectItem value="name">Name</SelectItem>
                <SelectItem value="created">Recently added</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {/* Flat table — single sticky header, no grouping. */}
          {sorted.length === 0 ? (
            <Card className="p-12 text-center text-sm text-muted-foreground">
              No credentials match the current filters.
            </Card>
          ) : (
            <Card className="overflow-hidden p-0">
              {/* min-w keeps the fixed-width columns intact on narrow
                  screens; the Table's built-in overflow-x-auto container
                  (see components/ui/table.tsx) turns that into horizontal
                  scroll instead of column crush. */}
              <Table className="min-w-[720px]">
                <TableHeader className="sticky top-0 z-10 bg-card/95 backdrop-blur">
                  <TableRow>
                    <TableHead className="w-[28px]"></TableHead>
                    <TableHead className="w-[36px]"></TableHead>
                    <TableHead>Name</TableHead>
                    <TableHead className="w-[180px]">Tags</TableHead>
                    <TableHead className="w-[140px]">Used by</TableHead>
                    <TableHead className="w-[140px]">Last used</TableHead>
                    <TableHead className="w-[80px] text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sorted.map((cred) => (
                    <CredentialRow
                      key={cred.id}
                      cred={cred}
                      selected={selectedIds.has(cred.id)}
                      canUpdate={canUpdate}
                      canDelete={canDelete}
                      onToggleSelect={() => toggleSelected(cred.id)}
                      onOpen={() => { setDetailCredential(cred); setDetailOpen(true) }}
                      onEdit={() => handleEdit(cred)}
                      onDelete={() => handleDelete(cred)}
                    />
                  ))}
                </TableBody>
              </Table>
            </Card>
          )}
        </div>
      )}

      {workspaceId && (
        <AddSecretSheet
          workspaceId={workspaceId}
          open={addOpen}
          onOpenChange={setAddOpen}
          onSuccess={handleRefresh}
          knownTags={tagsInUse}
        />
      )}

      {workspaceId && (
        <CredentialDetailSheet
          workspaceId={workspaceId}
          credential={detailCredential}
          open={detailOpen}
          onOpenChange={(o) => { setDetailOpen(o); if (!o) setDetailCredential(null) }}
          onRefresh={handleRefresh}
          onEdit={(c) => handleEdit(c as Credential)}
          onRotate={(c) => {
            setRotateCredential(c as unknown as Credential)
            setRotateOpen(true)
          }}
        />
      )}

      {workspaceId && rotateCredential && (
        <RotationDialog
          workspaceId={workspaceId}
          credentialId={rotateCredential.id}
          credentialName={rotateCredential.name}
          open={rotateOpen}
          onOpenChange={(o) => { setRotateOpen(o); if (!o) setRotateCredential(null) }}
          onRotated={handleRefresh}
        />
      )}

      <AlertDialog open={!!deleteCredential} onOpenChange={(o) => !o && setDeleteCredential(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete credential?</AlertDialogTitle>
            <AlertDialogDescription>
              <span className="font-mono">{deleteCredential?.name}</span> will be permanently deleted.
              Agents that use this credential will start failing immediately. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction className="bg-destructive text-white hover:bg-destructive/90" onClick={confirmDeleteCredential}>
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={bulkDeleteOpen} onOpenChange={setBulkDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {selectedIds.size} credential{selectedIds.size === 1 ? "" : "s"}?</AlertDialogTitle>
            <AlertDialogDescription>
              All selected credentials will be permanently deleted. Any agents using them will fail immediately.
              This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={bulkDeleting}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={bulkDelete}
              disabled={bulkDeleting}
            >
              {bulkDeleting ? "Deleting..." : `Delete ${selectedIds.size}`}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {canDelete && selectedIds.size > 0 && (
        <div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-50 rounded-full border border-border bg-popover/95 backdrop-blur shadow-2xl px-4 py-2 flex items-center gap-3 text-xs">
          <span className="font-medium">{selectedIds.size} selected</span>
          <button type="button" onClick={() => setBulkDeleteOpen(true)} className="text-red-400 hover:text-red-300">
            Delete
          </button>
          <button type="button" onClick={() => setSelectedIds(new Set())} className="text-muted-foreground hover:text-foreground">
            Cancel
          </button>
        </div>
      )}

      {workspaceId && editCredential && (
        <EditCredentialDialog
          workspaceId={workspaceId}
          credential={editCredential}
          open={editOpen}
          onOpenChange={setEditOpen}
          onSuccess={handleRefresh}
          knownTags={tagsInUse}
        />
      )}
        </div>
      </div>
    </div>
  )
}

interface CredentialRowProps {
  cred: Credential
  selected: boolean
  /** CASL "update" on Credential — shows the Edit row action. */
  canUpdate: boolean
  /** CASL "delete" on Credential — shows Delete + the bulk-select checkbox. */
  canDelete: boolean
  onToggleSelect: () => void
  onOpen: () => void
  onEdit: () => void
  onDelete: () => void
}

// Single row, single style. Provider is an icon prefix on the name —
// not a column, not a group header. Type is a tiny inline badge.
function CredentialRow({ cred, selected, canUpdate, canDelete, onToggleSelect, onOpen, onEdit, onDelete }: CredentialRowProps) {
  const derived = deriveStatus(cred)
  const brand = getBrand(cred.provider)
  const BrandIcon = brand.Icon
  const expiresIn = cred.token_expires_at
    ? Math.floor((new Date(cred.token_expires_at).getTime() - Date.now()) / (24 * 3600 * 1000))
    : null
  const lastUsed = cred.last_used_at ? formatRelativeTime(cred.last_used_at) : null

  return (
    <TableRow
      className={cn("cursor-pointer row-hover transition-colors", selected && "row-selected")}
      onClick={onOpen}
    >
      <TableCell onClick={(e) => e.stopPropagation()}>
        {/* Bulk-select only exists to feed bulk delete — hide it from
            roles that can't delete so it doesn't promise an action
            that would 403. */}
        {canDelete && (
          <input
            type="checkbox"
            checked={selected}
            onChange={onToggleSelect}
            className="h-3.5 w-3.5 cursor-pointer accent-blue-500"
            aria-label={`Select ${cred.name}`}
          />
        )}
      </TableCell>
      <TableCell>
        <span
          className={cn(
            "inline-block h-2 w-2 rounded-full",
            STATUS_DOT_COLOR[derived],
            derived === "Connected" && "shadow-[0_0_0_2px_rgba(52,211,153,0.18)]",
          )}
          title={derived}
        />
      </TableCell>
      <TableCell>
        <div className="flex items-center gap-2 min-w-0">
          <BrandIcon
            className="h-4 w-4 shrink-0"
            style={{ color: brandColor(brand) }}
            aria-label={brand.label}
          />
          <span className="font-mono text-sm truncate">{cred.name}</span>
          {brand.cli && (
            <Badge
              variant="outline"
              className="text-[9px] px-1 font-mono shrink-0 border-blue-400/50 text-blue-300"
              title="Crewship uses this credential to authenticate the agent's CLI inside the container"
            >
              CLI
            </Badge>
          )}
          <Badge variant="outline" className="text-[9px] px-1 font-mono shrink-0 opacity-70">
            {TYPE_LABEL[cred.type]}
          </Badge>
          {derived === "Pending" && (
            // Deep-link straight to the inbox — the approve/reject
            // action lives there, so "go approve it" must be one
            // click, not a copy-the-hint-and-navigate scavenger hunt.
            <Link
              href="/inbox"
              onClick={(e) => e.stopPropagation()}
              className="shrink-0"
              title="Proposed by an agent — approve or reject it in the inbox"
            >
              <Badge
                variant="outline"
                className="text-[9px] h-4 px-1 border-amber-400/40 text-amber-300 font-mono hover:border-amber-300/70 hover:text-amber-200 transition-colors"
              >
                Pending approval →
              </Badge>
            </Link>
          )}
          {expiresIn !== null && expiresIn >= 0 && expiresIn <= 30 && (
            <Badge
              variant="outline"
              className="text-[9px] h-4 px-1 border-amber-400/40 text-amber-300 font-mono shrink-0"
              title={`Expires in ${expiresIn}d`}
            >
              {expiresIn}d
            </Badge>
          )}
        </div>
      </TableCell>
      <TableCell>
        <div className="flex items-center gap-1 flex-wrap">
          {cred.tags.length === 0 ? (
            <span className="text-[10px] text-muted-foreground-soft">—</span>
          ) : (
            cred.tags.slice(0, 3).map((t) => (
              <Badge key={t} variant="outline" className="text-[10px] px-1 font-mono">{t}</Badge>
            ))
          )}
          {cred.tags.length > 3 && (
            <span className="text-[10px] text-muted-foreground" title={cred.tags.slice(3).join(", ")}>
              +{cred.tags.length - 3}
            </span>
          )}
        </div>
      </TableCell>
      <TableCell>
        <span className="text-xs text-muted-foreground">
          {cred._count_agent_credentials > 0
            ? `${cred._count_agent_credentials} ${cred._count_agent_credentials === 1 ? "agent" : "agents"}`
            : <span className="text-muted-foreground-soft">—</span>}
          {cred.mcp_used && <span className="ml-1.5 text-[9px] text-blue-300">MCP</span>}
        </span>
      </TableCell>
      <TableCell>
        {lastUsed ? (
          <span className="text-xs text-muted-foreground inline-flex items-center gap-1.5">
            <Clock className="h-3 w-3 opacity-60" />
            {lastUsed}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground-soft">never</span>
        )}
      </TableCell>
      <TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-end gap-0.5">
          {canUpdate && (
            <Button variant="ghost" size="icon-xs" onClick={onEdit} title="Edit">
              <Pencil className="h-3.5 w-3.5" />
              <span className="sr-only">Edit</span>
            </Button>
          )}
          {canDelete && (
            <Button variant="ghost" size="icon-xs" onClick={onDelete} title="Delete">
              <Trash2 className="h-3.5 w-3.5 text-destructive" />
              <span className="sr-only">Delete</span>
            </Button>
          )}
        </div>
      </TableCell>
    </TableRow>
  )
}
