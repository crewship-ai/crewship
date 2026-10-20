"use client"

import { useEffect, useMemo, useState, useCallback, useRef } from "react"
import { useRouter } from "next/navigation"
import { ArrowLeft, ChevronRight, ExternalLink, Shield } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { useIsMobile } from "@/hooks/use-mobile"
import { apiFetch } from "@/lib/api-fetch"
import { withWs } from "@/lib/admin-workspace-query"
import { SubBar, SubBarSecondary } from "@/components/layout/sub-bar"
import {
  SidebarToolbar, SidebarSearch, SidebarSection, SidebarRow,
} from "@/components/layout/sidebar-kit"
import { SecondaryPanel, useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { PageError, PageNoAccess } from "@/components/layout/page-state"
import { SettingsPageHead } from "@/components/features/settings/shared"

import {
  initialAdminTab, movedAdminTabHref, adminSectionLabel, adminSectionItem, filterNav,
} from "./navigation"
import type { TabKey, Stats, KeeperStatus } from "./types"
import { useAdminOverview } from "./hooks/use-admin-overview"
import { OverviewTab } from "./tabs/overview-tab"
import { RuntimeTab } from "./tabs/runtime-tab"
import type { RuntimeEntry } from "./tabs/runtime-tab"
import { AdminNavTile } from "./tabs/admin-kit"
import { RetentionPage } from "@/components/features/admin/backups/data-retention"
import { NotificationsTab } from "./tabs/notifications-tab"
import { RateLimitsTab } from "./tabs/rate-limits-tab"
import { PageSaveBar, PageSaveProvider, usePageSaveGuard } from "@/components/ui/page-save-bar"
import { cn } from "@/lib/utils"

/**
 * Admin sidebar sections — ONLY real, wired tabs.
 *
 * The previous revision listed 12 extra placeholder sections ("System Logs",
 * "Networking", "Backups", "LLM Gateway", "Auth & SSO", "Feature Flags",
 * "Rate Limits", "Resources") that all rendered a "Coming Soon" card.
 * Those were removed on the user's explicit instruction that the UI must
 * only surface what actually works. Reintroduce them one at a time when
 * each has a real backend to talk to.
 */

/** Overview is the wide dashboard; every other section reads in the 768 px
 *  Settings column under its section head (Harbor 2 template B). */
const WIDE_TABS: ReadonlySet<TabKey> = new Set<TabKey>(["overview"])

/** Data retention keeps its workspaces in the URL (?scope=&ws=). */
const CONSOLE_TABS: ReadonlySet<TabKey> = new Set<TabKey>(["retention"])

export default function AdminPage() {
  // One Save for the console: every card's edits join the floating bar, and
  // switching section with edits pending asks first.
  return (
    <PageSaveProvider>
      <AdminConsole />
    </PageSaveProvider>
  )
}

function AdminConsole() {
  const router = useRouter()
  const { workspaceId: rememberedWorkspaceId, loading: wsLoading, workspaceUnavailable } = useWorkspace()
  // PAR-093: the console is instance-wide. A remembered workspace that is not
  // one of this person's own (an instance admin who belongs to none, or whose
  // last choice was removed) is fenced by the client and every request that
  // names it fails with "workspace unavailable". Such a person reads the
  // instance's figures with no workspace, like one who has none.
  const workspaceId = workspaceUnavailable ? null : rememberedWorkspaceId
  // The console belongs to instance administrators (instance_admin.go on the
  // server): it retunes the whole server, so a workspace OWNER or ADMIN role
  // does not open it. null while the session loads — wait, judge nothing.
  const instanceAdmin = useIsInstanceAdmin()
  const isAdmin = instanceAdmin === true
  // Deep-linkable, like /settings?tab=. An unknown or absent key falls back to
  // Overview, the section every admin can read.
  const [tab, _setTab] = useState<TabKey>(() =>
    typeof window === "undefined" ? "overview" : initialAdminTab(window.location.search),
  )
  // Phone (PNL-05): /admin is the list of sections; a pick opens that section
  // with a Back to the list. A link that names a section (?tab=) opens it
  // straight away. Desktop and tablet keep list and section side by side.
  const isMobile = useIsMobile()
  const [phoneDetail, setPhoneDetail] = useState(() =>
    typeof window !== "undefined" && new URLSearchParams(window.location.search).has("tab"),
  )
  const phoneList = isMobile && !phoneDetail
  const guard = usePageSaveGuard()
  const setTab = useCallback((next: TabKey) => guard(() => {
    _setTab(next)
    setPhoneDetail(true)
    // replaceState, not a route push: this is the same document, and a history
    // entry per sidebar click would turn Back into "undo my last five clicks".
    if (typeof window !== "undefined") {
      const url = new URL(window.location.href)
      url.searchParams.set("tab", next)
      url.searchParams.delete("section")
      // The workspaces Data retention edits mean nothing anywhere else.
      if (!CONSOLE_TABS.has(next)) {
        url.searchParams.delete("scope")
        url.searchParams.delete("ws")
      }
      window.history.replaceState(null, "", url.toString())
    }
  }), [guard])
  // Phone: back from a section to the list, dropping ?tab= so a reload stays there.
  const backToList = useCallback(() => guard(() => {
    setPhoneDetail(false)
    if (typeof window !== "undefined") {
      const url = new URL(window.location.href)
      for (const k of ["tab", "section", "scope", "ws"]) url.searchParams.delete(k)
      window.history.replaceState(null, "", url.toString())
    }
  }), [guard])
  // Universal search doubles as a command-finder — filters the nav live.
  const [navQuery, setNavQuery] = useState("")
  const closePanelSheet = useCloseSecondaryPanelSheet()
  const navQ = navQuery.trim().toLowerCase()
  // Hooks must run before the early returns below, so keep this memo up here.
  const filteredSections = useMemo(() => filterNav(navQ), [navQ])
  const firstNavMatch = filteredSections[0]?.items[0]
  const [stats, setStats] = useState<Stats | null>(null)
  // The Overview's reads, each landing on its own (hooks/use-admin-overview):
  // the slow journal walk no longer holds the whole page on a skeleton.
  const overview = useAdminOverview(workspaceId, isAdmin && !wsLoading && tab === "overview")
  // A 403/500/network failure on the primary fetches must be visible, not a
  // silently empty table (#868). Populated by fetchData; cleared on success.
  const [fetchError, setFetchError] = useState<{ title: string; detail?: string } | null>(null)

  const [runtimeAvailable, setRuntimeAvailable] = useState<boolean | null>(null)
  const [runtimeInfo, setRuntimeInfo] = useState<{ runtime: string; version: string; socket: string } | null>(null)
  const [allRuntimes, setAllRuntimes] = useState<RuntimeEntry[]>([])
  const [runtimeInstallLinks, setRuntimeInstallLinks] = useState<Record<string, string>>({})
  const [runtimeChecking, setRuntimeChecking] = useState(false)

  const [keeperStatus, setKeeperStatus] = useState<KeeperStatus | null>(null)

  const checkRuntime = useCallback(async () => {
    setRuntimeChecking(true)
    try {
      // Pass workspace_id so the backend resolves this caller as ADMIN+ and
      // returns full host detail (versions/sockets) rather than the redacted
      // availability-only shape non-admin surfaces get (#865).
      const res = await apiFetch(withWs("/api/v1/system/runtime", workspaceId))
      if (!res.ok) {
        setRuntimeAvailable(false)
        return
      }
      const data = await res.json()
      setRuntimeAvailable(data.available)
      // install_links arrives on both paths since #1690 — an operator with one
      // runtime installed still needs to be told what the others are.
      setRuntimeInstallLinks(data.install_links ?? {})
      setAllRuntimes(data.runtimes ?? [])
      // The top-level summary is null when runtimes are installed but none is
      // in use (the server booted without a container provider). Keep it null
      // rather than manufacturing a {runtime: null} object — the overview
      // renders "none in use" off exactly that distinction.
      setRuntimeInfo(
        data.available && data.runtime
          ? { runtime: data.runtime, version: data.version, socket: data.socket }
          : null,
      )
    } catch {
      setRuntimeAvailable(false)
    } finally {
      setRuntimeChecking(false)
    }
  }, [workspaceId])

  // An old ?tab= link (Users, Workspaces, Security, Reviews, Backups) moved to
  // a nested page. It follows the move straight away, admin or not, without
  // waiting for the session: the nested page decides what this person may
  // see there, so there is nothing to judge here.
  useEffect(() => {
    const moved = movedAdminTabHref(window.location.search)
    if (moved) router.replace(moved)
  }, [router])

  // Lifted out of the effect so an action on a tab (creating a workspace,
  // adding a member) can ask for the same refresh the page does on mount —
  // a list that does not catch up after a create reads as a failed create.
  // A generation counter rather than a captured flag: lifting this into a
  // useCallback left a `const` that nothing could ever flip, so every
  // staleness check below was unreachable and a slow response for the previous
  // workspace could overwrite the current one's screen. A ref survives the
  // callback being recreated, which a local no longer does.
  const fetchGeneration = useRef(0)
  const fetchData = useCallback(async () => {
    // An instance admin with no workspace gets the instance's figures.
    // While the workspace directory loads the client fence refuses every
    // workspace-scoped request; the read is made once it has answered.
    if (!isAdmin || wsLoading) return
    const generation = ++fetchGeneration.current
    const isStale = () => generation !== fetchGeneration.current
    try {
      // People and workspaces load on their own page (/admin/people); the
      // console itself needs only the figures the Overview reads.
      const statsRes = await apiFetch(withWs("/api/v1/admin/stats", workspaceId))
      if (isStale()) return
      // A failure is visible, not a silently empty card — the honesty pass (#868).
      setFetchError(statsRes.ok ? null : {
        title: "Couldn’t load the instance figures",
        detail: `HTTP ${statsRes.status} from GET /api/v1/admin/stats${statsRes.status === 403 ? " — needs an instance administrator" : ""}`,
      })
      if (statsRes.ok) setStats(await statsRes.json())
    } catch (e) {
      if (!isStale()) setFetchError({ title: "Couldn’t load the instance figures", detail: e instanceof Error ? e.message : "Network error loading admin data." })
    }
  }, [workspaceId, isAdmin, wsLoading])

  useEffect(() => {
    void fetchData()
  }, [fetchData])

  // The Overview's one-line keeper verdict. Everything else about the Keeper
  // lives on its own page now (/admin/security).
  const fetchKeeperData = useCallback(async () => {
    try {
      const statusRes = await apiFetch(withWs("/api/v1/system/keeper", workspaceId))
      if (statusRes.ok) setKeeperStatus(await statusRes.json())
    } catch {
      // The Overview line then reads "unknown", which is what it is.
    }
  }, [workspaceId])

  useEffect(() => {
    if (isAdmin && !wsLoading) checkRuntime()
  }, [isAdmin, wsLoading, checkRuntime])

  useEffect(() => {
    if (isAdmin && !wsLoading && tab === "overview") fetchKeeperData()
  }, [isAdmin, wsLoading, tab, fetchKeeperData])

  // KIT-03: a person who is not an instance administrator is told so, on the
  // page they opened, instead of being sent home without a word. Nothing of
  // the console is read or drawn for them.
  if (instanceAdmin === false && !wsLoading) {
    return (
      <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col">
        <SubBar icon={Shield} title="Admin" ariaLabel="Admin Console" />
        <PageNoAccess
          area="the Admin console"
          needed="an instance administrator"
          primary={{ label: "Back to Home", href: "/" }}
          className="flex-1"
        />
      </div>
    )
  }

  if (wsLoading || !isAdmin) return <AdminLoading />

  function renderContent() {
    if (tab === "overview") {
      return (
        <OverviewTab
          stats={stats}
          runtimeAvailable={runtimeAvailable}
          runtimeInfo={runtimeInfo}
          health={overview.health}
          license={overview.license}
          telemetry={overview.telemetry}
          version={overview.version}
          posture={overview.posture}
          journal={overview.journal}
          journalPending={!overview.settled.journal}
          keeper={keeperStatus}
          daemon={overview.daemon}
          aux={overview.aux}
          agents={overview.agents}
          keeperHealth={overview.keeperHealth}
          runs={overview.runs}
          cost={overview.cost}
          noWorkspace={!workspaceId && !wsLoading}
        />
      )
    }

    if (tab === "providers") {
      return (
        <RuntimeTab
          runtimeChecking={runtimeChecking}
          runtimeAvailable={runtimeAvailable}
          allRuntimes={allRuntimes}
          runtimeInstallLinks={runtimeInstallLinks}
          onCheckRuntime={checkRuntime}
          workspaceId={workspaceId}
        />
      )
    }

    if (tab === "notifications") {
      return <NotificationsTab workspaceId={workspaceId} />
    }

    if (tab === "ratelimits") {
      return <RateLimitsTab workspaceId={workspaceId} />
    }

    if (tab === "retention") {
      return <RetentionPage />
    }

    return null
  }

  const sectionLabel = adminSectionLabel(tab)
  const item = adminSectionItem(tab)
  // The release this build can update to, when the server knows one.
  const releaseUrl = overview.version?.newer ? overview.version.url ?? null : null

  /* The panel's content: a column on a desktop, the page itself on a phone
     while no section is open, a bottom sheet once one is. */
  const adminNav = (
    <>
      <SidebarToolbar>
        <SidebarSearch
          value={navQuery}
          onValueChange={setNavQuery}
          placeholder="Search admin…"
          onKeyDown={(e) => {
            if (e.key === "Enter" && firstNavMatch) {
              if (firstNavMatch.href) { const href = firstNavMatch.href; guard(() => router.push(href)) }
              else setTab(firstNavMatch.key as TabKey)
              closePanelSheet()
            }
          }}
        />
      </SidebarToolbar>
      <div data-slot="admin-panel-head" className="flex items-center gap-1.5 px-3 pb-0.5 pt-1 text-label font-semibold text-sidebar-foreground/80">
        <Shield className="h-3.5 w-3.5" aria-hidden />
        Admin Console
        <span className="ml-1 rounded-sm border border-border px-1 text-micro font-semibold leading-normal text-muted-foreground">Instance admin</span>
      </div>
      <nav className="min-h-0 flex-1 overflow-y-auto pb-4" aria-label="Admin sections">
        {filteredSections.map((section) => (
          <SidebarSection key={section.label} label={section.label}>
            {section.items.map((navItem) => {
              const isActive = !phoneList && navItem.key === tab
              return (
                <SidebarRow
                  key={navItem.key}
                  selected={isActive}
                  onSelect={() => {
                    if (navItem.href) { const href = navItem.href; guard(() => router.push(href)) }
                    else setTab(navItem.key as TabKey)
                    closePanelSheet()
                  }}
                  aria-label={navItem.label}
                >
                  <AdminNavTile icon={navItem.icon} active={isActive} />
                  <span className="truncate flex-1">{navItem.label}</span>
                  {navItem.href && <ChevronRight className="h-3.5 w-3.5 shrink-0 opacity-50" aria-hidden />}
                </SidebarRow>
              )
            })}
          </SidebarSection>
        ))}
      </nav>
    </>
  )

  const wide = WIDE_TABS.has(tab)

  return (
    <div className="flex flex-col h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))]">
      <SubBar
        icon={Shield}
        title="Admin"
        section={phoneList ? undefined : sectionLabel}
        ariaLabel="Admin Console"
        leading={isMobile && !phoneList ? (
          <Button variant="ghost" size="icon-sm" className="kit-tap -ml-1" aria-label="Back to Admin" onClick={backToList}>
            <ArrowLeft className="h-3.5 w-3.5" />
          </Button>
        ) : undefined}
        actions={tab === "overview" && !phoneList && releaseUrl ? (
          <SubBarSecondary icon={ExternalLink} asChild>
            <a href={releaseUrl} target="_blank" rel="noopener noreferrer">Release notes</a>
          </SubBarSecondary>
        ) : undefined}
      />

      <div className="flex flex-1 min-h-0">
        {/* The one SecondaryPanel: a 272 px column on a desktop; on a phone
            the page itself until a section opens, then the top bar's sheet. */}
        <SecondaryPanel kind="list" id="admin" label="Admin navigation" phoneInline={phoneList}>{adminNav}</SecondaryPanel>

        {/* tabIndex + role/label, not decoration: this pane scrolls, and the
            default section (Overview) renders nothing focusable inside it, so
            without a tab stop a keyboard-only admin cannot scroll it at all
            (axe: scrollable-region-focusable). The label names the section
            rather than saying "content", so the landmark list stays useful. */}
        {!phoneList && (
        <div className="relative flex-1 min-w-0">
        <div
          className="h-full overflow-y-auto pb-20"
          tabIndex={0}
          role="region"
          aria-label={sectionLabel ? `Admin ${sectionLabel}` : "Admin content"}
        >
          <div data-slot="admin-content" data-wide={wide || undefined} className={cn("mx-auto space-y-4 p-4 md:p-6", wide ? "max-w-screen-2xl" : "max-w-3xl")}>
            {!wide && item && (
              <SettingsPageHead icon={item.icon} title={item.label} description={item.about} className="mb-5" />
            )}
            {fetchError && tab === "overview" && (
              <PageError className="px-0 py-0" title={fetchError.title} detail={fetchError.detail}
                description="The checks and cards below still load on their own."
                onRetry={() => void fetchData()} />
            )}
            {renderContent()}
          </div>
        </div>
        <PageSaveBar />
        </div>
        )}
      </div>
    </div>
  )
}

/** Loading: the shape of what is coming — the strip of checks and two rows of cards. */
function AdminLoading() {
  return (
    <div className="mx-auto w-full max-w-screen-2xl space-y-4 p-4 md:p-6" role="status" aria-busy="true" aria-label="Loading the Admin console">
      <div className="flex items-center gap-3">
        <Skeleton className="h-12 w-12 rounded-card" />
        <div className="space-y-1.5"><Skeleton className="h-4 w-40" /><Skeleton className="h-3 w-64" /></div>
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
        {Array.from({ length: 6 }, (_, i) => <Skeleton key={i} className="h-14 rounded-card" />)}
      </div>
      <Skeleton className="h-20 rounded-card" />
      <div className="grid gap-4 lg:grid-cols-3">
        {Array.from({ length: 3 }, (_, i) => <Skeleton key={i} className="h-40 rounded-card" />)}
      </div>
    </div>
  )
}
