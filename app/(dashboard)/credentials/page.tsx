"use client"

/**
 * The Vault (/credentials) — Harbor 2.
 *
 * Template "Explorer + entity": the SecondaryPanel picks a space (Secrets, AI
 * providers), a view and facets; the content is the Secrets table with its
 * health strip, or a secret's own page with tabs. Everything that decides what
 * is on screen lives in the URL (lib/credentials/vault-url-state.ts), so links
 * from other pages — `?id=`, `?crew=`, `?class=`, `?tab=providers` — land on
 * exactly that, and a reload keeps it.
 *
 * The AI providers space (OWNER/ADMIN) is the model accounts: a health strip,
 * cards or a list, a provider's page (`?provider=`), an account group's page
 * (`?pool=`) and each account's own page. Keys 1 and 2 switch the spaces; the
 * last space is remembered per person and a bare /credentials opens it.
 *
 * A load failure is never drawn as an empty vault (#1085): an empty vault
 * invites re-creating secrets that already exist.
 */

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueries } from "@tanstack/react-query"
import { Key, Plus, ShieldCheck, Sparkles, Terminal, Trash2, X } from "lucide-react"

import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { SubBar, SubBarIconButton, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { PageEmpty, PageError, PageLoading, PageNoResults, PageNotFound } from "@/components/layout/page-state"
import { SecondaryPanel, useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { AddSecretSheet } from "@/components/features/credentials/add-secret-sheet"
import { CredentialDetailSheet } from "@/components/features/credentials/credential-detail-sheet"
import { RotationDialog } from "@/components/features/credentials/rotation-dialog"
import { AccountsList, type AccountTokenUsage, ProviderHead, ProviderHealth, providerLabel } from "@/components/features/credentials/provider-accounts"
import { PROVIDER_VIEW_LABEL, ProvidersPanel } from "@/components/features/credentials/providers-panel"
import { AccountGroupPage } from "@/components/features/credentials/account-group-page"
import { NewAccountGroupDialog } from "@/components/features/credentials/new-account-group-dialog"
import { AddProviderDialog } from "@/components/features/credentials/add-provider-dialog"
import { SecretsTable, type VaultSecret } from "@/components/features/credentials/secrets-table"
import { VaultHealth, buildVaultHealth } from "@/components/features/credentials/vault-health"
import { VIEW_LABEL, VaultPanel, VaultSpaces, buildClassFacet } from "@/components/features/credentials/vault-panel"
import { BulkDeleteDialog, BulkTierDialog } from "@/components/features/credentials/bulk-actions"
import { NewFolderDialog } from "@/components/features/credentials/new-folder-dialog"
import { EditFolderDialog } from "@/components/features/credentials/edit-folder-dialog"
import { VaultSpaceCrumb } from "@/components/features/credentials/vault-space-crumb"
import { VaultCliDialog } from "@/components/features/credentials/vault-cli-dialog"
import type { WizardInitial } from "@/components/features/credentials/add-credential-wizard"
import {
  EMPTY_LOGIN_FILTERS,
  applyLoginFilters,
  buildLoginModeFacet,
  buildLoginOwnerFacet,
  buildLoginProviderFacet,
  buildProviderHealth,
  hasLogin,
  sortLogins,
  type LoginFilters,
  type ProviderLogin,
} from "@/lib/credentials/provider-logins"
import { loginProvider } from "@/lib/credentials/login-providers"
import { poolKeys, poolRequest, useProviderPools, type ProviderPool } from "@/hooks/use-provider-pools"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useSessionSafe } from "@/hooks/use-auth"
import { Capability } from "@/lib/capabilities"
import { useAbilities } from "@/hooks/use-abilities"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { useCredentialSpendThisMonth, useSubscriptionUsage } from "@/hooks/use-paymaster"
import { useCredentialReadiness } from "@/hooks/use-credential-readiness"
import { useWorkspaceAgentDirectory } from "@/hooks/use-workspace-agent-directory"
import {
  EMPTY_CREDENTIAL_FILTERS,
  applyCredentialFilters,
  buildAgentFacet,
  buildBrandFacet,
  buildScopeFacet,
  buildShapeFacet,
  buildTagFacet,
  credentialCrewIds,
  deriveSecretState,
  matchesView,
  type CredentialFacetOption,
  type CredentialFilters,
} from "@/lib/credentials/facets"
import { buildTierFacet } from "@/lib/credentials/tiers"
import {
  DEFAULT_PROVIDERS_STATE,
  activeFacetCount,
  activeLoginFacetCount,
  buildVaultQuery,
  hasExplicitVaultState,
  parseVaultUrl,
  resolveCrewTokens,
  type ProvidersUrlState,
  type SecretTab,
  type VaultSpace,
  type VaultUrlState,
} from "@/lib/credentials/vault-url-state"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { useCredentialFolders } from "@/hooks/use-credential-folders"
import { folderSelectionIds, type CredentialFolder } from "@/lib/credentials/folders"

interface Credential extends VaultSecret {
  description: string | null
  type: "PROVIDER_LOGIN" | "AI_CLI_TOKEN" | "API_KEY" | "CLI_TOKEN" | "SECRET" | "OAUTH2"
       | "USERPASS" | "SSH_KEY" | "CERTIFICATE" | "GENERIC_SECRET"
  provider: string
  status: string
  scope: "WORKSPACE" | "CREW"
  crew_id: string | null
  crew_ids: string[]
  account_label: string | null
  account_email: string | null
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
  agent_ids?: string[]
  mcp_used: boolean
  testable?: boolean
  security_level?: number
  security_level_label?: string
  sensitivity?: string | null
  login?: ProviderLogin | null
}

/** How many rows one request asks for — the list endpoint's ceiling. */
const LIST_LIMIT = 500

/** Account groups whose members are read for the "Group" column and grouping. */
const POOL_DETAIL_LIMIT = 25

/** True when a key press belongs to a field, not to the page. */
function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null
  if (!el || typeof el.closest !== "function") return false
  return Boolean(el.closest("input, textarea, select, [contenteditable=''], [contenteditable='true'], [role=textbox], [role=combobox]"))
}

const normalise = (c: Credential): Credential => ({
  ...c,
  last_used_at: c.last_used_at ?? null,
  last_used_ips: Array.isArray(c.last_used_ips) ? c.last_used_ips : [],
  tags: Array.isArray(c.tags) ? c.tags : [],
  crew_ids: Array.isArray(c.crew_ids) ? c.crew_ids : [],
  agent_names: Array.isArray(c.agent_names) ? c.agent_names : [],
})

export default function CredentialsPage() {
  const { abilities, hasCapability, loading: abilitiesLoading } = useAbilities()
  const session = useSessionSafe()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const router = useRouter()
  const searchParams = useSearchParams()
  const isMobile = useIsMobile()
  const closePanelSheet = useCloseSecondaryPanelSheet()

  // Layered like the backend (requireRoleOrCapabilityOrForbid): MANAGER+ via
  // role, or any member holding an explicit credential.create grant (#1034).
  const canManage = abilities.can("create", "Credential") || hasCapability(Capability.CredentialCreate)
  // DELETE is OWNER/ADMIN ("manage" → CASL "delete").
  const canDelete = abilities.can("delete", "Credential")
  // PATCH (tier) is MANAGER+.
  const canEdit = abilities.can("update", "Credential")
  // Provider logins, pools and bindings are OWNER/ADMIN.
  const canBind = abilities.can("manage", "Credential")

  // ── The URL is the state ────────────────────────────────────────────────
  const url = React.useMemo(() => parseVaultUrl(searchParams), [searchParams])
  const urlRef = React.useRef(url)
  urlRef.current = url
  const go = React.useCallback(
    (patch: Partial<VaultUrlState>, mode: "push" | "replace" = "replace") => {
      const next = { ...urlRef.current, ...patch }
      router[mode](`/credentials${buildVaultQuery(next)}`, { scroll: false })
    },
    [router],
  )
  // A MEMBER following `?tab=providers` lands on Secrets and is told why.
  const providersDenied = url.space === "providers" && !canBind
  const space: VaultSpace = providersDenied ? "secrets" : url.space

  const [credentials, setCredentials] = React.useState<Credential[]>([])
  // The AI providers list — `?kind=provider_login`. Its failure is its own:
  // a broken seats endpoint must not blank the secrets list.
  const [providerLogins, setProviderLogins] = React.useState<Credential[]>([])
  const [loginsError, setLoginsError] = React.useState<string | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [loadError, setLoadError] = React.useState<string | null>(null)
  const [addOpen, setAddOpen] = React.useState(false)
  const [newPoolOpen, setNewPoolOpen] = React.useState(false)
  const [newFolderOpen, setNewFolderOpen] = React.useState(false)
  const [editFolder, setEditFolder] = React.useState<CredentialFolder | null>(null)
  const [providerDialog, setProviderDialog] = React.useState<{ initial?: WizardInitial } | null>(null)
  const [cliOpen, setCliOpen] = React.useState(false)
  const [addInitial, setAddInitial] = React.useState<WizardInitial | undefined>(undefined)
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(new Set())
  const [bulkDeleteOpen, setBulkDeleteOpen] = React.useState(false)
  const [bulkTierOpen, setBulkTierOpen] = React.useState(false)
  const [rotateCredential, setRotateCredential] = React.useState<{ cred: Credential; mode: "rotate" | "patch" } | null>(null)
  // A linked secret that is not in the list (beyond the page, or deleted).
  const [linked, setLinked] = React.useState<{ id: string; state: "loading" | "missing" | "error"; row?: Credential } | null>(null)

  const readiness = useCredentialReadiness(workspaceId)
  const folderApi = useCredentialFolders(workspaceId, space === "secrets")
  const folderIds = React.useMemo(() => folderSelectionIds(folderApi.folders, url.filters.folder), [folderApi.folders, url.filters.folder])
  const agentDirectory = useWorkspaceAgentDirectory(workspaceId ?? undefined)
  const agentCrew = React.useMemo(() => {
    const m = new Map<string, string>()
    for (const a of (agentDirectory.agents ?? []) as { id: string; crew_id?: string | null }[]) {
      if (a.crew_id) m.set(a.id, a.crew_id)
    }
    return m
  }, [agentDirectory.agents])

  const abortRef = React.useRef<AbortController | null>(null)

  // Throws on failure so the page can show an error state — never "empty".
  const fetchCredentials = React.useCallback(async (oid: string, signal?: AbortSignal) => {
    let res: Response
    let loginsRes: Response | null = null
    try {
      const ws = encodeURIComponent(oid)
      const [a, b] = await Promise.all([
        apiFetch(`/api/v1/credentials?workspace_id=${ws}&limit=${LIST_LIMIT}`, { signal }),
        canBind ? apiFetch(`/api/v1/credentials?workspace_id=${ws}&kind=provider_login&limit=${LIST_LIMIT}`, { signal }).catch((err) => {
          if ((err as { name?: string })?.name === "AbortError") throw err
          return null
        }) : Promise.resolve(null),
      ])
      res = a
      loginsRes = b
    } catch (err) {
      if ((err as { name?: string })?.name === "AbortError") throw err
      throw new Error("Network error while loading the vault.")
    }
    if (signal?.aborted) return
    if (!res.ok) throw new Error(`Loading the vault failed (HTTP ${res.status}).`)
    const data = await res.json()
    if (signal?.aborted) return
    const rows: Credential[] = (Array.isArray(data) ? data : []).map(normalise)
    setCredentials(rows)
    // #1085: any successful load clears a stale error.
    setLoadError(null)

    if (!canBind) {
      setProviderLogins([])
      setLoginsError(null)
    } else if (loginsRes && loginsRes.ok) {
      const seats = await loginsRes.json().catch(() => [])
      if (signal?.aborted) return
      setProviderLogins((Array.isArray(seats) ? seats : []).map(normalise).filter(hasLogin))
      setLoginsError(null)
    } else {
      setProviderLogins(rows.filter(hasLogin))
      setLoginsError(loginsRes ? `HTTP ${loginsRes.status}` : "network error")
    }
  }, [canBind])

  const loadData = React.useCallback(async () => {
    if (wsLoading) return
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setLoadError(null)
    try {
      if (workspaceId) await fetchCredentials(workspaceId, controller.signal)
      else {
        setCredentials([])
        setProviderLogins([])
      }
    } catch (err) {
      if (controller.signal.aborted || (err as { name?: string })?.name === "AbortError") return
      setLoadError(err instanceof Error ? err.message : "Something went wrong while loading the vault.")
    } finally {
      if (abortRef.current === controller) setLoading(false)
    }
  }, [wsLoading, workspaceId, fetchCredentials])

  React.useEffect(() => {
    loadData()
    return () => { abortRef.current?.abort() }
  }, [loadData])

  // Selection and seat facets are per workspace: a stale id must never reach
  // a DELETE in the next workspace.
  React.useEffect(() => {
    setSelectedIds(new Set())
    setAddOpen(false)
    setProviderDialog(null)
    if (!canBind) setProviderLogins([])
  }, [workspaceId, canBind])

  /** A refresh while data is on screen: a failure is a toast, never the error page (#1085). */
  const handleRefresh = React.useCallback(() => {
    if (!workspaceId) return
    fetchCredentials(workspaceId).catch((err) => {
      toast.error(err instanceof Error ? err.message : "Couldn't refresh the vault.")
    })
  }, [workspaceId, fetchCredentials])

  const openAdd = React.useCallback((initial?: WizardInitial) => {
    if (initial?.itemType === "PROVIDER_LOGIN") {
      if (!canBind) return
      setAddOpen(false)
      setProviderDialog({ initial })
      return
    }
    // Adding while a folder is open files the new secret there.
    const openFolder = url.filters.folder.length === 1 ? url.filters.folder[0] : undefined
    setAddInitial(initial ?? (openFolder ? { folderId: openFolder } : undefined))
    setAddOpen(true)
  }, [canBind, url.filters.folder])
  /** Add AI provider, optionally with the provider already chosen (a provider's page). */
  const openAddProvider = React.useCallback((provider?: string) => {
    if (!provider) { openAdd({ itemType: "PROVIDER_LOGIN" }); return }
    const p = loginProvider(provider)
    openAdd({
      itemType: "PROVIDER_LOGIN",
      provider: provider.toUpperCase(),
      loginMode: p?.subscription && provider.toUpperCase() !== "GOOGLE" ? "subscription" : "api_key",
      signIn: provider.toUpperCase() === "OPENAI" ? "device" : "paste",
      step: "values",
      name: p?.label,
    })
  }, [openAdd])

  // ── Secrets: the vault minus the AI provider accounts ───────────────────
  const loginIds = React.useMemo(() => {
    const ids = new Set(providerLogins.map((c) => c.id))
    for (const c of credentials) if (hasLogin(c)) ids.add(c.id)
    return ids
  }, [credentials, providerLogins])
  const secrets = React.useMemo(() => credentials.filter((c) => !loginIds.has(c.id)), [credentials, loginIds])

  const filters: CredentialFilters = React.useMemo(
    () => ({ ...url.filters, crew: resolveCrewTokens(url.filters.crew, readiness.crewsById) }),
    [url.filters, readiness.crewsById],
  )
  const setFilters = (next: CredentialFilters) => {
    setSelectedIds(new Set())
    go({ filters: next, id: null, tab: null })
  }

  const now = Date.now()
  const missing = readiness.missingToolIds
  const viewCounts = React.useMemo(() => {
    const count = (v: "all" | "attention" | "recent" | "unused" | "missing-tool") => secrets.filter((c) => matchesView(c, v, missing)).length
    return { all: secrets.length, attention: count("attention"), recent: count("recent"), unused: count("unused"), "missing-tool": count("missing-tool") }
  }, [secrets, missing])
  const health = React.useMemo(() => buildVaultHealth(secrets), [secrets])

  const crewFacet = React.useMemo<CredentialFacetOption[]>(() => {
    const counts = new Map<string, number>()
    for (const c of secrets) for (const id of credentialCrewIds(c, agentCrew)) counts.set(id, (counts.get(id) ?? 0) + 1)
    return [...counts.entries()]
      .map(([id, count]) => ({ value: id, label: readiness.crewsById[id]?.name ?? id, count }))
      .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
  }, [secrets, agentCrew, readiness.crewsById])

  const facets = React.useMemo(() => ({
    shapes: buildShapeFacet(secrets),
    brands: buildBrandFacet(secrets),
    scopes: buildScopeFacet(secrets, readiness.crewNames),
    tiers: buildTierFacet(secrets),
    classes: buildClassFacet(secrets),
    tags: buildTagFacet(secrets),
    agents: buildAgentFacet(secrets),
  }), [secrets, readiness.crewNames])

  const visible = React.useMemo(() => {
    const out = applyCredentialFilters(secrets, filters, missing, { agentCrew, folderIds })
    // Broken first, then most recently used — "what is wrong" before "what is busy".
    const rank = { rejected: 0, pending: 1, expiring: 2, ready: 3, unused: 4 } as const
    return out.sort((a, b) => {
      const d = rank[deriveSecretState(a).key] - rank[deriveSecretState(b).key]
      if (d !== 0) return d
      const at = a.last_used_at ? Date.parse(a.last_used_at) : 0
      const bt = b.last_used_at ? Date.parse(b.last_used_at) : 0
      return bt - at || a.name.localeCompare(b.name)
    })
  }, [secrets, filters, missing, agentCrew, folderIds])

  // Only what is on screen can be acted on — a tick made under another filter
  // must not delete a secret the person can no longer see.
  const visibleSelection = React.useMemo(() => visible.filter((c) => selectedIds.has(c.id)), [visible, selectedIds])
  const canSelect = canDelete || canEdit

  // ── AI providers ────────────────────────────────────────────────────────
  const pv: ProvidersUrlState = url.providers
  const loginFilters = pv.filters
  const me = React.useMemo(() => ({ id: session.data?.user?.id ?? null, email: session.data?.user?.email ?? null }), [session.data?.user?.id, session.data?.user?.email])
  const setLoginFilters = (next: LoginFilters) => go({ providers: { ...pv, filters: next, pool: null, poolTab: null }, id: null, tab: null })
  const providerViewCounts = React.useMemo(() => {
    const n = (status: LoginFilters["status"]) => applyLoginFilters(providerLogins, { ...EMPTY_LOGIN_FILTERS, status }, Date.now(), me).length
    return { all: providerLogins.length, attention: n("attention"), unassigned: n("unassigned"), mine: n("mine") }
  }, [providerLogins, me])
  const providerHealth = React.useMemo(() => buildProviderHealth(providerLogins), [providerLogins])
  const loginProviders = React.useMemo(() => buildLoginProviderFacet(providerLogins), [providerLogins])
  const loginModes = React.useMemo(() => buildLoginModeFacet(providerLogins), [providerLogins])
  const loginOwners = React.useMemo(() => buildLoginOwnerFacet(providerLogins), [providerLogins])
  const visibleLogins = React.useMemo(() => sortLogins(applyLoginFilters(providerLogins, loginFilters, Date.now(), me)), [providerLogins, loginFilters, me])

  // Account groups: the panel's list, and their members for the Group column.
  const inProviders = space === "providers" && canBind && Boolean(workspaceId)
  const subUsage = useSubscriptionUsage("30d", 0, inProviders)
  const keySpend = useCredentialSpendThisMonth(0, inProviders)
  const accountUsage = React.useMemo(() => {
    const m: Record<string, AccountTokenUsage> = {}
    for (const r of subUsage.data?.rows ?? []) {
      if (!r.credential_id) continue
      const cur = m[r.credential_id] ?? { tokens: 0, calls: 0 }
      m[r.credential_id] = { ...cur, tokens: cur.tokens + r.input_tokens + r.output_tokens, calls: cur.calls + r.call_count }
    }
    // An API key with no spend yet this month reads $0.00, not "no data".
    if (keySpend.data && !keySpend.error) {
      for (const login of providerLogins) {
        if (login.login?.mode !== "api_key") continue
        const cur = m[login.id] ?? { tokens: 0, calls: 0 }
        m[login.id] = { ...cur, spendUsd: 0 }
      }
      for (const r of keySpend.data.rows) {
        const cur = m[r.credential_id] ?? { tokens: 0, calls: 0 }
        m[r.credential_id] = { ...cur, spendUsd: r.cost_usd }
      }
    }
    return m
  }, [subUsage.data, keySpend.data, keySpend.error, providerLogins])
  const poolsApi = useProviderPools(workspaceId ?? "", inProviders)
  const poolItems: ProviderPool[] | undefined = poolsApi.pools?.items
  const poolDetails = useQueries({
    queries: (poolItems ?? []).slice(0, POOL_DETAIL_LIMIT).map((pool) => ({
      queryKey: poolKeys.detail(workspaceId ?? "", pool.id),
      enabled: inProviders,
      retry: false,
      queryFn: async ({ signal }: { signal: AbortSignal }) =>
        (await poolRequest(workspaceId ?? "", `provider-logins/pools/${encodeURIComponent(pool.id)}`, { signal })).json() as Promise<ProviderPool>,
    })),
  })
  const poolByAccount = React.useMemo(() => {
    const m = new Map<string, string>()
    for (const q of poolDetails) for (const mem of q.data?.members ?? []) if (!m.has(mem.credential_id)) m.set(mem.credential_id, q.data!.name)
    return m
  }, [poolDetails])
  const poolOf = React.useCallback((id: string) => poolByAccount.get(id) ?? null, [poolByAccount])

  // A provider's page: exactly one provider chosen.
  const providerPage = space === "providers" && !pv.pool && loginFilters.provider.length === 1 ? loginFilters.provider[0] : null
  const [workspaceDefaults, setWorkspaceDefaults] = React.useState<{ ws: string; rows: { credential_id: string; credential_name?: string }[] } | null>(null)
  React.useEffect(() => {
    if (!providerPage || !workspaceId || !canBind || workspaceDefaults?.ws === workspaceId) return
    let cancelled = false
    apiFetch(`/api/v1/credentials/bindings?workspace_id=${encodeURIComponent(workspaceId)}&scope=WORKSPACE`)
      .then((r) => (r.ok ? r.json() : { bindings: [] }))
      .then((body: { bindings?: { credential_id: string; credential_name?: string }[] }) => {
        if (!cancelled) setWorkspaceDefaults({ ws: workspaceId, rows: Array.isArray(body?.bindings) ? body.bindings : [] })
      })
      .catch(() => { if (!cancelled) setWorkspaceDefaults({ ws: workspaceId, rows: [] }) })
    return () => { cancelled = true }
  }, [providerPage, workspaceId, canBind, workspaceDefaults?.ws])
  const providerDefault = React.useMemo(() => {
    if (!providerPage || workspaceDefaults?.ws !== workspaceId) return undefined
    const ids = new Set(providerLogins.filter((c) => (c.login?.provider ?? c.provider).toUpperCase() === providerPage).map((c) => c.id))
    const hit = workspaceDefaults.rows.find((b) => ids.has(b.credential_id))
    return hit ? hit.credential_name ?? providerLogins.find((c) => c.id === hit.credential_id)?.name ?? null : null
  }, [providerPage, workspaceDefaults, workspaceId, providerLogins])

  const tagsInUse = React.useMemo(() => Array.from(new Set(credentials.flatMap((c) => c.tags ?? []))).sort(), [credentials])

  // ── The open secret ─────────────────────────────────────────────────────
  const listed = url.id ? providerLogins.find((c) => c.id === url.id) ?? credentials.find((c) => c.id === url.id) ?? null : null
  // Only the newest lookup may land. Not an effect cleanup: marking the lookup
  // "loading" changes linked?.id and re-runs this effect, and a cleanup would
  // then discard the very response it is waiting for.
  const linkedRequest = React.useRef(0)
  React.useEffect(() => {
    const id = url.id
    if (!id || loading || loadError || listed || !workspaceId) {
      if (!id) { linkedRequest.current++; setLinked(null) }
      return
    }
    if (linked?.id === id) return
    const request = ++linkedRequest.current
    const current = () => request === linkedRequest.current
    setLinked({ id, state: "loading" })
    apiFetch(`/api/v1/credentials/${encodeURIComponent(id)}?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then(async (r) => {
        if (!current()) return
        if (r.status === 404 || r.status === 403) { setLinked({ id, state: "missing" }); return }
        if (!r.ok) throw new Error()
        const row = normalise(await r.json())
        if (current()) setLinked({ id, state: "loading", row })
      })
      .catch(() => { if (current()) setLinked({ id, state: "error" }) })
  }, [url.id, loading, loadError, listed, workspaceId, linked?.id])
  const detail = listed ?? (linked?.id === url.id ? linked?.row ?? null : null)

  const openSecret = (id: string) => {
    go({ id, tab: null }, "push")
    closePanelSheet()
  }
  const closeSecret = () => {
    go({ id: null, tab: null }, "push")
  }

  const changeSpace = React.useCallback((s: VaultSpace) => {
    setSelectedIds(new Set())
    go({ space: s, id: null, tab: null, filters: EMPTY_CREDENTIAL_FILTERS, group: "none", providers: DEFAULT_PROVIDERS_STATE }, "push")
  }, [go])
  const openPool = (id: string | null, poolTab: ProvidersUrlState["poolTab"] = null) => {
    go({ providers: { ...pv, pool: id, poolTab }, id: null, tab: null }, "push")
    closePanelSheet()
  }

  // ── Keys 1 and 2 switch the spaces ──────────────────────────────────────
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) return
      if (e.key !== "1" && e.key !== "2") return
      if (isTypingTarget(e.target)) return
      // A dialog owns the keyboard while it is open.
      if (document.querySelector("[role=dialog], [role=alertdialog]")) return
      if (e.key === "2" && !canBind) return
      const next: VaultSpace = e.key === "1" ? "secrets" : "providers"
      if (next === space && !url.id && !pv.pool) return
      e.preventDefault()
      changeSpace(next)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [canBind, space, url.id, pv.pool, changeSpace])

  // ── The last space, per person ──────────────────────────────────────────
  // A bare /credentials opens the space the person was last in. Any address
  // that names a space, a filter or a page wins over the memory.
  const [lastSpace, setLastSpace, { ready: lastSpaceReady }] = useUserPreference<VaultSpace>("vault.lastSpace", "secrets")
  const restored = React.useRef(false)
  React.useEffect(() => {
    if (restored.current) return
    if (hasExplicitVaultState(searchParams)) { restored.current = true; return }
    if (lastSpace === "providers") {
      if (canBind) { restored.current = true; go({ space: "providers" }, "replace"); return }
      if (!abilitiesLoading) restored.current = true
      return
    }
    // "secrets" from the local cache may still be overruled by the server's copy.
    if (lastSpaceReady) restored.current = true
  }, [searchParams, lastSpace, lastSpaceReady, canBind, abilitiesLoading, go])
  React.useEffect(() => {
    if (!restored.current || providersDenied) return
    if (space !== lastSpace) setLastSpace(space)
    // Only a change of space is worth a write.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [space])

  /** Sign in again: the provider dialog at Connect, this seat's provider, mode and name locked. */
  const relogin = (cred: Credential) => {
    const login = cred.login
    const provider = (login?.provider ?? cred.provider).toUpperCase()
    openAdd({
      credentialId: cred.id,
      itemType: "PROVIDER_LOGIN",
      provider,
      loginMode: login?.mode ?? "subscription",
      signIn: provider === "OPENAI" && (login?.mode ?? "subscription") === "subscription" ? "device" : "paste",
      step: "values",
      name: cred.name,
    })
  }

  // ── Top bar ─────────────────────────────────────────────────────────────
  const viewTitle = space === "providers" ? "AI providers" : VIEW_LABEL[filters.status]
  const providersTitle = providerPage
    ? providerLabel(providerPage)
    : loginFilters.status !== "all" ? PROVIDER_VIEW_LABEL[loginFilters.status] ?? "AI providers" : "AI providers"
  const headerActions = (
    <>
      <SubBarIconButton icon={Terminal} aria-label="Command line" title="Command line" onClick={() => setCliOpen(true)} />
      {space === "providers" ? (
        <>
          {canManage && (
            <SubBarSecondary icon={Key} aria-label="Add secret" title="Add secret" onClick={() => openAdd()}>
              <span className="hidden sm:inline">Add secret</span>
            </SubBarSecondary>
          )}
          <SubBarPrimary icon={Plus} aria-label="Add AI provider" onClick={() => openAddProvider(providerPage ?? undefined)}>
            <span className="hidden sm:inline">Add AI provider</span>
          </SubBarPrimary>
        </>
      ) : canManage ? (
        <>
          {canBind && (
            <SubBarSecondary icon={Sparkles} aria-label="Add AI provider" title="Add AI provider" onClick={() => openAdd({ itemType: "PROVIDER_LOGIN" })}>
              <span className="hidden sm:inline">Add AI provider</span>
            </SubBarSecondary>
          )}
          <SubBarPrimary icon={Plus} aria-label="Add secret" onClick={() => openAdd()}>
            <span className="hidden sm:inline">Add secret</span>
          </SubBarPrimary>
        </>
      ) : null}
    </>
  )

  // v67: "Vault › Secrets ▾ › <view or secret>": the space is the chooser.
  const crumbRest = detail ? detail.name : space === "secrets" && filters.status !== "all" ? viewTitle : space === "providers" && (providerPage || loginFilters.status !== "all") ? providersTitle : undefined
  const subBar = (
    <SubBar
      icon={Key}
      title="Vault"
      section={
        <>
          <VaultSpaceCrumb space={space} onSpace={changeSpace} chooser={canBind} />
          {crumbRest != null && <span className="font-normal text-muted-foreground"> › {crumbRest}</span>}
        </>
      }
      ariaLabel="Vault"
      actions={headerActions}
      // The list and the secret page carry their own visible h1.
      heading={false}
    />
  )

  const spaces = (
    <VaultSpaces
      space={space}
      counts={{ secrets: secrets.length, providers: providerLogins.length }}
      showProviders={canBind}
      onSpace={changeSpace}
    />
  )

  const phoneSpaces = isMobile && canBind && !detail ? (
    <div role="radiogroup" aria-label="Vault space" className="mb-3 grid grid-cols-2 rounded-lg border border-border bg-muted/40 p-0.5">
      {(["secrets", "providers"] as const).map((s) => (
        <button key={s} type="button" role="radio" aria-checked={space === s} onClick={() => changeSpace(s)}
          className={cn("kit-tap h-8 rounded-md text-label", space === s ? "bg-card font-medium text-foreground shadow-xs" : "text-muted-foreground")}>
          {s === "secrets" ? "Secrets" : "AI providers"}
        </button>
      ))}
    </div>
  ) : null

  const bulkBar = canSelect && visibleSelection.length > 0 ? (
    <div role="toolbar" aria-label="Change the selected secrets" data-slot="vault-bulk"
      className="sticky bottom-3 z-20 mx-auto mt-3 flex w-fit max-w-full items-center gap-1.5 whitespace-nowrap rounded-xl border border-border bg-foreground px-3 py-1.5 text-label text-background shadow-xl">
      <span className="pr-1 font-medium tabular-nums">{visibleSelection.length} selected</span>
      <span className="h-4 w-px bg-background/30" aria-hidden />
      {canEdit && (
        <Button type="button" size="sm" variant="ghost" className="text-background hover:bg-background/15 hover:text-background" onClick={() => setBulkTierOpen(true)}>
          <ShieldCheck className="size-3.5" aria-hidden /> Change tier
        </Button>
      )}
      {canDelete && (
        <Button type="button" size="sm" variant="ghost" className="text-background hover:bg-background/15 hover:text-background" onClick={() => setBulkDeleteOpen(true)}>
          <Trash2 className="size-3.5" aria-hidden /> Delete
        </Button>
      )}
      <Button type="button" size="icon-sm" variant="ghost" aria-label="Clear the selection" className="text-background hover:bg-background/15 hover:text-background" onClick={() => setSelectedIds(new Set())}>
        <X className="size-3.5" />
      </Button>
    </div>
  ) : null

  // ── Content ─────────────────────────────────────────────────────────────
  let content: React.ReactNode
  if (loading) {
    content = <PageLoading variant="list" onRetry={loadData} />
  } else if (loadError) {
    content = (
      <PageError
        title="Couldn't load the vault"
        description={<>{loadError} Nothing was changed; your secrets are still stored.</>}
        onRetry={loadData}
      />
    )
  } else if (url.id && !detail) {
    content = linked?.state === "missing" ? (
      <PageNotFound what="secret" description="It was deleted, or the link points to another workspace." back={{ label: "Back to Secrets", onClick: closeSecret }} />
    ) : linked?.state === "error" ? (
      <PageError title="Couldn't open this secret" description="The vault list is fine; only this secret failed to load." onRetry={() => setLinked(null)} />
    ) : (
      <PageLoading variant="cards" />
    )
  } else if (detail && workspaceId) {
    content = (
      <CredentialDetailSheet
        key={detail.id}
        workspaceId={workspaceId}
        credential={detail}
        open
        tab={url.tab}
        onTabChange={(t: SecretTab) => go({ tab: t === "overview" ? null : t })}
        onOpenChange={(o) => { if (!o) closeSecret() }}
        onBack={closeSecret}
        backLabel={hasLogin(detail) ? "AI providers" : "Secrets"}
        onRefresh={handleRefresh}
        toolGaps={readiness.gapsByCredential.get(detail.id) ?? []}
        readinessKnown={readiness.crewsChecked > 0}
        crewsById={readiness.crewsById}
        knownTags={tagsInUse}
        onRotate={(c, mode) => setRotateCredential({ cred: c as unknown as Credential, mode: mode ?? "rotate" })}
        onRelogin={canBind ? (c) => relogin(c as Credential) : undefined}
      />
    )
  } else if (space === "providers" && pv.pool && workspaceId) {
    content = (
      <AccountGroupPage
        key={pv.pool}
        workspaceId={workspaceId}
        poolId={pv.pool}
        tab={pv.poolTab}
        onTabChange={(t) => go({ providers: { ...pv, poolTab: t === "accounts" ? null : t } })}
        accounts={providerLogins}
        canManage={canBind}
        onBack={() => openPool(null)}
        onOpenAccount={openSecret}
        onDeleted={() => openPool(null)}
      />
    )
  } else if (space === "providers") {
    const filtered = loginFilters.status !== "all" || activeLoginFacetCount(loginFilters) > 0 || loginFilters.search.trim() !== ""
    content = (
      <>
        {phoneSpaces}
        {providerPage ? (
          <ProviderHead
            provider={providerPage}
            rows={visibleLogins}
            workspaceDefault={providerDefault}
            onAdd={canBind ? () => openAddProvider(providerPage) : undefined}
          />
        ) : (
          <PageHead title={providersTitle} description="The model accounts your agents think and pay with." />
        )}
        {loginsError && providerLogins.length === 0 && (
          <div role="alert" className="page-banner page-banner-warn mb-4">
            Couldn&apos;t load the AI provider accounts ({loginsError}). Accounts marked on the vault list are shown.
          </div>
        )}
        {providerLogins.length === 0 ? (
          <PageEmpty
            icon={Sparkles}
            title="No AI provider accounts yet"
            description="Add the model accounts your agents think with: a subscription sign-in (Claude, ChatGPT, Gemini) or an API key."
            primary={canBind ? { label: "Add AI provider", onClick: () => openAddProvider() } : undefined}
            cli="crewship credential login --provider OPENAI"
          />
        ) : (
          <>
            {!providerPage && !filtered && (
              <ProviderHealth counts={providerHealth} onOpen={(view) => setLoginFilters({ ...EMPTY_LOGIN_FILTERS, status: view })} />
            )}
            {visibleLogins.length === 0 && filtered ? (
              <PageNoResults
                filters={describeLoginFilters(loginFilters, providersTitle)}
                onClear={() => setLoginFilters(EMPTY_LOGIN_FILTERS)}
              />
            ) : (
              <AccountsList
                rows={visibleLogins}
                layout={pv.layout}
                onLayoutChange={(l) => go({ providers: { ...pv, layout: l } })}
                group={pv.group}
                onGroupChange={(g) => go({ providers: { ...pv, group: g } })}
                onOpen={openSecret}
                onAdd={canBind ? () => openAddProvider(providerPage ?? undefined) : undefined}
                poolOf={poolOf}
                isPhone={isMobile}
                usage={accountUsage}
              />
            )}
            <p className="mt-4 text-label text-muted-foreground">
              Each account says whose it is and which agents it pays for. An agent uses the account bound to it, then its crew&apos;s, then the workspace default.
            </p>
          </>
        )}
      </>
    )
  } else if (secrets.length === 0) {
    content = (
      <>
        {phoneSpaces}
        {providersDenied && <ProvidersNote />}
        <PageEmpty
          icon={Key}
          title="The vault is empty"
          description="Store keys and logins your agents use, and the AI accounts they think with. Values are encrypted with AES-256-GCM."
          primary={canManage ? { label: "Add secret", onClick: () => openAdd() } : undefined}
          secondary={canBind ? { label: "Add AI provider", icon: Sparkles, onClick: () => openAdd({ itemType: "PROVIDER_LOGIN" }) } : undefined}
          cli="crewship credential create"
        />
      </>
    )
  } else {
    const filtered = filters.status !== "all" || activeFacetCount(filters) > 0 || filters.search.trim() !== ""
    content = (
      <>
        {phoneSpaces}
        {providersDenied && <ProvidersNote />}
        <PageHead
          title={viewTitle}
          description="Keys, tokens, logins, files and certificates your tools need. Agents use them through the crew proxy; people reveal them only with a reason."
        />
        {filters.status === "all" && activeFacetCount(filters) === 0 && !filters.search.trim() && (
          <VaultHealth className="mb-4" counts={health} onOpen={(view) => setFilters({ ...EMPTY_CREDENTIAL_FILTERS, status: view })} />
        )}
        {visible.length === 0 && filtered ? (
          <PageNoResults
            filters={describeFilters(filters, viewTitle)}
            onClear={() => setFilters(EMPTY_CREDENTIAL_FILTERS)}
          />
        ) : (
          <SecretsTable
            rows={visible}
            now={now}
            group={url.group}
            onGroupChange={(g) => go({ group: g })}
            selectable={canSelect}
            selectedIds={selectedIds}
            onToggle={(id) => setSelectedIds((prev) => { const n = new Set(prev); if (n.has(id)) n.delete(id); else n.add(id); return n })}
            onToggleAll={(ids, on) => setSelectedIds((prev) => { const n = new Set(prev); for (const id of ids) { if (on) n.add(id); else n.delete(id) } return n })}
            onOpen={openSecret}
          />
        )}
        {bulkBar}
      </>
    )
  }

  const isPage = Boolean(detail && workspaceId && !loading && !loadError)

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      {subBar}
      <div className="relative flex flex-1 overflow-hidden">
        {!loadError && (
          <SecondaryPanel id="vault" label="Vault">
            {space === "providers" ? (
              <ProvidersPanel
                filters={loginFilters}
                onFiltersChange={setLoginFilters}
                viewCounts={providerViewCounts}
                providers={loginProviders}
                modes={loginModes}
                owners={loginOwners}
                pools={poolItems}
                poolsError={Boolean(poolsApi.error)}
                selectedPool={pv.pool}
                onOpenPool={(id) => openPool(id)}
                onNewPool={canBind ? () => setNewPoolOpen(true) : undefined}
                spaces={spaces}
              />
            ) : (
              <VaultPanel
                filters={filters}
                onFiltersChange={setFilters}
                viewCounts={viewCounts}
                {...facets}
                crews={crewFacet}
                crewsById={readiness.crewsById}
                folders={folderApi.folders}
                onNewFolder={canEdit ? () => setNewFolderOpen(true) : undefined}
                onEditFolder={canEdit ? setEditFolder : undefined}
                spaces={spaces}
              />
            )}
          </SecondaryPanel>
        )}
        <div className={cn("min-w-0 flex-1", isPage ? "min-h-0 overflow-hidden" : "overflow-y-auto")}>
          <div className={cn(isPage ? "h-full" : "mx-auto w-full max-w-6xl p-4 md:p-6")}>{content}</div>
        </div>
      </div>

      <NewFolderDialog
        open={newFolderOpen}
        onOpenChange={setNewFolderOpen}
        folders={folderApi.folders}
        canSetPolicy={canBind}
        onCreate={folderApi.create}
      />

      <EditFolderDialog
        folder={editFolder}
        onOpenChange={(o) => { if (!o) setEditFolder(null) }}
        canSetPolicy={canBind}
        onSave={async (id, patch) => {
          const saved = await folderApi.update(id, patch)
          // Raising the floor can lift secrets already inside.
          if (patch.min_security_level !== undefined) handleRefresh()
          return saved
        }}
      />

      {workspaceId && (
        <AddSecretSheet
          workspaceId={workspaceId}
          open={addOpen}
          onOpenChange={(o) => { setAddOpen(o); if (!o) setAddInitial(undefined) }}
          onAddProvider={canBind ? () => openAdd({ itemType: "PROVIDER_LOGIN" }) : undefined}
          onSuccess={(id) => {
            handleRefresh()
            if (id) openSecret(id)
          }}
          knownTags={tagsInUse}
          initial={addInitial}
        />
      )}

      {workspaceId && rotateCredential && (
        <RotationDialog
          workspaceId={workspaceId}
          credentialId={rotateCredential.cred.id}
          credentialName={rotateCredential.cred.name}
          credentialType={rotateCredential.cred.type}
          mode={rotateCredential.mode}
          open
          onOpenChange={(o) => { if (!o) setRotateCredential(null) }}
          onRotated={handleRefresh}
        />
      )}

      {workspaceId && (
        <BulkDeleteDialog
          open={bulkDeleteOpen}
          onOpenChange={setBulkDeleteOpen}
          workspaceId={workspaceId}
          targets={visibleSelection}
          onDone={(failed) => { setSelectedIds(new Set(failed)); handleRefresh() }}
        />
      )}
      {workspaceId && (
        <BulkTierDialog
          open={bulkTierOpen}
          onOpenChange={setBulkTierOpen}
          workspaceId={workspaceId}
          targets={visibleSelection}
          onDone={(failed) => { setSelectedIds(new Set(failed)); handleRefresh() }}
        />
      )}
      <VaultCliDialog open={cliOpen} onOpenChange={setCliOpen} />
      {canBind && workspaceId && (
        <AddProviderDialog
          workspaceId={workspaceId}
          open={providerDialog !== null}
          onOpenChange={(o) => { if (!o) setProviderDialog(null) }}
          initial={providerDialog?.initial}
          knownTags={tagsInUse}
          onSuccess={(id) => {
            handleRefresh()
            if (id && !providerDialog?.initial?.credentialId) openSecret(id)
          }}
        />
      )}
      {canBind && workspaceId && space === "providers" && (
        <NewAccountGroupDialog
          workspaceId={workspaceId}
          open={newPoolOpen}
          onOpenChange={setNewPoolOpen}
          accounts={providerLogins}
          onCreated={(id) => openPool(id)}
        />
      )}
    </div>
  )
}

/** The page heading — the page's one visible h1 (the top bar's title is not a heading here). */
function PageHead({ title, description }: { title: string; description: string }) {
  return (
    <header className="mb-4">
      <h1 className="text-title font-semibold tracking-tight text-foreground">{title}</h1>
      <p className="mt-1 max-w-[80ch] text-body text-muted-foreground">{description}</p>
    </header>
  )
}

function ProvidersNote() {
  return (
    <p role="note" className="page-banner mb-4 bg-muted">
      AI provider accounts are managed by owners and admins.
    </p>
  )
}

function describeLoginFilters(f: LoginFilters, viewTitle: string): string {
  const parts: string[] = []
  if (f.status !== "all") parts.push(viewTitle)
  const n = activeLoginFacetCount(f)
  if (n > 0) parts.push(`${n} filter${n === 1 ? "" : "s"}`)
  if (f.search.trim()) parts.push(`“${f.search.trim()}”`)
  return parts.join(" · ")
}

function describeFilters(f: CredentialFilters, viewTitle: string): string {
  const parts: string[] = []
  if (f.status !== "all") parts.push(viewTitle)
  const n = activeFacetCount(f)
  if (n > 0) parts.push(`${n} filter${n === 1 ? "" : "s"}`)
  if (f.search.trim()) parts.push(`“${f.search.trim()}”`)
  return parts.join(" · ")
}
