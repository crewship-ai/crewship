"use client"

import * as React from "react"
import { useSearchParams } from "next/navigation"
import { toast } from "sonner"
import { AlertTriangle, Key, Link2, Plus, RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { EmptyState } from "@/components/layout/empty-state"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Card } from "@/components/ui/card"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { AddSecretSheet } from "@/components/features/credentials/add-secret-sheet"
import { ConnectOAuthDialog } from "@/components/features/credentials/connect-oauth-dialog"
import { CredentialDetailSheet } from "@/components/features/credentials/credential-detail-sheet"
import { CredentialsSidebar } from "@/components/features/credentials/credentials-sidebar"
import {
  CredentialsOverview,
  CredentialsOverviewSkeleton,
} from "@/components/features/credentials/credentials-overview"
import { RotationDialog } from "@/components/features/credentials/rotation-dialog"
import { EditCredentialDialog, type CredentialData } from "@/components/features/credentials/edit-credential-dialog"
import { Capability } from "@/lib/capabilities"
import { useAbilities } from "@/hooks/use-abilities"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { useCredentialReadiness } from "@/hooks/use-credential-readiness"
import {
  EMPTY_CREDENTIAL_FILTERS,
  applyCredentialFilters,
  buildAgentFacet,
  buildBrandFacet,
  buildShapeFacet,
  buildScopeFacet,
  buildTagFacet,
  deriveCredentialStatus,
  needsAttention,
  type CredentialFilters,
} from "@/lib/credentials/facets"
import { buildTierFacet, tierOf } from "@/lib/credentials/tiers"
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
  /** Server-declared probe support — see credentials.go `testable`. */
  testable?: boolean
  /** Keeper tier, 1–4 — see lib/credentials/tiers.ts. Optional so an older
   *  server response renders as unclassified rather than crashing the page. */
  security_level?: number
  security_level_label?: string
}

type SortKey = "last_used" | "name" | "created"

export default function CredentialsPage() {
  const { abilities, hasCapability } = useAbilities()
  // #1033: read the selected workspace from the shared store (driven by the
  // top-bar workspace switcher) instead of hardcoding the first workspace, so
  // users in multiple workspaces can manage each one's credentials.
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [credentials, setCredentials] = React.useState<Credential[]>([])
  const [loading, setLoading] = React.useState(true)
  const [loadError, setLoadError] = React.useState<string | null>(null)
  const [addOpen, setAddOpen] = React.useState(false)
  const [oauthOpen, setOauthOpen] = React.useState(false)
  const [editOpen, setEditOpen] = React.useState(false)
  const [editCredential, setEditCredential] = React.useState<CredentialData | null>(null)
  // Layered like the backend (requireRoleOrCapabilityOrForbid): MANAGER+
  // via role, or any member holding an explicit credential.create grant
  // (#1034). Gates both the raw Add-secret sheet and the OAuth connect
  // flow — the OAuth routes are aligned to the same tier server-side.
  const canManage = abilities.can("create", "Credential") || hasCapability(Capability.CredentialCreate)
  // Bulk delete mirrors the backend: DELETE is OWNER/ADMIN only ("manage" →
  // CASL "delete"). Hiding the checkbox beats letting a MEMBER tick rows and
  // discover the 403 at the confirmation dialog.
  const canDelete = abilities.can("delete", "Credential")
  const [filters, setFilters] = React.useState<CredentialFilters>(EMPTY_CREDENTIAL_FILTERS)
  const [sidebarCollapsed, setSidebarCollapsed] = React.useState(false)
  // On a phone the rail is 280px of a 390px screen — it does not sit BESIDE the
  // content, it replaces it, and the detail beside it renders one word per line.
  // Collapse it when the viewport narrows and let it open as an overlay instead
  // of a column, so the page keeps the full width it was designed for. Same
  // treatment /routines gives its explorer.
  const isMobile = useIsMobile()
  React.useEffect(() => {
    if (isMobile) setSidebarCollapsed(true)
  }, [isMobile])
  const [sortKey, setSortKey] = React.useState<SortKey>("last_used")
  const [detailCredential, setDetailCredential] = React.useState<Credential | null>(null)
  const [detailOpen, setDetailOpen] = React.useState(false)
  // Selection is a mode you enter, not the resting state of the list — see the
  // rail's `selectMode` prop. Leaving it drops the selection with it, so a
  // forgotten tick cannot be deleted by a later click on the floating bar.
  const [selectMode, setSelectMode] = React.useState(false)
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(new Set())
  const [bulkDeleteOpen, setBulkDeleteOpen] = React.useState(false)
  const [bulkDeleting, setBulkDeleting] = React.useState(false)

  // /credentials?id=<id> — the ⌘K palette's Credentials rows, and any
  // bookmark. Landing on the list and leaving the caller to find the secret
  // they had just searched for is the same second-search-by-eye the members
  // roster had.
  //
  // Keyed on the LAST id acted on, not a once-ever latch. A latch survives the
  // gap before the list arrives (on the first render `credentials` is empty)
  // but then swallows every later link: standing on /credentials?id=A, opening
  // ⌘K and picking secret B changed the URL and nothing else, because this
  // page never unmounts. Per id, each new one opens exactly once — a refresh
  // of the vault is not a navigation, so it cannot reopen a sheet the user has
  // closed, and an id no credential has is marked handled rather than retried.
  const linkedCredentialId = useSearchParams().get("id")
  const appliedCredentialId = React.useRef<string | null>(null)
  React.useEffect(() => {
    if (!linkedCredentialId || credentials.length === 0) return
    if (appliedCredentialId.current === linkedCredentialId) return
    appliedCredentialId.current = linkedCredentialId
    const hit = credentials.find((c) => c.id === linkedCredentialId)
    if (!hit) return
    setDetailCredential(hit)
    setDetailOpen(true)
  }, [linkedCredentialId, credentials])
  const [rotateCredential, setRotateCredential] = React.useState<Credential | null>(null)
  const [rotateOpen, setRotateOpen] = React.useState(false)

  // Tool readiness (§2.3, blocker #3): "the secret is valid" and "the CLI that
  // reads it exists in the container" are different questions, and only the
  // second one predicts whether the agent's next command works.
  const readiness = useCredentialReadiness(workspaceId)

  // Cancel an in-flight fetch when the workspace changes so a slower
  // response from the previous workspace can't resolve later and overwrite
  // the newly-selected workspace's rows (matches the AbortController idiom
  // used by app/(dashboard)/crews/page.tsx).
  const abortRef = React.useRef<AbortController | null>(null)

  // fetchCredentials THROWS on failure so loadData can surface a real
  // error state — a failed fetch must never render as "no credentials
  // yet" (which invites re-creating secrets that already exist).
  const fetchCredentials = React.useCallback(async (oid: string, signal?: AbortSignal) => {
    let res: Response
    try {
      res = await apiFetch(`/api/v1/credentials?workspace_id=${oid}`, { signal })
    } catch (err) {
      if ((err as { name?: string })?.name === "AbortError") throw err
      throw new Error("Network error while loading credentials.")
    }
    // Belt-and-suspenders: even if the abort didn't preempt the fetch (or
    // the mock/transport ignores signals), never apply a response that's no
    // longer wanted.
    if (signal?.aborted) return
    if (!res.ok) throw new Error(`Loading credentials failed (HTTP ${res.status}).`)
    const data = await res.json()
    if (signal?.aborted) return
    const normalised: Credential[] = (Array.isArray(data) ? data : []).map((c: Credential) => ({
      ...c,
      last_used_at: c.last_used_at ?? null,
      last_used_ips: Array.isArray(c.last_used_ips) ? c.last_used_ips : [],
      tags: Array.isArray(c.tags) ? c.tags : [],
      crew_ids: Array.isArray(c.crew_ids) ? c.crew_ids : [],
    }))
    setCredentials(normalised)
    // #1085: any successful load clears a stale error — otherwise a full-page
    // error card from an earlier failure survives a later good refresh.
    setLoadError(null)
  }, [])

  const loadData = React.useCallback(async () => {
    // Wait for the workspace store to resolve the selected workspace before
    // deciding there's nothing to load.
    if (wsLoading) return
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setLoadError(null)
    try {
      if (workspaceId) {
        await fetchCredentials(workspaceId, controller.signal)
      } else {
        setCredentials([])
      }
    } catch (err) {
      if (controller.signal.aborted || (err as { name?: string })?.name === "AbortError") return
      setLoadError(err instanceof Error ? err.message : "Something went wrong while loading credentials.")
    } finally {
      // Only the currently-owned controller may flip loading back off —
      // otherwise a superseded request's `finally` could clear the
      // skeleton after the newer request already took over.
      if (abortRef.current === controller) setLoading(false)
    }
  }, [wsLoading, workspaceId, fetchCredentials])

  // Reload whenever the selected workspace changes (switcher) or the store
  // finishes loading.
  React.useEffect(() => {
    loadData()
    return () => { abortRef.current?.abort() }
  }, [loadData])

  // Bulk-select and facets are per-workspace state: without this, switching
  // workspaces leaves stale ids selected, floating the "N selected" bar over
  // the new workspace's rows and letting bulk-delete fire DELETEs against the
  // previous workspace's credential ids. The facets reset for the same
  // reason — a crew id from the old workspace filters the new one to nothing.
  React.useEffect(() => {
    setSelectMode(false)
    setSelectedIds(new Set())
    setFilters(EMPTY_CREDENTIAL_FILTERS)
  }, [workspaceId])

  const handleRefresh = React.useCallback(() => {
    if (!workspaceId) return
    // #1085: this refresh runs while data is already on screen (after a
    // mutation). A transient failure should surface as a toast, not replace
    // the loaded list with the full-page error card — the stale-but-visible
    // list is more useful than an error screen, and the next refresh recovers.
    fetchCredentials(workspaceId).catch((err) => {
      toast.error(err instanceof Error ? err.message : "Couldn't refresh credentials.")
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
        if (r.status === "rejected") return true
        // #1085: a 404 means the credential is already gone (another admin
        // deleted it first). Treat it as success, not a failure — otherwise it
        // lingers in `selectedIds` as a phantom selection after the refresh
        // removes its row, and the floating bar shows "1 selected" forever.
        if (r.value.status === 404) return false
        return !r.value.ok
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

  // The rail's "Needs attention" count. The overview's own tiles derive their
  // numbers from lib/credentials/overview.ts over the same predicate, so the
  // rail and the dashboard cannot report a different total.
  const attentionList = React.useMemo(() => credentials.filter(needsAttention), [credentials])

  // Distinct tags from data — drives the sidebar's Tag facet so we never
  // show tags the workspace doesn't have.
  const tagsInUse = React.useMemo(() => {
    const set = new Set<string>()
    for (const c of credentials) {
      for (const t of c.tags ?? []) set.add(t)
    }
    return Array.from(set).sort()
  }, [credentials])

  const missingToolCount = React.useMemo(
    () => credentials.filter((c) => readiness.missingToolIds.has(c.id)).length,
    [credentials, readiness.missingToolIds],
  )

  const brands = React.useMemo(() => buildBrandFacet(credentials), [credentials])
  const shapes = React.useMemo(() => buildShapeFacet(credentials), [credentials])
  const scopes = React.useMemo(
    () => buildScopeFacet(credentials, readiness.crewNames),
    [credentials, readiness.crewNames],
  )
  // Counted over the whole vault, not the filtered view: a tier row that
  // recounted itself after every click would report 0 for every tier but the
  // selected one, which is the opposite of what the section is for.
  const tiers = React.useMemo(() => buildTierFacet(credentials), [credentials])
  const tagFacet = React.useMemo(() => buildTagFacet(credentials), [credentials])
  const agentsInUse = React.useMemo(() => buildAgentFacet(credentials), [credentials])

  const filtered = React.useMemo(
    () => applyCredentialFilters(credentials, filters, readiness.missingToolIds),
    [credentials, filters, readiness.missingToolIds],
  )

  const sorted = React.useMemo(() => {
    const out = [...filtered]
    out.sort((a, b) => {
      // Errors always rank to the top so users see breakage on every
      // sort, regardless of which key they picked.
      const aErr = deriveCredentialStatus(a) === "Error" ? 0 : 1
      const bErr = deriveCredentialStatus(b) === "Error" ? 0 : 1
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
    <>
      <SubBarSecondary icon={Link2} onClick={() => setOauthOpen(true)}>
        Connect via OAuth
      </SubBarSecondary>
      <SubBarPrimary icon={Plus} onClick={() => setAddOpen(true)}>
        Add secret
      </SubBarPrimary>
    </>
  ) : null

  // Canonical page chrome: the SubBar (identity + actions) directly under the
  // global top bar, then the explorer rail + a scrollable, padded content
  // region — the same shape Integrations uses.
  const subBar = (
    <SubBar
      icon={Key}
      title="Credentials"
      ariaLabel="Credentials"
      description={
        credentials.length === 0
          ? "Shared secrets, API keys, and CLI tokens for your agents"
          : `${credentials.length} secret${credentials.length === 1 ? "" : "s"}` +
            (missingToolCount > 0 ? ` · ${missingToolCount} waiting on a tool` : "")
      }
      actions={headerActions}
    />
  )

  if (loading) {
    return (
      <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
        {subBar}
        <div className="flex-1 overflow-y-auto">
          {/* The skeleton is the OVERVIEW's geometry, not three grey bars where
              a table used to be — a placeholder that does not match what
              replaces it makes the page reflow on load, which reads as a
              second, unexplained render. */}
          <div className="p-4 md:p-6">
            <CredentialsOverviewSkeleton />
          </div>
        </div>
      </div>
    )
  }

  // The rail is a filter surface. With nothing to filter (empty vault) or
  // nothing loaded (error), it would be a column of zeroes next to a message
  // asking the user to do something else.
  const showSidebar = !loadError && credentials.length > 0

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {subBar}
      <div className="relative flex flex-1 overflow-hidden">
        {/* Tapping away closes the overlay. Without it the only way back to the
            content on a phone is the collapse button, which the rail is
            covering. */}
        {showSidebar && isMobile && !sidebarCollapsed && (
          <button
            type="button"
            aria-label="Close credential list"
            onClick={() => setSidebarCollapsed(true)}
            className="absolute inset-0 z-20 bg-black/50"
          />
        )}
        {showSidebar && (
          <aside
            className={cn(
              "shrink-0 border-r border-white/[0.06] bg-card transition-all",
              sidebarCollapsed ? "w-9 overflow-hidden" : "w-[280px]",
              // The collapsed rail stays in flow at both sizes, so the expand
              // button never moves.
              isMobile && !sidebarCollapsed && "absolute inset-y-0 left-0 z-30 shadow-2xl",
            )}
          >
            {sidebarCollapsed ? (
              <div className="flex h-full flex-col items-center pt-1.5">
                <SidebarCollapseButton collapsed onToggle={() => setSidebarCollapsed(false)} />
              </div>
            ) : (
              <CredentialsSidebar
                filters={filters}
                onFiltersChange={setFilters}
                counts={{
                  all: credentials.length,
                  attention: attentionList.length,
                  missingTool: missingToolCount,
                }}
                brands={brands}
                shapes={shapes}
                scopes={scopes}
                tiers={tiers}
                agents={agentsInUse}
                tags={tagFacet}
                crewsById={readiness.crewsById}
                sort={sortKey}
                onSortChange={setSortKey}
                selectMode={selectMode}
                onSelectModeChange={
                  canDelete
                    ? (next) => {
                        setSelectMode(next)
                        if (!next) setSelectedIds(new Set())
                      }
                    : undefined
                }
                selectedIds={selectedIds}
                onToggleSelected={canDelete ? toggleSelected : undefined}
                onToggleCollapse={() => setSidebarCollapsed(true)}
                // The rail IS the list now — the table under the dashboard was
                // the same names a second time. Which is why the sort control
                // moved here with it: the order belongs to the list, and the
                // list is here.
                credentials={sorted.map((c) => ({
                  id: c.id,
                  name: c.name,
                  provider: c.provider,
                  type: c.type,
                  tier: tierOf(c),
                  tierLabel: c.security_level_label,
                }))}
                selectedCredentialId={detailOpen ? detailCredential?.id ?? null : null}
                onSelectCredential={(id) => {
                  const cred = sorted.find((c) => c.id === id)
                  if (!cred) return
                  setDetailCredential(cred)
                  setDetailOpen(true)
                  // Picking a credential on a phone means "show me that", and
                  // the overlay covering it would be the opposite.
                  if (isMobile) setSidebarCollapsed(true)
                }}
              />
            )}
          </aside>
        )}

        {/* The detail owns its own scroll and padding — it is a full page in
            the /issues shape, not a panel dropped into a padded box. Everything
            else keeps the page's padding. */}
        <div className={cn("flex-1", detailOpen && detailCredential && workspaceId ? "min-h-0 overflow-hidden" : "overflow-y-auto")}>
          <div className={cn(!(detailOpen && detailCredential && workspaceId) && "p-4 md:p-6", "h-full")}>
      {/* Master-detail, the /integrations shape: selecting a credential
          REPLACES the list rather than covering it. A modal would keep the
          table behind a scrim and make the reader dismiss one secret before
          looking at the next — the wrong rhythm for a page whose job is moving
          between them. Add-a-credential stays a dialog on purpose: a create is
          a task you finish or abandon, an inspect is somewhere you navigate.
          Written as the first branch of the existing chain so the detail needs
          no wrapper of its own. */}
      {detailOpen && detailCredential && workspaceId ? (
        <CredentialDetailSheet
          workspaceId={workspaceId}
          credential={detailCredential}
          open
          onOpenChange={(o) => { setDetailOpen(o); if (!o) setDetailCredential(null) }}
          onBack={() => { setDetailOpen(false); setDetailCredential(null) }}
          onRefresh={handleRefresh}
          toolGaps={readiness.gapsByCredential.get(detailCredential.id) ?? []}
          readinessKnown={readiness.crewsChecked > 0}
          crewsById={readiness.crewsById}
          onEdit={(c) => handleEdit(c as Credential)}
          onRotate={(c) => {
            setRotateCredential(c as unknown as Credential)
            setRotateOpen(true)
          }}
        />
      ) : loadError ? (
        // Load failure — visually and semantically distinct from the
        // empty state: red accent, explicit error copy, and a Retry
        // affordance. Never claims "no credentials yet".
        <Card className="p-12 text-center border-destructive/30 bg-destructive/[0.03]" role="alert">
          <AlertTriangle className="mx-auto h-6 w-6 text-destructive" />
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
            <div className="mt-4 flex items-center justify-center gap-2">
              <Button onClick={() => setAddOpen(true)}>
                <Plus className="mr-2 h-4 w-4" />
                Add first secret
              </Button>
              <Button variant="outline" onClick={() => setOauthOpen(true)}>
                <Link2 className="mr-2 h-4 w-4" />
                Connect via OAuth
              </Button>
            </div>
          )}
        </EmptyState>
      ) : (
        // The landing pane is the vault's dashboard, and the table is the last
        // card on it rather than the first thing you meet. See
        // credentials-overview.tsx for why — in short, the rail to the left was
        // already this list, and the copy in the main pane was the one you could
        // not search.
        // The landing pane is the vault's dashboard. The table that used to
        // sit under it is gone: the rail on the left is the same list, iconed,
        // searchable and filtered, and the copy in the main pane was the one
        // that could not be searched. Everything the columns carried has a home
        // — readiness and last use on the credential's own Overview, tags in
        // its header, agents on its Used by tab, and the whole vault's shape in
        // the cards above.
        <CredentialsOverview
          credentials={credentials}
          missingToolIds={readiness.missingToolIds}
          crewsChecked={readiness.crewsChecked}
          readinessLoading={readiness.loading}
          onSelect={(id) => {
            const cred = credentials.find((c) => c.id === id)
            if (!cred) return
            setDetailCredential(cred)
            setDetailOpen(true)
          }}
          onSelectTier={(tier) => setFilters((f) => ({ ...f, tier }))}
          onSelectStatus={(status) => setFilters((f) => ({ ...f, status }))}
        />
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
        <ConnectOAuthDialog
          workspaceId={workspaceId}
          open={oauthOpen}
          onOpenChange={setOauthOpen}
          onSuccess={handleRefresh}
        />
      )}

      {workspaceId && (
        <></>
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

      {canDelete && selectMode && selectedIds.size > 0 && (
        <div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-50 rounded-full border border-border bg-popover/95 backdrop-blur shadow-2xl px-4 py-2 flex items-center gap-3 text-xs">
          <span className="font-medium">{selectedIds.size} selected</span>
          <button type="button" onClick={() => setBulkDeleteOpen(true)} className="text-destructive hover:text-destructive/80">
            Delete
          </button>
          <button
            type="button"
            onClick={() => {
              setSelectedIds(new Set())
              setSelectMode(false)
            }}
            className="text-muted-foreground hover:text-foreground"
          >
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
    </div>
  )
}
