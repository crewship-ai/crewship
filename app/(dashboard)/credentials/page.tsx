"use client"

import * as React from "react"
import { motion } from "motion/react"
import {
  Key, Plus, Pencil, Trash2, Search,
  Bot, Lock, Terminal, CheckCircle, AlertTriangle, Clock, XCircle, ExternalLink,
  ChevronDown, ChevronRight,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageShell } from "@/components/layout/page-shell"
import { EmptyState } from "@/components/layout/empty-state"
import { Card } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusBadge } from "@/components/ui/status-badge"
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
import { AddCredentialWizard } from "@/components/features/credentials/add-credential-wizard"
import { CredentialDetailSheet } from "@/components/features/credentials/credential-detail-sheet"
import { RotationDialog } from "@/components/features/credentials/rotation-dialog"
import { EditCredentialDialog } from "@/components/features/credentials/edit-credential-dialog"
import type { CredentialData } from "@/components/features/credentials/edit-credential-dialog"
import { formatDate, formatRelativeTime } from "@/lib/time"
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
  // Backed by EPIC 1.4 (credential_audit + ringbuffer). Drives the
  // 5-state status taxonomy's Stale check (last_used_at < now-90d)
  // and the row-level "still in use?" signal copied from
  // GitLab/GitHub/Stripe.
  last_used_at: string | null
  last_used_ips: string[]
  created_at: string
  updated_at: string
  _count_agent_credentials: number
  agent_names: string[]
  mcp_used: boolean
}

// 5-state status taxonomy from CONNECTIONS.md §3.4 (Datadog parity).
// Stale + Detected are computed in the FE — they're not persisted DB
// columns. Stale = active credentials no agent has touched in 90d;
// Detected is reserved for the auto-detect feature in EPIC 5.3.
type DerivedStatus = "Available" | "Detected" | "Connected" | "Error" | "Stale"

const STALE_THRESHOLD_DAYS = 90

function deriveStatus(c: Credential): DerivedStatus {
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
  const [activeTab, setActiveTab] = React.useState<"all" | "needs">("all")
  const [search, setSearch] = React.useState("")
  const [filterProvider, setFilterProvider] = React.useState<string>("all")
  const [filterScope, setFilterScope] = React.useState<string>("all")
  const [filterType, setFilterType] = React.useState<string>("all")
  const [collapsedProviders, setCollapsedProviders] = React.useState<Set<string>>(new Set())
  const [detailCredential, setDetailCredential] = React.useState<Credential | null>(null)
  const [detailOpen, setDetailOpen] = React.useState(false)
  const [rotateCredential, setRotateCredential] = React.useState<Credential | null>(null)
  const [rotateOpen, setRotateOpen] = React.useState(false)

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
      // Defensive: backend now returns last_used_ips but pre-EPIC-1.4
      // databases may have NULL there; a missing field becomes
      // undefined and would crash .length checks downstream.
      const normalised: Credential[] = (Array.isArray(data) ? data : []).map((c: Credential) => ({
        ...c,
        last_used_at: c.last_used_at ?? null,
        last_used_ips: Array.isArray(c.last_used_ips) ? c.last_used_ips : [],
      }))
      setCredentials(normalised)
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

  // KPI counts — computed client-side from the in-memory list. Cheap.
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
      if (s === "Error" || s === "Stale") return true
      if (c.token_expires_at) {
        const exp = new Date(c.token_expires_at).getTime()
        if (!Number.isNaN(exp) && exp - Date.now() < 30 * 24 * 3600 * 1000) return true
      }
      return false
    }),
    [credentials],
  )

  const filtered = React.useMemo(() => {
    const base = activeTab === "needs" ? needsAttention : credentials
    return base.filter((c) => {
      if (filterProvider !== "all" && c.provider !== filterProvider) return false
      if (filterScope !== "all" && c.scope !== filterScope) return false
      if (filterType !== "all" && c.type !== filterType) return false
      if (search.trim()) {
        const q = search.toLowerCase()
        if (!c.name.toLowerCase().includes(q) && !(c.account_label ?? "").toLowerCase().includes(q)) return false
      }
      return true
    })
  }, [credentials, needsAttention, activeTab, filterProvider, filterScope, filterType, search])

  // Group by provider for the collapsible-section list. Sort: providers
  // with most credentials first; ties broken alphabetically.
  const grouped = React.useMemo(() => {
    const map = new Map<string, Credential[]>()
    for (const c of filtered) {
      const arr = map.get(c.provider) ?? []
      arr.push(c)
      map.set(c.provider, arr)
    }
    const entries = Array.from(map.entries())
    entries.sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]))
    return entries
  }, [filtered])

  // Distinct providers for the filter dropdown — pulled from the data
  // we have, not the static enum, so dropdown only shows what the
  // workspace actually owns.
  const providersInUse = React.useMemo(() => {
    const set = new Set<string>()
    for (const c of credentials) set.add(c.provider)
    return Array.from(set).sort()
  }, [credentials])

  function toggleProviderCollapsed(provider: string) {
    setCollapsedProviders((prev) => {
      const next = new Set(prev)
      if (next.has(provider)) next.delete(provider)
      else next.add(provider)
      return next
    })
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
        <div className="space-y-4">
          {/* KPI strip — 4 cards, animated nums (CONNECTIONS.md §4.1) */}
          <div className="grid gap-4 grid-cols-2 sm:grid-cols-4">
            <KpiCard
              label="Active"
              value={kpis.active}
              valueColor={kpis.active > 0 ? "rgb(52, 211, 153)" : undefined}
              subtitle={`of ${credentials.length} total`}
            />
            <KpiCard
              label="Expiring"
              value={kpis.expiring}
              valueColor={kpis.expiring > 0 ? "rgb(251, 191, 36)" : undefined}
              subtitle="next 30 days"
            />
            <KpiCard
              label="Errors"
              value={kpis.errors}
              valueColor={kpis.errors > 0 ? "rgb(248, 113, 113)" : undefined}
              subtitle={kpis.errors > 0 ? "needs attention" : "all healthy"}
            />
            <KpiCard
              label="Linked agents"
              value={kpis.linked}
              subtitle={`across ${credentials.length} credential${credentials.length === 1 ? "" : "s"}`}
            />
          </div>

          {/* Tab strip — All / Needs attention (CONNECTIONS.md §4.1) */}
          <div className="flex items-center gap-0 border-b border-white/[0.08]">
            <button
              onClick={() => setActiveTab("all")}
              className={cn(
                "flex items-center gap-1.5 px-3 h-9 text-xs font-medium border-b-2 transition-colors -mb-px",
                activeTab === "all"
                  ? "border-blue-400 text-blue-400"
                  : "border-transparent text-muted-foreground hover:text-foreground/80",
              )}
            >
              All
              <span className="text-[10px] font-mono opacity-60">{credentials.length}</span>
            </button>
            <button
              onClick={() => setActiveTab("needs")}
              className={cn(
                "flex items-center gap-1.5 px-3 h-9 text-xs font-medium border-b-2 transition-colors -mb-px",
                activeTab === "needs"
                  ? "border-blue-400 text-blue-400"
                  : "border-transparent text-muted-foreground hover:text-foreground/80",
              )}
            >
              Needs attention
              {needsAttention.length > 0 && (
                <Badge variant="destructive" className="h-4 px-1.5 text-[10px]">
                  {needsAttention.length}
                </Badge>
              )}
            </button>
          </div>

          {/* Filter row */}
          <div className="flex items-center gap-2 flex-wrap">
            <div className="relative flex-1 min-w-[200px] max-w-md">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
              <Input
                placeholder="Search by name or label..."
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="pl-8 h-8"
              />
            </div>
            <Select value={filterProvider} onValueChange={setFilterProvider}>
              <SelectTrigger className="h-8 w-[140px] text-xs">
                <SelectValue placeholder="Provider" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All providers</SelectItem>
                {providersInUse.map((p) => (
                  <SelectItem key={p} value={p}>{PROVIDER_LABELS[p] ?? p}</SelectItem>
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
            <Select value={filterType} onValueChange={setFilterType}>
              <SelectTrigger className="h-8 w-[140px] text-xs">
                <SelectValue placeholder="Type" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All types</SelectItem>
                <SelectItem value="AI_CLI_TOKEN">AI CLI Token</SelectItem>
                <SelectItem value="API_KEY">API Key</SelectItem>
                <SelectItem value="CLI_TOKEN">CLI Token</SelectItem>
                <SelectItem value="SECRET">Secret</SelectItem>
                <SelectItem value="OAUTH2">OAuth 2.0</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {/* Provider-grouped list (CONNECTIONS.md §3.3 multi-account UX) */}
          {grouped.length === 0 ? (
            <Card className="p-12 text-center text-sm text-muted-foreground">
              No credentials match the current filters.
            </Card>
          ) : (
            <div className="space-y-3">
              {grouped.map(([provider, items], groupIdx) => {
                const isCollapsed = collapsedProviders.has(provider)
                const providerColor = PROVIDER_ICON_COLOR[provider] ?? "text-muted-foreground"
                return (
                  <motion.div
                    key={provider}
                    initial={{ opacity: 0, y: 4 }}
                    animate={{ opacity: 1, y: 0 }}
                    transition={{ duration: 0.15, delay: groupIdx * 0.02 }}
                  >
                    <Card className="overflow-hidden p-0">
                      <button
                        type="button"
                        onClick={() => toggleProviderCollapsed(provider)}
                        className="w-full flex items-center gap-2 px-4 py-2.5 text-left hover:bg-white/[0.02] transition-colors"
                      >
                        {isCollapsed ? (
                          <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/60" />
                        ) : (
                          <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
                        )}
                        <span className={cn("text-sm font-medium", providerColor)}>
                          {PROVIDER_LABELS[provider] ?? provider}
                        </span>
                        <span className="text-[10px] font-mono text-muted-foreground/60">
                          {items.length}
                        </span>
                      </button>
                      {!isCollapsed && (
                        <Table>
                          <TableHeader>
                            <TableRow>
                              <TableHead className="w-[36px]"></TableHead>
                              <TableHead>Name</TableHead>
                              <TableHead>Type</TableHead>
                              <TableHead>Status</TableHead>
                              <TableHead>Used by</TableHead>
                              <TableHead>Last used</TableHead>
                              <TableHead className="text-right">Actions</TableHead>
                            </TableRow>
                          </TableHeader>
                          <TableBody>
                            {items.map((cred) => {
                              const typeConfig = TYPE_CONFIG[cred.type]
                              const TypeIcon = typeConfig.icon
                              const typeColor = CREDENTIAL_TYPE_ICON_COLOR[cred.type] ?? "text-muted-foreground"
                              const derived = deriveStatus(cred)
                              const showStatus = cred.type !== "SECRET"
                              const lastUsed = cred.last_used_at ? formatRelativeTime(cred.last_used_at) : null
                              return (
                                <TableRow
                                  key={cred.id}
                                  className="cursor-pointer hover:bg-white/[0.02]"
                                  onClick={() => { setDetailCredential(cred); setDetailOpen(true) }}
                                >
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
                                    {showStatus ? (
                                      <Badge variant="outline" className="text-[10px] font-medium gap-1.5">
                                        <span className={cn("h-1.5 w-1.5 rounded-full", STATUS_DOT_COLOR[derived])} />
                                        {derived}
                                      </Badge>
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
                                    {lastUsed ? (
                                      <span
                                        className="text-body text-muted-foreground inline-flex items-center gap-1.5"
                                        title={cred.last_used_ips.length > 0 ? `Last 5 IPs: ${cred.last_used_ips.join(", ")}` : undefined}
                                      >
                                        <Clock className="h-3 w-3 opacity-60" />
                                        {lastUsed}
                                        {cred.last_used_ips.length > 0 && (
                                          <span className="text-[10px] font-mono opacity-60">
                                            · {cred.last_used_ips.length} IP{cred.last_used_ips.length === 1 ? "" : "s"}
                                          </span>
                                        )}
                                      </span>
                                    ) : (
                                      <span className="text-label text-muted-foreground/60">never</span>
                                    )}
                                  </TableCell>
                                  <TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
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
                      )}
                    </Card>
                  </motion.div>
                )
              })}
            </div>
          )}
        </div>
      )}

      {workspaceId && (
        <AddCredentialWizard
          workspaceId={workspaceId}
          open={addOpen}
          onOpenChange={setAddOpen}
          onSuccess={handleRefresh}
        />
      )}

      {workspaceId && (
        <CredentialDetailSheet
          workspaceId={workspaceId}
          credential={detailCredential}
          open={detailOpen}
          onOpenChange={(o) => { setDetailOpen(o); if (!o) setDetailCredential(null) }}
          onRefresh={handleRefresh}
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
