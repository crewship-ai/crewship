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
 * A load failure is never drawn as an empty vault (#1085): an empty vault
 * invites re-creating secrets that already exist.
 */

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Key, Layers, Plus, ShieldCheck, Sparkles, TerminalSquare, Trash2, X } from "lucide-react"

import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { SubBar, SubBarIconButton, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { PageEmpty, PageError, PageLoading, PageNoResults, PageNotFound } from "@/components/layout/page-state"
import { SecondaryPanel, useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { AddSecretSheet } from "@/components/features/credentials/add-secret-sheet"
import { CredentialDetailSheet } from "@/components/features/credentials/credential-detail-sheet"
import { RotationDialog } from "@/components/features/credentials/rotation-dialog"
import { ProviderLoginsPanel } from "@/components/features/credentials/provider-logins-panel"
import { ProviderPoolsDialog } from "@/components/features/credentials/provider-pools-dialog"
import { ProviderLoginsSidebar } from "@/components/features/credentials/provider-logins-sidebar"
import { SecretsTable, type VaultSecret } from "@/components/features/credentials/secrets-table"
import { VaultHealth, buildVaultHealth } from "@/components/features/credentials/vault-health"
import { VIEW_LABEL, VaultPanel, VaultSpaces, buildClassFacet } from "@/components/features/credentials/vault-panel"
import { BulkDeleteDialog, BulkTierDialog } from "@/components/features/credentials/bulk-actions"
import { VaultCliDialog } from "@/components/features/credentials/vault-cli-dialog"
import type { WizardInitial } from "@/components/features/credentials/add-credential-wizard"
import {
  EMPTY_LOGIN_FILTERS,
  applyLoginFilters,
  buildLoginModeFacet,
  buildLoginOwnerFacet,
  buildLoginProviderFacet,
  buildLoginStatusCounts,
  hasLogin,
  sortLogins,
  type LoginFilters,
  type ProviderLogin,
} from "@/lib/credentials/provider-logins"
import { Capability } from "@/lib/capabilities"
import { useAbilities } from "@/hooks/use-abilities"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
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
  activeFacetCount,
  buildVaultQuery,
  parseVaultUrl,
  resolveCrewTokens,
  type SecretTab,
  type VaultSpace,
  type VaultUrlState,
} from "@/lib/credentials/vault-url-state"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

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

const normalise = (c: Credential): Credential => ({
  ...c,
  last_used_at: c.last_used_at ?? null,
  last_used_ips: Array.isArray(c.last_used_ips) ? c.last_used_ips : [],
  tags: Array.isArray(c.tags) ? c.tags : [],
  crew_ids: Array.isArray(c.crew_ids) ? c.crew_ids : [],
  agent_names: Array.isArray(c.agent_names) ? c.agent_names : [],
})

export default function CredentialsPage() {
  const { abilities, hasCapability } = useAbilities()
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
  const [loginFilters, setLoginFilters] = React.useState<LoginFilters>(EMPTY_LOGIN_FILTERS)
  const [loading, setLoading] = React.useState(true)
  const [loadError, setLoadError] = React.useState<string | null>(null)
  const [addOpen, setAddOpen] = React.useState(false)
  const [poolsOpen, setPoolsOpen] = React.useState(false)
  const [cliOpen, setCliOpen] = React.useState(false)
  const [addInitial, setAddInitial] = React.useState<WizardInitial | undefined>(undefined)
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(new Set())
  const [bulkDeleteOpen, setBulkDeleteOpen] = React.useState(false)
  const [bulkTierOpen, setBulkTierOpen] = React.useState(false)
  const [rotateCredential, setRotateCredential] = React.useState<{ cred: Credential; mode: "rotate" | "patch" } | null>(null)
  const [assignOnOpen, setAssignOnOpen] = React.useState(false)
  // A linked secret that is not in the list (beyond the page, or deleted).
  const [linked, setLinked] = React.useState<{ id: string; state: "loading" | "missing" | "error"; row?: Credential } | null>(null)

  const readiness = useCredentialReadiness(workspaceId)
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
    setLoginFilters(EMPTY_LOGIN_FILTERS)
    setAddOpen(false)
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
    if (initial?.itemType === "PROVIDER_LOGIN" && !canBind) return
    setAddInitial(initial)
    setAddOpen(true)
  }, [canBind])

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
    const out = applyCredentialFilters(secrets, filters, missing, { agentCrew })
    // Broken first, then most recently used — "what is wrong" before "what is busy".
    const rank = { rejected: 0, pending: 1, expiring: 2, ready: 3, unused: 4 } as const
    return out.sort((a, b) => {
      const d = rank[deriveSecretState(a).key] - rank[deriveSecretState(b).key]
      if (d !== 0) return d
      const at = a.last_used_at ? Date.parse(a.last_used_at) : 0
      const bt = b.last_used_at ? Date.parse(b.last_used_at) : 0
      return bt - at || a.name.localeCompare(b.name)
    })
  }, [secrets, filters, missing, agentCrew])

  // Only what is on screen can be acted on — a tick made under another filter
  // must not delete a secret the person can no longer see.
  const visibleSelection = React.useMemo(() => visible.filter((c) => selectedIds.has(c.id)), [visible, selectedIds])
  const canSelect = canDelete || canEdit

  // ── AI providers ────────────────────────────────────────────────────────
  const loginCounts = React.useMemo(() => buildLoginStatusCounts(providerLogins), [providerLogins])
  const loginProviders = React.useMemo(() => buildLoginProviderFacet(providerLogins), [providerLogins])
  const loginModes = React.useMemo(() => buildLoginModeFacet(providerLogins), [providerLogins])
  const loginOwners = React.useMemo(() => buildLoginOwnerFacet(providerLogins), [providerLogins])
  const visibleLogins = React.useMemo(() => sortLogins(applyLoginFilters(providerLogins, loginFilters)), [providerLogins, loginFilters])

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
    setAssignOnOpen(false)
    go({ id: null, tab: null }, "push")
  }

  const changeSpace = (s: VaultSpace) => {
    setSelectedIds(new Set())
    go({ space: s, id: null, tab: null, filters: EMPTY_CREDENTIAL_FILTERS, group: "none" }, "push")
  }

  /** Re-login: the wizard on the sign-in step, this seat's provider and mode chosen. */
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
  const headerActions = (
    <>
      <SubBarIconButton icon={TerminalSquare} aria-label="Command line" title="Command line" onClick={() => setCliOpen(true)} />
      {space === "providers" ? (
        <>
          <SubBarSecondary icon={Layers} aria-label="Account groups" title="Account groups" onClick={() => setPoolsOpen(true)}>
            <span className="hidden sm:inline">Account groups</span>
          </SubBarSecondary>
          {canManage && (
            <SubBarSecondary icon={Key} aria-label="Add secret" title="Add secret" onClick={() => openAdd()}>
              <span className="hidden sm:inline">Add secret</span>
            </SubBarSecondary>
          )}
          <SubBarPrimary icon={Plus} aria-label="Add AI provider" onClick={() => openAdd({ itemType: "PROVIDER_LOGIN" })}>
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

  const subBar = (
    <SubBar
      icon={Key}
      title={space === "providers" ? "AI providers" : "Secrets"}
      section={detail ? detail.name : space === "secrets" && filters.status !== "all" ? viewTitle : undefined}
      ariaLabel="Vault"
      description={space === "providers" ? `${providerLogins.length} accounts` : `${secrets.length} secrets`}
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
        assignOnOpen={assignOnOpen}
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
  } else if (space === "providers") {
    content = (
      <>
        {phoneSpaces}
        <PageHead title="AI providers" description="The model accounts your agents think and pay with." />
        {loginsError && providerLogins.length === 0 && (
          <div role="alert" className="page-banner page-banner-warn mb-4">
            Couldn&apos;t load the AI provider accounts ({loginsError}). Accounts marked on the vault list are shown.
          </div>
        )}
        <ProviderLoginsPanel
          logins={providerLogins}
          visible={visibleLogins}
          onSelect={openSecret}
          onSelectStatus={(status) => setLoginFilters((f) => ({ ...f, status }))}
          onAdd={() => openAdd({ itemType: "PROVIDER_LOGIN" })}
          onAssign={(id) => { setAssignOnOpen(true); openSecret(id) }}
        />
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
              <ProviderLoginsSidebar
                filters={loginFilters}
                onFiltersChange={setLoginFilters}
                counts={loginCounts}
                providers={loginProviders}
                modes={loginModes}
                owners={loginOwners}
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
                spaces={spaces}
              />
            )}
          </SecondaryPanel>
        )}
        <div className={cn("min-w-0 flex-1", isPage ? "min-h-0 overflow-hidden" : "overflow-y-auto")}>
          <div className={cn(isPage ? "h-full" : "mx-auto w-full max-w-6xl p-4 md:p-6")}>{content}</div>
        </div>
      </div>

      {workspaceId && (
        <AddSecretSheet
          workspaceId={workspaceId}
          open={addOpen}
          onOpenChange={(o) => { setAddOpen(o); if (!o) setAddInitial(undefined) }}
          onAddProvider={canBind ? () => { setAddInitial({ itemType: "PROVIDER_LOGIN" }) } : undefined}
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
      {canBind && workspaceId && poolsOpen && <ProviderPoolsDialog key={workspaceId} workspaceId={workspaceId} onClose={() => setPoolsOpen(false)} />}
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

function describeFilters(f: CredentialFilters, viewTitle: string): string {
  const parts: string[] = []
  if (f.status !== "all") parts.push(viewTitle)
  const n = activeFacetCount(f)
  if (n > 0) parts.push(`${n} filter${n === 1 ? "" : "s"}`)
  if (f.search.trim()) parts.push(`“${f.search.trim()}”`)
  return parts.join(" · ")
}
