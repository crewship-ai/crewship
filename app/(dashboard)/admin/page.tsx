"use client"

import { useEffect, useMemo, useState, useCallback, useRef } from "react"
import { useRouter } from "next/navigation"
import {
  Shield, AlertTriangle, ChevronRight, Menu,
} from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { withWs } from "@/lib/admin-workspace-query"
import { SubBar } from "@/components/layout/sub-bar"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { useIsMobile } from "@/hooks/use-mobile"
import {
  SidebarToolbar, SidebarSearch, SidebarSection, SidebarRow, SIDEBAR_WIDTH,
} from "@/components/layout/sidebar-kit"

import {
  initialAdminTab, movedAdminTabHref, adminSectionLabel, filterNav, ALL_TABS,
} from "./navigation"
import type { TabKey, Stats, KeeperStatus } from "./types"
import { useAdminOverview } from "./hooks/use-admin-overview"
import { OverviewTab } from "./tabs/overview-tab"
import { RuntimeTab } from "./tabs/runtime-tab"
import type { RuntimeEntry } from "./tabs/runtime-tab"
import { BackupsConsole } from "@/components/features/admin/backups/backups-console"
import { NotificationsTab } from "./tabs/notifications-tab"
import { RateLimitsTab } from "./tabs/rate-limits-tab"
import { PageSaveBar, PageSaveProvider, usePageSaveGuard } from "@/components/ui/page-save-bar"

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


/**
 * Sections that are settings (cards of rows) read in the same centred 768px
 * column Settings uses. Dashboards and tables keep the wide column.
 *
 * No heading repeats the section inside the page: the sub-bar already names
 * it, exactly as Settings does, and each card says what it is for.
 */
const SETTINGS_TABS: ReadonlySet<TabKey> = new Set<TabKey>(["providers", "notifications", "ratelimits"])

/** Data retention draws its own workspace strip and column. */
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
  const { workspaceId, loading: wsLoading } = useWorkspace()
  // The console belongs to instance administrators (instance_admin.go on the
  // server): it retunes the whole server, so a workspace OWNER or ADMIN role
  // does not open it. null while the session loads — wait, do not redirect.
  const instanceAdmin = useIsInstanceAdmin()
  const isAdmin = instanceAdmin === true
  // Deep-linkable, like /settings?tab=. Admin was the one console whose URL
  // never changed — /admin whichever section you were on — so a section could
  // not be bookmarked, linked in a ticket, or reloaded without losing your
  // place. An unknown or absent key falls back to Overview, the section every
  // admin can read.
  const [tab, _setTab] = useState<TabKey>(() =>
    typeof window === "undefined" ? "overview" : initialAdminTab(window.location.search),
  )
  const guard = usePageSaveGuard()
  const setTab = useCallback((next: TabKey) => guard(() => {
    _setTab(next)
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
  // Universal search doubles as a command-finder — filters the nav live.
  const [navQuery, setNavQuery] = useState("")
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const isMobile = useIsMobile()
  const navQ = navQuery.trim().toLowerCase()
  // Hooks must run before the early returns below, so keep this memo up here.
  const filteredSections = useMemo(() => filterNav(navQ), [navQ])
  const firstNavMatch = filteredSections[0]?.items[0]
  const [stats, setStats] = useState<Stats | null>(null)
  // The Overview's reads, each landing on its own (hooks/use-admin-overview):
  // the slow journal walk no longer holds the whole page on a skeleton.
  const overview = useAdminOverview(workspaceId, isAdmin && tab === "overview")
  const [loading, setLoading] = useState(true)
  // A 403/500/network failure on the primary fetches must be visible, not a
  // silently empty table (#868). Populated by fetchData; cleared on success.
  const [fetchError, setFetchError] = useState<string | null>(null)

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

  useEffect(() => {
    if (wsLoading || instanceAdmin === null) return
    if (!isAdmin) {
      router.push("/")
      return
    }
    // Workspaces and Users moved to their own page; an old link lands there.
    const moved = movedAdminTabHref(window.location.search)
    if (moved) router.replace(moved)
  }, [wsLoading, instanceAdmin, isAdmin, router])

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
    if (!isAdmin) return
    const generation = ++fetchGeneration.current
    const isStale = () => generation !== fetchGeneration.current
    {
      setLoading(true)
      try {
        // People and workspaces load on their own page (/admin/people); the
        // console itself needs only the figures the Overview reads.
        const statsRes = await apiFetch(withWs("/api/v1/admin/stats", workspaceId))
        if (isStale()) return
        // A failure is visible, not a silently empty card — the honesty pass (#868).
        setFetchError(statsRes.ok ? null
          : `Failed to load stats (HTTP ${statsRes.status}${statsRes.status === 403 ? " — needs an instance administrator" : ""}).`)
        if (statsRes.ok) setStats(await statsRes.json())
      } catch (e) {
        if (!isStale()) setFetchError(e instanceof Error ? e.message : "Network error loading admin data.")
      } finally {
        if (!isStale()) setLoading(false)
      }
    }
  }, [workspaceId, isAdmin])

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
    if (isAdmin) checkRuntime()
  }, [isAdmin, checkRuntime])

  useEffect(() => {
    if (isAdmin && tab === "overview") fetchKeeperData()
  }, [isAdmin, tab, fetchKeeperData])

  if (wsLoading || !isAdmin) {
    return (
      <div className="p-4 md:p-6">
        <Skeleton className="h-8 w-48 mb-3" />
        <Skeleton className="h-[300px] rounded-xl" />
      </div>
    )
  }

  function renderContent() {
    // Overview fills card by card, so it never waits on the tables.
    if (loading && tab !== "overview" && ALL_TABS.includes(tab)) {
      return <Skeleton className="h-[200px] rounded-xl" />
    }

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




    return null
  }

  const sectionLabel = adminSectionLabel(tab)

  /* One nav body, rendered into a permanent column on a desktop and into a
     Sheet on a phone. Choosing a section closes the Sheet — on a desktop
     `setMobileNavOpen(false)` is a no-op. */
  const adminNav = (
    <>
      {/* pr-10 on the phone: SheetContent draws its close button at
          top-4 right-4 over this row, and it would otherwise take taps
          meant for the search field. */}
      <SidebarToolbar className={isMobile ? "pr-10" : undefined}>
        <SidebarSearch
          value={navQuery}
          onValueChange={setNavQuery}
          placeholder="Search admin…"
          onKeyDown={(e) => {
            if (e.key === "Enter" && firstNavMatch) {
              if (firstNavMatch.href) { const href = firstNavMatch.href; guard(() => router.push(href)) }
              else setTab(firstNavMatch.key as TabKey)
              setMobileNavOpen(false)
            }
          }}
        />
      </SidebarToolbar>
      <nav className="flex-1 overflow-y-auto pb-4" aria-label="Admin sections">
        {filteredSections.map((section) => (
          <SidebarSection key={section.label} label={section.label}>
            {section.items.map((item) => {
              const Icon = item.icon
              const isActive = item.key === tab
              return (
                <SidebarRow
                  key={item.key}
                  selected={isActive}
                  onSelect={() => {
                    if (item.href) { const href = item.href; guard(() => router.push(href)) }
                    else setTab(item.key as TabKey)
                    setMobileNavOpen(false)
                  }}
                  aria-label={item.label}
                >
                  <Icon className={cn("h-3.5 w-3.5 shrink-0", isActive ? "opacity-100" : "opacity-60")} />
                  <span className="truncate flex-1">{item.label}</span>
                  {item.href && <ChevronRight className="h-3.5 w-3.5 shrink-0 opacity-50" aria-hidden />}
                </SidebarRow>
              )
            })}
          </SidebarSection>
        ))}
      </nav>
    </>
  )

  return (
    <div className="flex flex-col h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))]">
      {/* Identity lives in the sub-bar (not repeated in the sidebar). */}
      <SubBar
        icon={Shield}
        title="Admin Console"
        section={sectionLabel}
        ariaLabel="Admin Console"
        /* The console's own nav is a 280px column. On a phone that leaves the
           page 109px to render into, which is not a narrow layout — it is no
           layout (#2483). Below `md` the same nav moves into a Sheet, reached
           from here, exactly as Settings does. */
        leading={
          isMobile ? (
            <Button
              variant="ghost"
              size="icon-sm"
              className="h-7 w-7 -ml-1 coarse:relative coarse:after:absolute coarse:after:-inset-2.5 coarse:after:content-['']"
              aria-label="Open admin navigation"
              onClick={() => setMobileNavOpen(true)}
            >
              <Menu className="h-3.5 w-3.5" />
            </Button>
          ) : undefined
        }
        meta={
          <span className="text-micro font-mono uppercase tracking-wide text-muted-foreground-soft">Instance admin</span>
        }
      />

      <div className="flex flex-1 min-h-0">
        {/* ── Left nav ─────────────────────────────────────────────── */}
        {isMobile ? (
          <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
            <SheetContent side="left" className="w-[280px] max-w-[85vw] p-0">
              <SheetHeader className="sr-only">
                <SheetTitle>Admin navigation</SheetTitle>
              </SheetHeader>
              <div className="flex h-full flex-col bg-sidebar">{adminNav}</div>
            </SheetContent>
          </Sheet>
        ) : (
          <aside className={cn(SIDEBAR_WIDTH, "shrink-0 border-r border-border bg-sidebar flex flex-col overflow-hidden")}>
            {adminNav}
          </aside>
        )}

        {/* ── Content ─────────────────────────────────────────────── */}
        {/* tabIndex + role/label, not decoration: this pane scrolls, and the
            default section (Overview) renders nothing focusable inside it, so
            without a tab stop a keyboard-only admin cannot scroll it at all
            (axe: scrollable-region-focusable). The label names the section
            rather than saying "content", so the landmark list stays useful. */}
        <div className="relative flex-1 min-w-0">
        <div
          className="h-full overflow-y-auto pb-20"
          tabIndex={0}
          role="region"
          aria-label={sectionLabel ? `Admin ${sectionLabel}` : "Admin content"}
        >
        {CONSOLE_TABS.has(tab) ? (
          <BackupsConsole page="retention" onNavigate={(s) => guard(() => router.push(`/admin/backups?section=${s}`))} />
        ) : (
        <div className={cn("mx-auto space-y-4 p-4 md:p-6", SETTINGS_TABS.has(tab) ? "max-w-3xl" : "max-w-5xl")}>
          {fetchError && (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn"
            >
              <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
              <span>{fetchError}</span>
            </div>
          )}
          {renderContent()}
        </div>
        )}
      </div>
        <PageSaveBar />
      </div>
      </div>
    </div>
  )
}
